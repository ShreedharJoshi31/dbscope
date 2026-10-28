package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
)

const verifyTable = `CREATE TABLE t (
  id INT AUTO_INCREMENT PRIMARY KEY,
  a INT NOT NULL,
  b VARCHAR(50) CHARACTER SET utf8mb4,
  l VARCHAR(100) CHARACTER SET latin1,
  n INT NULL,
  e ENUM('x','y'),
  p INT
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC`

// verifyCases are ALTER clauses whose cost dbscope predicts.
var verifyCases = []struct {
	kind, clause string
	fkChecksOff  bool
	cascade      bool // t gets a foreign key with ON DELETE CASCADE first
}{
	{"modify column", "MODIFY a BIGINT NOT NULL", false, true}, // MariaDB's online COPY excludes cascades
	{"add column", "ADD COLUMN z INT", false, false},
	{"add column", "ADD COLUMN z INT FIRST", false, false},
	{"add column", "ADD COLUMN z INT AS (a + 1) STORED", false, false},
	{"add column", "ADD COLUMN z INT AS (a + 1) VIRTUAL", false, false},
	{"drop column", "DROP COLUMN n", false, false},
	{"rename column", "RENAME COLUMN n TO n2", false, false},
	{"modify column", "MODIFY b VARCHAR(60) CHARACTER SET utf8mb4", false, false}, // 200 to 240 bytes: same length prefix
	{"modify column", "MODIFY b VARCHAR(70) CHARACTER SET utf8mb4", false, false}, // past 255 bytes
	{"modify column", "MODIFY l VARCHAR(120) CHARACTER SET latin1", false, false}, // 100 to 120 bytes
	{"modify column", "MODIFY a BIGINT NOT NULL", false, false},                   // type change
	{"modify column", "MODIFY n INT NOT NULL", false, false},                      // NULL to NOT NULL
	{"modify column", "MODIFY a INT NULL", false, false},                          // NOT NULL to NULL
	{"modify column", "MODIFY e ENUM('x','y','z')", false, false},                 // append a member
	{"modify column", "MODIFY e ENUM('z','x','y')", false, false},                 // insert a member
	{"change column", "CHANGE n n3 INT NULL", false, false},                       // rename only
	{"alter column", "ALTER COLUMN a SET DEFAULT 5", false, false},
	{"add index", "ADD INDEX i (a)", false, false},
	{"add unique", "ADD UNIQUE INDEX u (a)", false, false},
	{"add fulltext", "ADD FULLTEXT INDEX f (b)", false, false},
	{"add check", "ADD CONSTRAINT ck CHECK (a >= 0)", false, false},
	{"add foreign key", "ADD CONSTRAINT fk FOREIGN KEY (p) REFERENCES parent (id)", false, false},
	{"add foreign key", "ADD CONSTRAINT fk FOREIGN KEY (p) REFERENCES parent (id)", true, false},
	{"drop primary key", "DROP PRIMARY KEY, ADD PRIMARY KEY (id, a)", false, false},
	{"convert charset", "CONVERT TO CHARACTER SET latin1", false, false},
	{"rebuild", "FORCE", false, false},
	{"table option", "COMMENT = 'x'", false, false},
	{"rename table", "RENAME TO t2", false, false},
}

// TestDDLPredictions runs each clause on real servers and compares the
// cheapest algorithm they accept, whether LOCK=NONE works and whether the
// table was rebuilt with what planClause predicts.
func TestDDLPredictions(t *testing.T) {
	for _, flavor := range []string{"mysql", "mariadb"} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			p := mytest.Server(t, flavor)
			db, err := Open(p, mytest.Password, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1) // SET foreign_key_checks must apply to the ALTER's connection
			ctx := context.Background()
			srv, err := Describe(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			algos := []Algo{Instant, Inplace, Copy}
			if flavor == "mariadb" {
				algos = []Algo{Instant, Nocopy, Inplace, Copy}
			}
			for _, c := range verifyCases {
				name := c.clause
				if c.fkChecksOff {
					name += " (foreign_key_checks=0)"
				}
				if c.cascade {
					name += " (cascading foreign key)"
				}
				t.Run(name, func(t *testing.T) {
					reset := func() {
						for _, q := range []string{"SET foreign_key_checks = 1", "DROP TABLE IF EXISTS t, t2", "DROP TABLE IF EXISTS parent",
							"CREATE TABLE parent (id INT PRIMARY KEY)", verifyTable,
							"INSERT INTO t (a, b, l, n, e) VALUES (1, 'one', 'one', 1, 'x'), (2, 'two', 'two', 2, 'y')"} {
							if _, err := db.ExecContext(ctx, q); err != nil {
								t.Fatalf("%s: %v", q, err)
							}
						}
						if c.cascade {
							if _, err := db.ExecContext(ctx, "ALTER TABLE t ADD CONSTRAINT c FOREIGN KEY (p) REFERENCES parent (id) ON DELETE CASCADE"); err != nil {
								t.Fatal(err)
							}
						}
						if c.fkChecksOff {
							db.ExecContext(ctx, "SET foreign_key_checks = 0")
						}
					}
					try := func(opts string) error {
						reset()
						_, err := db.ExecContext(ctx, "ALTER TABLE t "+c.clause+", "+opts)
						return err
					}
					var actual Algo = -1
					for _, a := range algos {
						if try("ALGORITHM="+a.String()) == nil {
							actual = a
							break
						}
					}
					if actual < 0 {
						t.Fatalf("no algorithm accepted")
					}
					// MySQL accepts no LOCK clause with INSTANT, which changes only metadata.
					dml := (actual == Instant && flavor == "mysql") || try(fmt.Sprintf("ALGORITHM=%s, LOCK=NONE", actual)) == nil
					reset()
					before := tableID(t, db, flavor)
					if _, err := db.ExecContext(ctx, "ALTER TABLE t "+c.clause+", ALGORITHM="+actual.String()); err != nil {
						t.Fatal(err)
					}
					rebuilt := before != tableID(t, db, flavor)

					reset()
					base, err := ReadBase(ctx, db, nil, "app")
					if err != nil {
						t.Fatal(err)
					}
					toks := Tokens(c.clause)
					clauses := clauseSplit(toks)
					var kinds []string
					for _, cl := range clauses {
						kinds = append(kinds, clauseKind(words(cl)))
					}
					got := Plan{Algo: Instant, DML: true}
					for i, cl := range clauses {
						got = worse(got, planClause(srv, base.Tables["app.t"], Session{ForeignKeyChecks: !c.fkChecksOff}, kinds[i], cl, kinds))
					}
					// INPLACE and NOCOPY differ only in whether MariaDB accepts the stricter word.
					if got.Algo != actual || got.DML != dml || got.Rebuild != rebuilt {
						t.Errorf("predicted %s dml=%v rebuild=%v; %s %s says %s dml=%v rebuild=%v",
							got.Algo, got.DML, got.Rebuild, flavor, srv.Version, actual, dml, rebuilt)
					}
				})
			}
		})
	}
}

// tableID is InnoDB's id for app/t (or app/t2 after a rename); a rebuild assigns a new one.
func tableID(t *testing.T, db *sql.DB, flavor string) string {
	q := `SELECT TABLE_ID FROM information_schema.INNODB_TABLES WHERE NAME IN ('app/t', 'app/t2')`
	if flavor == "mariadb" {
		q = `SELECT TABLE_ID FROM information_schema.INNODB_SYS_TABLES WHERE NAME IN ('app/t', 'app/t2')`
	}
	var id string
	if err := db.QueryRow(q).Scan(&id); err != nil {
		t.Fatalf("table id: %v", err)
	}
	return strings.TrimSpace(id)
}

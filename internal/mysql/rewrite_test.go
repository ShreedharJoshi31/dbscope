package mysql

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestRewriteLive runs each rewrite on real servers: it must succeed, leave
// the same tables as the original, and drop DBS-MYSQL-LOCK-WAIT without
// raising the rating.
func TestRewriteLive(t *testing.T) {
	const rw = "SET SESSION lock_wait_timeout = 5;\n"
	cases := []struct {
		name, pre, sql string
		want           map[string]string // by flavor; "" for no rewrite
	}{
		{name: "instant add column", sql: "ALTER TABLE t ADD COLUMN z INT",
			want: both(rw + "ALTER TABLE t ADD COLUMN z INT, ALGORITHM=INSTANT")},
		{name: "online add index", sql: "ALTER TABLE t ADD INDEX i (a)",
			want: map[string]string{"mysql": rw + "ALTER TABLE t ADD INDEX i (a), ALGORITHM=INPLACE, LOCK=NONE",
				"mariadb": rw + "ALTER TABLE t ADD INDEX i (a), ALGORITHM=NOCOPY, LOCK=NONE"}},
		{name: "create index", sql: "CREATE INDEX i ON t (a)",
			want: map[string]string{"mysql": rw + "CREATE INDEX i ON t (a) ALGORITHM=INPLACE LOCK=NONE",
				"mariadb": rw + "CREATE INDEX i ON t (a) ALGORITHM=NOCOPY LOCK=NONE"}},
		{name: "type change that copies gets no algorithm", sql: "ALTER TABLE t MODIFY a BIGINT NOT NULL",
			want: both(rw + "ALTER TABLE t MODIFY a BIGINT NOT NULL")},
		{name: "explicit algorithm is kept", sql: "ALTER TABLE t ADD COLUMN z INT, ALGORITHM=INPLACE",
			want: both(rw + "ALTER TABLE t ADD COLUMN z INT, ALGORITHM=INPLACE")},
		{name: "optimize gets no algorithm", sql: "OPTIMIZE TABLE t", want: both(rw + "OPTIMIZE TABLE t")},
		{name: "drop table", sql: "DROP TABLE t2", want: both(rw + "DROP TABLE t2")},
		{name: "lock_wait_timeout already set", pre: "SET SESSION lock_wait_timeout = 5;", sql: "ALTER TABLE t ADD COLUMN z INT", want: both("")},
		{name: "statement that fails", sql: "ALTER TABLE t ADD FULLTEXT INDEX f (b), LOCK=NONE", want: both("")},
		{name: "type change with LOCK=NONE fails on MySQL only", sql: "ALTER TABLE t MODIFY a BIGINT NOT NULL, LOCK=NONE",
			want: map[string]string{"mysql": "", "mariadb": rw + "ALTER TABLE t MODIFY a BIGINT NOT NULL, LOCK=NONE"}},
	}
	for _, flavor := range []string{"mysql", "mariadb"} {
		t.Run(flavor, func(t *testing.T) {
			db, err := Open(mytest.Server(t, flavor), mytest.Password, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1) // SET SESSION must apply to the next statement's connection
			ctx := context.Background()
			reset := func() {
				t.Helper()
				for _, q := range []string{"SET SESSION lock_wait_timeout = DEFAULT", "DROP TABLE IF EXISTS t, t2",
					"CREATE TABLE t (id INT AUTO_INCREMENT PRIMARY KEY, a INT NOT NULL, b VARCHAR(50)) ENGINE=InnoDB",
					"CREATE TABLE t2 (id INT PRIMARY KEY)", "INSERT INTO t (a, b) VALUES (1, 'one'), (2, 'two'), (3, 'three')"} {
					if _, err := db.ExecContext(ctx, q); err != nil {
						t.Fatalf("%s: %v", q, err)
					}
				}
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					reset()
					base, err := ReadBase(ctx, db, nil, "app")
					if err != nil {
						t.Fatal(err)
					}
					analyze := func(stmt string) []migrate.Statement {
						return Analyze(base, []ingest.Input{{Name: "m.sql", SQL: c.pre + "\n" + stmt + ";\n"}}).Statements
					}
					stmts := analyze(c.sql)
					target := stmts[len(stmts)-1]
					got := ""
					if target.Rewrite != nil {
						got = target.Rewrite.SQL
						if target.Rewrite.Note == "" {
							t.Error("rewrite has no note")
						}
					}
					if got != c.want[flavor] {
						t.Fatalf("rewrite\n got %q\nwant %q\nfindings %v", got, c.want[flavor], ruleIDs(target))
					}
					if got == "" {
						return
					}
					after := analyze(got)[len(stmts)-1:]
					var afterIDs []string
					for _, s := range after {
						if s.Rating.Rank() > target.Rating.Rank() {
							t.Errorf("%q rated %s, original %s", s.SQL, s.Rating, target.Rating)
						}
						afterIDs = append(afterIDs, ruleIDs(s)...)
					}
					if !slices.Contains(ruleIDs(target), "DBS-MYSQL-LOCK-WAIT") || slices.Contains(afterIDs, "DBS-MYSQL-LOCK-WAIT") {
						t.Errorf("findings %v -> %v", ruleIDs(target), afterIDs)
					}
					t.Logf("%s %v -> %v", target.Rating, ruleIDs(target), afterIDs)

					exec := func(script string) string {
						t.Helper()
						reset()
						for _, p := range Split(script) {
							if _, err := db.ExecContext(ctx, p.SQL); err != nil {
								t.Fatalf("%s: %v", p.SQL, err)
							}
						}
						return tables(t, db)
					}
					if a, b := exec(c.pre+"\n"+c.sql), exec(c.pre+"\n"+got); a != b {
						t.Errorf("tables differ\noriginal: %s\nrewrite:  %s", a, b)
					}
				})
			}
		})
	}
}

func both(s string) map[string]string { return map[string]string{"mysql": s, "mariadb": s} }

func ruleIDs(s migrate.Statement) []string {
	var out []string
	for _, f := range s.Findings {
		out = append(out, f.RuleID)
	}
	return out
}

// tables is SHOW CREATE TABLE of t and t2, or "missing".
func tables(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out []string
	for _, n := range []string{"t", "t2"} {
		var name, ddl string
		if err := db.QueryRow("SHOW CREATE TABLE "+n).Scan(&name, &ddl); err != nil {
			ddl = n + " missing"
		}
		out = append(out, ddl)
	}
	return strings.Join(out, "\n")
}

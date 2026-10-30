package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

func blockedFinding(s migrate.Statement) *migrate.Finding {
	for i, f := range s.Findings {
		if f.RuleID == migrate.BlockedNow {
			return &s.Findings[i]
		}
	}
	return nil
}

// TestBlockersMySQL holds a transaction open on one connection, analyzes a
// DDL on another and checks the reported blockers against the server: the
// DDL really times out on lock_wait_timeout exactly when dbscope says it waits.
func TestBlockersMySQL(t *testing.T) {
	for _, c := range []struct {
		name, flavor string
		args         []string
		mdl          bool
	}{
		{"mysql", "mysql", nil, true},
		{"mariadb with performance_schema", "mariadb", []string{"--performance-schema=ON", "--performance-schema-instrument=wait/lock/metadata/sql/mdl=ON"}, true},
		{"mariadb default", "mariadb", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) { testBlockers(t, c.flavor, c.args, c.mdl) })
	}
}

func testBlockers(t *testing.T, flavor string, args []string, mdlVisible bool) {
	p := mytest.Server(t, flavor, args...)
	ctx := context.Background()
	db, err := Open(p, mytest.Password, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"CREATE TABLE t (id INT PRIMARY KEY, a INT)", "INSERT INTO t VALUES (1, 1)", "CREATE TABLE other (id INT PRIMARY KEY)"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	var holderID int64
	if err := holder.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&holderID); err != nil {
		t.Fatal(err)
	}
	const ddl = "ALTER TABLE t COMMENT = 'x'"
	waits := func() bool {
		t.Helper()
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.ExecContext(ctx, "SET SESSION lock_wait_timeout = 1"); err != nil {
			t.Fatal(err)
		}
		_, err = c.ExecContext(ctx, ddl)
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1205 {
			return true
		}
		if err != nil {
			t.Fatal(err)
		}
		return false
	}
	analyze := func(as *sql.DB) *migrate.Report {
		t.Helper()
		base, err := ReadBase(ctx, as, nil, "app")
		if err != nil {
			t.Fatal(err)
		}
		rep := Analyze(base, []ingest.Input{{Name: "m.sql", SQL: ddl + ";\nCREATE TABLE fresh (id INT)"}})
		ReadBlockers(ctx, as, base, rep)
		return rep
	}
	open := func(read string) func() {
		t.Helper()
		if _, err := holder.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := holder.ExecContext(ctx, read); err != nil {
			t.Fatal(err)
		}
		return func() { holder.ExecContext(ctx, "ROLLBACK") }
	}

	t.Run("no open transaction", func(t *testing.T) {
		rep := analyze(db)
		if len(rep.Blockers) != 0 || blockedFinding(rep.Statements[0]) != nil || waits() {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
		if mdlVisible == (rep.BlockersAt == nil) {
			t.Errorf("BlockersAt %v, warnings %v", rep.BlockersAt, rep.Warnings)
		}
		if !mdlVisible && !strings.Contains(strings.Join(rep.Warnings, "\n"), "performance_schema is off on this server, so dbscope cannot tell which tables") {
			t.Errorf("warnings %v", rep.Warnings)
		}
	})

	t.Run("transaction on another table", func(t *testing.T) {
		defer open("SELECT * FROM other")()
		rep := analyze(db)
		if len(rep.Blockers) != 0 || blockedFinding(rep.Statements[0]) != nil || waits() {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
	})

	t.Run("idle transaction that read the table", func(t *testing.T) {
		defer open("SELECT * FROM t")()
		time.Sleep(1100 * time.Millisecond)
		rep := analyze(db)
		if !waits() {
			t.Fatal("the server does not wait")
		}
		if !mdlVisible {
			w := strings.Join(rep.Warnings, "\n")
			if len(rep.Blockers) != 0 || !strings.Contains(w, fmt.Sprintf("connection %d (root), idle in transaction for ", holderID)) {
				t.Fatalf("blockers %+v, warnings %v", rep.Blockers, rep.Warnings)
			}
			t.Logf("observed warning: %s", w)
			return
		}
		if len(rep.Blockers) != 1 {
			t.Fatalf("blockers %+v, warnings %v", rep.Blockers, rep.Warnings)
		}
		// MariaDB leaves the events_statements_current consumer off, so an idle connection's last statement is unknown.
		wantQuery := "SELECT * FROM t"
		if flavor == "mariadb" {
			wantQuery = ""
		}
		b := rep.Blockers[0]
		if b.Relation != "app.t" || b.Session != fmt.Sprint(holderID) || b.User != "root" || b.State != "idle in transaction" ||
			b.Lock != "SHARED_READ metadata lock" || b.AgeSeconds < 1 || b.Query != wantQuery {
			t.Errorf("blocker %+v", b)
		}
		f := blockedFinding(rep.Statements[0])
		if f == nil || f.Rating != "high" || rep.Statements[0].Rating != "high" ||
			!strings.Contains(f.Message, fmt.Sprintf("connection %d (root), idle in transaction for ", holderID)) || !strings.Contains(f.Message, "s, holding SHARED_READ metadata lock on app.t") {
			t.Errorf("finding %+v", f)
		}
		if blockedFinding(rep.Statements[1]) != nil {
			t.Error("CREATE TABLE of a new table marked blocked")
		}
		t.Logf("observed: %+v", b)
	})

	if flavor == "mysql" {
		t.Run("user without performance_schema or PROCESS", func(t *testing.T) {
			for _, q := range []string{"CREATE USER plain IDENTIFIED BY 'plain-pass'", "GRANT SELECT ON app.* TO plain"} {
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			pp := p
			pp.User = "plain"
			as, err := Open(pp, "plain-pass", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			defer as.Close()
			defer open("SELECT * FROM t")()
			rep := analyze(as)
			w := strings.Join(rep.Warnings, "\n")
			if len(rep.Blockers) != 0 || rep.BlockersAt != nil || !strings.Contains(w, "cannot name the sessions") {
				t.Fatalf("blockers %+v, warnings %v", rep.Blockers, rep.Warnings)
			}
			t.Logf("observed warnings: %s", w)
		})
	}
}

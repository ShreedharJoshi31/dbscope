package oracle

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestBusyTable writes 4,000 rows to one table and 20 to another on a real
// server, then checks DBS-HOT-TABLE against what DBA_TAB_MODIFICATIONS
// counted: an index build that blocks writes to the busy table is rated one
// step above its DBS-ORA-BLOCKS-DML, a DDL that needs its lock at once with
// ddl_lock_timeout 0 names the table at Medium, and the quiet table, an
// ONLINE build or a session timeout gets neither.
func TestBusyTable(t *testing.T) {
	p := mytest.Oracle(t)
	ctx := context.Background()
	db, err := Open(p, mytest.OraclePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE USER shop IDENTIFIED BY " + shopPass + " DEFAULT TABLESPACE sysaux QUOTA UNLIMITED ON sysaux",
		"CREATE TABLE shop.hot (id NUMBER PRIMARY KEY, body VARCHAR2(50))",
		"CREATE TABLE shop.quiet (id NUMBER PRIMARY KEY, body VARCHAR2(50))",
		"BEGIN DBMS_STATS.GATHER_SCHEMA_STATS('SHOP'); END;",
		"INSERT INTO shop.hot SELECT level, 'x' FROM dual CONNECT BY level <= 2000",
		"UPDATE shop.hot SET body = 'y'",
		"INSERT INTO shop.quiet SELECT level, 'x' FROM dual CONNECT BY level <= 20",
		"COMMIT",
		// dbscope never flushes; the test does, so the counts do not depend on when the server last did.
		"BEGIN DBMS_STATS.FLUSH_DATABASE_MONITORING_INFO; END;",
	} {
		mustExec(t, db, q)
	}

	base, err := ReadBase(ctx, db, []string{"SHOP"})
	if err != nil {
		t.Fatal(err)
	}
	if base.Traffic == nil || base.Traffic.Writes["SHOP.HOT"] != 4000 || base.Traffic.Writes["SHOP.QUIET"] != 20 {
		t.Fatalf("traffic: %+v, warnings %v", base.Traffic, base.Warnings)
	}
	find := func(st migrate.Statement, id string) *migrate.Finding {
		for i := range st.Findings {
			if st.Findings[i].RuleID == id {
				return &st.Findings[i]
			}
		}
		return nil
	}
	for _, c := range []struct {
		label, sql string
		want       score.Rating
	}{
		{"index build on the busy table", "CREATE INDEX ix_body ON hot (body);", score.High},
		{"index build on the quiet table", "CREATE INDEX ix_body ON quiet (body);", ""},
		{"online index build", "CREATE INDEX ix_body ON hot (body) ONLINE;", ""},
		{"lock needed at once, ddl_lock_timeout 0", "ALTER TABLE hot RENAME COLUMN body TO note;", score.Medium},
		{"lock needed at once, ddl_lock_timeout 5", "ALTER SESSION SET ddl_lock_timeout = 5;\nALTER TABLE hot RENAME COLUMN body TO note;", ""},
	} {
		rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: c.sql}})
		st := rep.Statements[len(rep.Statements)-1]
		f := find(st, "DBS-HOT-TABLE")
		switch {
		case c.want == "" && f != nil:
			t.Errorf("%s: unexpected %+v", c.label, *f)
		case c.want == "":
		case f == nil:
			t.Errorf("%s: no DBS-HOT-TABLE in %+v", c.label, st.Findings)
		case f.Rating != c.want || st.Rating != score.Max(st.Rating, c.want) || !strings.Contains(f.Message, "SHOP.HOT has 100% of the rows written"):
			t.Errorf("%s: %s %q, statement %s; want %s", c.label, f.Rating, f.Message, st.Rating, c.want)
		case c.want == score.High:
			if b := find(st, ruleBlocksDML); b == nil || b.Rating.Up() != f.Rating {
				t.Errorf("%s: not one step above %s: %+v", c.label, ruleBlocksDML, st.Findings)
			}
		}
	}
}

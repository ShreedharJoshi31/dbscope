package oracle

import (
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

func testBase() *Base {
	return &Base{Server: Server{Version: "23.26.3.0.0", Num: 232600}, Online: true, OnlineKnown: true, Schema: "APP", Source: "live", Recyclebin: "on",
		Tables: map[string]*TableInfo{"APP.T": {Display: "APP.T", Rows: 2_000_000, RowsKnown: true, Bytes: 80 << 20, Sized: true,
			Columns:     map[string]ColumnInfo{"A": {Type: "NUMBER", Nullable: true}, "B": {Type: "VARCHAR2(40)", Nullable: true}},
			Constraints: map[string]string{"T_CK": "rows breaking a >= 0 can be written", "T_PK": "duplicate (ID) values can be written, and NULLs too"},
			PrimaryKey:  "T_PK", UniqueIndexes: map[string]string{"T_B_U": "duplicate (B) values can be written"}}},
		Indexes: map[string]string{"APP.T_B_U": "APP.T", "APP.T_PK": "APP.T"}}
}

func TestAnalyzeStatements(t *testing.T) {
	for _, c := range []struct {
		sql, kind, work string
		rules           []string
		relations       []string
	}{
		{"alter table t add z number", "ALTER TABLE", "none", []string{"DBS-ORA-DDL-LOCK(low)"}, []string{"APP.T"}},
		{`ALTER TABLE "APP"."T" ADD (z NUMBER, y NUMBER NOT NULL)`, "ALTER TABLE", "none", []string{"DBS-ORA-ADD-NOT-NULL(high)", "DBS-ORA-DDL-LOCK(low)"}, []string{"APP.T"}},
		{"CREATE INDEX app.t_a ON app.t (a)", "CREATE INDEX", "index_build", []string{"DBS-ORA-BLOCKS-DML(critical)", "DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T"}},
		{"CREATE INDEX app.t_a ON app.t (a) ONLINE", "CREATE INDEX", "index_build", []string{"DBS-ORA-TABLE-WORK(medium)", "DBS-ORA-DDL-LOCK(low)"}, []string{"APP.T"}},
		{"ALTER TABLE app.t DROP CONSTRAINT t_ck", "ALTER TABLE", "none", []string{"DBS-DROP-CONSTRAINT(medium)", "DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T"}},
		{"ALTER TABLE app.t DROP PRIMARY KEY", "ALTER TABLE", "none", []string{"DBS-DROP-CONSTRAINT(medium)", "DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T"}},
		{"DROP INDEX app.t_b_u", "DROP INDEX", "none", []string{"DBS-DROP-CONSTRAINT(medium)", "DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T_B_U", "APP.T"}},
		{"DROP TABLE app.t", "DROP TABLE", "none", []string{"DBS-ORA-DROPS-DATA(medium)", "DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T"}},
		{"DROP TABLE app.t PURGE", "DROP TABLE", "none", []string{"DBS-ORA-DROPS-DATA(high)", "DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T"}},
		{"ALTER TABLE app.t MODIFY b VARCHAR2(10)", "ALTER TABLE", "scan", []string{"DBS-ORA-NARROW(medium)", "DBS-ORA-BLOCKS-DML(critical)", "DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T"}},
		{"ALTER TABLE app.t MODIFY b VARCHAR2(400)", "ALTER TABLE", "none", []string{"DBS-ORA-DDL-LOCK(medium)"}, []string{"APP.T"}},
		{"BEGIN EXECUTE IMMEDIATE 'DROP TABLE app.t'; END;", "PL/SQL", "none", nil, nil},
		{"SET DEFINE OFF", "SQL*PLUS SET", "none", nil, nil},
	} {
		rep := Analyze(testBase(), []ingest.Input{{Name: "V1__a.sql", SQL: c.sql}})
		if len(rep.Statements) != 1 {
			t.Fatalf("%s: %d statements", c.sql, len(rep.Statements))
		}
		s := rep.Statements[0]
		if got := ruleIDs(s); s.Kind != c.kind || s.Work != c.work || !slices.Equal(got, c.rules) || !slices.Equal(s.Relations, c.relations) {
			t.Errorf("%s: kind %s, work %s, rules %v, relations %v; want %s, %s, %v, %v", c.sql, s.Kind, s.Work, got, s.Relations, c.kind, c.work, c.rules, c.relations)
		}
	}
}

func TestAnalyzeSession(t *testing.T) {
	rep := Analyze(testBase(), []ingest.Input{{Name: "V1__a.sql", SQL: "CREATE INDEX app.i1 ON app.t (a);\nALTER SESSION SET ddl_lock_timeout = 10;\nCREATE INDEX app.i2 ON app.t (b)"}})
	first, last := rep.Statements[0], rep.Statements[2]
	if f, g := finding(first, ruleDDLLock), finding(last, ruleDDLLock); f == nil || f.Rating != "medium" || g == nil || g.Rating != "low" || !strings.Contains(g.Message, "10 s") || !last.LockTimeout {
		t.Errorf("before: %v, after: %v (lock timeout %v)", ruleIDs(first), ruleIDs(last), last.LockTimeout)
	}
}

func TestAnalyzeRewrites(t *testing.T) {
	for sql, want := range map[string]string{
		"CREATE INDEX app.t_a ON app.t (a)":                                "CREATE INDEX app.t_a ON app.t (a) ONLINE",
		"ALTER TABLE app.t ADD CONSTRAINT t_ck2 CHECK (a < 10)":            "ALTER TABLE app.t ADD CONSTRAINT t_ck2 CHECK (a < 10) ENABLE NOVALIDATE;\nALTER TABLE APP.T MODIFY CONSTRAINT T_CK2 VALIDATE",
		"ALTER TABLE app.t DROP COLUMN b":                                  "ALTER TABLE APP.T SET UNUSED (B) ONLINE",
		"ALTER TABLE app.t MOVE":                                           "ALTER TABLE app.t MOVE ONLINE",
		"CREATE INDEX app.t_a ON app.t (a) ONLINE":                         "",
		"ALTER TABLE app.t ADD CHECK (a < 10)":                             "",
		"ALTER TABLE app.t ADD CONSTRAINT t_ck2 CHECK (a < 10) DEFERRABLE": "",
	} {
		rep := Analyze(testBase(), []ingest.Input{{Name: "V1__a.sql", SQL: sql}})
		got := ""
		if r := rep.Statements[0].Rewrite; r != nil {
			got = r.SQL
		}
		if got != want {
			t.Errorf("%s: rewrite %q, want %q", sql, got, want)
		}
	}
	b := testBase()
	b.Online = false
	if r := Analyze(b, []ingest.Input{{Name: "a.sql", SQL: "CREATE INDEX app.t_a ON app.t (a)"}}).Statements[0].Rewrite; r != nil {
		t.Errorf("rewrite without Online Index Build: %+v", r)
	}
	s := Analyze(b, []ingest.Input{{Name: "a.sql", SQL: "CREATE INDEX app.t_a ON app.t (a) ONLINE"}}).Statements[0]
	if finding(s, ruleOnline) == nil {
		t.Errorf("ONLINE without Online Index Build: %v", ruleIDs(s))
	}
}

func TestAnalyzeRollbacks(t *testing.T) {
	rep := Analyze(testBase(), []ingest.Input{
		{Name: "1_drop.up.sql", SQL: "ALTER TABLE app.t DROP COLUMN b"},
		{Name: "1_drop.down.sql", SQL: "ALTER TABLE app.t ADD b VARCHAR2(40)"},
		{Name: "2_add.up.sql", SQL: "ALTER TABLE app.t ADD c NUMBER"},
	})
	if len(rep.Statements) != 2 {
		t.Fatalf("%d statements; the down file must not be analyzed", len(rep.Statements))
	}
	if f := finding(rep.Statements[0], "DBS-DOWN-LOSES-DATA"); f == nil || !strings.Contains(f.Message, "column APP.T.B") {
		t.Errorf("up 1: %v", ruleIDs(rep.Statements[0]))
	}
	if finding(rep.Statements[1], "DBS-DOWN-MISSING") == nil {
		t.Errorf("up 2: %v", ruleIDs(rep.Statements[1]))
	}
}

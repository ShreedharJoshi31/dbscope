package mssql

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestBusyTable runs 1,200 INSERT statements on one table and 5 on another on
// a real server, then checks DBS-HOT-TABLE against what
// sys.dm_db_index_usage_stats counted: an offline index build on the busy
// table is rated one step above its DBS-MSSQL-OFFLINE-INDEX, a metadata-only
// change there without LOCK_TIMEOUT names the table at Medium, and the quiet
// table or a bounded wait gets neither. A login without VIEW SERVER
// PERFORMANCE STATE leaves ratings alone and says why.
func TestBusyTable(t *testing.T) {
	for _, name := range []string{"2025", "2022"} {
		t.Run(name, func(t *testing.T) { testBusyTable(t, name) })
	}
}

func testBusyTable(t *testing.T, name string) {
	p := mytest.MSSQL(t, name)
	ctx := context.Background()
	admin, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	mustExec(t, admin, "CREATE DATABASE shop")
	p.Database = "shop"
	db, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE TABLE dbo.hot (id int PRIMARY KEY, body varchar(50))",
		"CREATE TABLE dbo.quiet (id int PRIMARY KEY, body varchar(50))",
		"DECLARE @i int = 0; WHILE @i < 1200 BEGIN INSERT dbo.hot VALUES (@i, 'x'); SET @i += 1 END",
		"DECLARE @i int = 0; WHILE @i < 5 BEGIN INSERT dbo.quiet VALUES (@i, 'x'); SET @i += 1 END",
		"CREATE LOGIN ro WITH PASSWORD = '" + roPass + "', CHECK_POLICY = OFF",
		"CREATE USER ro FOR LOGIN ro",
		"ALTER ROLE db_datareader ADD MEMBER ro",
	} {
		mustExec(t, db, q)
	}

	base, err := ReadBase(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if base.Traffic == nil || base.Traffic.Writes["dbo.hot"] < 1200 {
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
	check := func(base *DDLBase, label, sql string, want score.Rating) {
		t.Helper()
		rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: sql}})
		st := rep.Statements[len(rep.Statements)-1]
		f := find(st, "DBS-HOT-TABLE")
		switch {
		case want == "" && f != nil:
			t.Errorf("%s: unexpected %+v", label, *f)
		case want == "":
		case f == nil:
			t.Errorf("%s: no DBS-HOT-TABLE in %+v", label, st.Findings)
		case f.Rating != want || score.Max(st.Rating, want) != st.Rating || !strings.Contains(f.Message, "dbo.hot has"):
			t.Errorf("%s: %s %q, statement %s; want %s", label, f.Rating, f.Message, st.Rating, want)
		case want == score.High:
			if o := find(st, "DBS-MSSQL-OFFLINE-INDEX"); o == nil || o.Rating.Up() != f.Rating || !strings.Contains(f.Message, "writes to it wait") {
				t.Errorf("%s: not one step above DBS-MSSQL-OFFLINE-INDEX, or reads said to wait: %+v", label, st.Findings)
			}
		}
	}
	check(base, "offline index on the busy table", "CREATE INDEX ix_body ON dbo.hot (body);", score.High)
	check(base, "offline index on the quiet table", "CREATE INDEX ix_body ON dbo.quiet (body);", "")
	check(base, "metadata change, no LOCK_TIMEOUT", "ALTER TABLE dbo.hot ADD c int NULL;", score.Medium)
	check(base, "metadata change, bounded wait", "SET LOCK_TIMEOUT 5000;\nALTER TABLE dbo.hot ADD c int NULL;", "")

	ro := p
	ro.User = "ro"
	rodb, err := Open(ro, roPass)
	if err != nil {
		t.Fatal(err)
	}
	defer rodb.Close()
	roBase, err := ReadBase(ctx, rodb, nil)
	if err != nil {
		t.Fatal(err)
	}
	granted := func(w string) bool { return strings.Contains(w, "GRANT VIEW SERVER PERFORMANCE STATE in master") }
	if roBase.Traffic != nil || !slices.ContainsFunc(roBase.Warnings, granted) {
		t.Fatalf("without the permission: traffic %+v, warnings %v", roBase.Traffic, roBase.Warnings)
	}
	check(roBase, "offline index without the permission", "CREATE INDEX ix_body ON dbo.hot (body);", "")
}

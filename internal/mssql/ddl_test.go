package mssql

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

func ddlTestBase(edition int) *DDLBase {
	return &DDLBase{
		Server: Server{Version: "16.0.4", Num: 160000, Edition: "Developer Edition (64-bit)"}, EngineEdition: edition,
		Schema: "dbo", Source: "live",
		Tables: map[string]*DDLTable{
			"dbo.orders": {Rows: 5_000_000, Bytes: 2 << 30, Sized: true,
				Columns: map[string]*DDLColumn{
					"total":    {Type: "decimal(10,2)", Checked: true},
					"note":     {Type: "varchar(50)", Nullable: true},
					"code":     {Type: "varchar(20)", Nullable: true, Indexed: true},
					"qty":      {Type: "int"},
					"cust_id":  {Type: "int", Nullable: true, ForeignKey: true},
					"status":   {Type: "char(10)", Default: true},
					"optional": {Type: "int", Nullable: true},
				},
				Constraints:    map[string]string{"fk_cust": "(cust_id) is no longer checked against dbo.customers", "uq_code": "duplicate (code) values can be written", "ck_total": "rows breaking ([total]>=(0)) can be written"},
				KeyIndexes:     map[string]bool{"uq_code": true, "pk_orders": true},
				ForeignKeyRefs: map[string]string{"fk_cust": "dbo.customers"},
			},
			"dbo.customers": {Rows: 10, Bytes: 16384, Sized: true, ReferencedBy: 1, Columns: map[string]*DDLColumn{}, Constraints: map[string]string{},
				KeyIndexes: map[string]bool{}, ForeignKeyRefs: map[string]string{}},
			"dbo.empty": {Rows: 0, Bytes: 0, Sized: true, Columns: map[string]*DDLColumn{}, Constraints: map[string]string{},
				KeyIndexes: map[string]bool{}, ForeignKeyRefs: map[string]string{}},
		},
	}
}

func ddlFindings(r *migrate.Report) map[string][]string {
	out := map[string][]string{}
	for _, s := range r.Statements {
		k := s.File + ":" + strconv.Itoa(s.Line)
		out[k] = []string{}
		for _, f := range s.Findings {
			out[k] = append(out[k], f.RuleID+"/"+string(f.Rating))
		}
	}
	return out
}

func TestDDLAnalyze(t *testing.T) {
	files := []ingest.Input{{Name: "1.sql", SQL: `ALTER TABLE orders ADD flag int NULL
ALTER TABLE orders ADD made datetime2 NOT NULL DEFAULT sysdatetime()
ALTER TABLE orders ADD g uniqueidentifier NOT NULL DEFAULT newid()
ALTER TABLE orders ADD must int NOT NULL
ALTER TABLE empty ADD must int NOT NULL
SET LOCK_TIMEOUT 5000;
ALTER TABLE orders ALTER COLUMN note varchar(100) NULL
ALTER TABLE orders ALTER COLUMN note nvarchar(50) NULL
ALTER TABLE orders ALTER COLUMN optional int NOT NULL
ALTER TABLE orders ALTER COLUMN qty int
ALTER TABLE orders ALTER COLUMN code varchar(10) NULL
ALTER TABLE orders ALTER COLUMN code varchar(40) NULL
ALTER TABLE orders ALTER COLUMN cust_id bigint NULL
ALTER TABLE orders ALTER COLUMN qty bigint NOT NULL WITH (ONLINE = ON)
CREATE INDEX by_qty ON orders (qty)
CREATE INDEX by_qty ON dbo.orders (qty) WITH (ONLINE = ON)
ALTER TABLE orders ADD CONSTRAINT fk2 FOREIGN KEY (qty) REFERENCES customers (id)
ALTER TABLE orders WITH NOCHECK ADD CONSTRAINT fk2 FOREIGN KEY (qty) REFERENCES customers (id)
ALTER TABLE orders WITH NOCHECK ADD CONSTRAINT ck2 CHECK (qty > 0)
ALTER TABLE orders NOCHECK CONSTRAINT fk_cust
ALTER TABLE orders DROP COLUMN note
ALTER TABLE orders DROP COLUMN status
DROP INDEX uq_code ON orders
TRUNCATE TABLE customers
DROP TABLE customers
ALTER TABLE orders DROP CONSTRAINT fk_cust, ck_total
ALTER TABLE orders DROP COLUMN status
EXEC sp_rename 'dbo.orders.qty', 'quantity', 'COLUMN'
CREATE TABLE made_here (id int)`}}
	rep := Analyze(ddlTestBase(3), files)
	got := ddlFindings(rep)
	lw := "DBS-MSSQL-LOCK-WAIT/medium"
	want := map[string][]string{
		"1.sql:1": {lw},
		"1.sql:2": {lw},
		"1.sql:3": {"DBS-MSSQL-BLOCKS-TABLE/critical", lw},
		"1.sql:4": {"DBS-MSSQL-FAILS/high", lw},
		"1.sql:5": {lw},
		"1.sql:6": {},
		// LOCK_TIMEOUT is set from here on
		"1.sql:7":  {},
		"1.sql:8":  {"DBS-MSSQL-BLOCKS-TABLE/critical"},
		"1.sql:9":  {"DBS-MSSQL-TABLE-SCAN/high"},
		"1.sql:10": {"DBS-MSSQL-NULLABLE/medium"},
		"1.sql:11": {"DBS-MSSQL-FAILS/high"},
		"1.sql:12": {},
		"1.sql:13": {"DBS-MSSQL-FAILS/high"},
		"1.sql:14": {"DBS-MSSQL-TABLE-WORK/high"},
		"1.sql:15": {"DBS-MSSQL-OFFLINE-INDEX/critical"},
		"1.sql:16": {"DBS-MSSQL-TABLE-WORK/high"},
		"1.sql:17": {"DBS-MSSQL-TABLE-SCAN/high"},
		"1.sql:18": {"DBS-MSSQL-UNTRUSTED-FK/medium"},
		"1.sql:19": {"DBS-MSSQL-UNTRUSTED-CHECK/medium"},
		"1.sql:20": {"DBS-MSSQL-DISABLED-FK/medium"},
		"1.sql:21": {"DBS-MSSQL-DROPS-DATA/high"},
		"1.sql:22": {"DBS-MSSQL-FAILS/high"},
		"1.sql:23": {"DBS-MSSQL-FAILS/high"},
		"1.sql:24": {"DBS-MSSQL-FAILS/high"},
		"1.sql:25": {"DBS-MSSQL-FAILS/high"},
		"1.sql:26": {"DBS-DROP-CONSTRAINT/medium", "DBS-DROP-CONSTRAINT/medium"},
		// the default may have been dropped by an earlier statement
		"1.sql:27": {"DBS-MSSQL-DROPS-DATA/high"},
		"1.sql:28": {},
		"1.sql:29": {},
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected %s: %v", k, got[k])
		}
	}
	st := rep.Statements
	if s := st[14]; s.Rewrite == nil || s.Rewrite.SQL != "CREATE INDEX by_qty ON orders (qty) WITH (ONLINE = ON, RESUMABLE = ON)" ||
		s.Locks[0] != (migrate.Lock{Relation: "dbo.orders", Mode: "S"}) || s.Work != "index_build" || *s.SizeBytes != 2<<30 {
		t.Errorf("create index %+v", s)
	}
	if s := st[16]; len(s.Locks) != 2 || s.Locks[1] != (migrate.Lock{Relation: "dbo.customers", Mode: "Sch-M"}) || !slices.Equal(s.Relations, []string{"dbo.orders", "dbo.customers"}) {
		t.Errorf("foreign key %+v", s)
	}
	if !st[6].LockTimeout || st[0].LockTimeout {
		t.Errorf("lock timeout %v %v", st[0].LockTimeout, st[6].LockTimeout)
	}
	if rep.Engine != "sqlserver" || rep.Rating != "critical" || !slices.Equal(rep.Diff.Dropped, []string{"dbo.customers"}) || rep.Statements[24].Findings[0].Message != "DROP TABLE dbo.customers fails (error 3726): a foreign key of another table references it" ||
		!slices.Equal(rep.Diff.Added, []string{"dbo.made_here"}) {
		t.Errorf("report %s %s %v %v", rep.Engine, rep.Rating, rep.Diff.Dropped, rep.Diff.Added)
	}
	renamed := rep.Diff.Columns[len(rep.Diff.Columns)-1]
	if renamed.Column != "qty" || renamed.To != "quantity" || renamed.Change != "renamed" || renamed.Relation != "dbo.orders" {
		t.Errorf("rename %+v", renamed)
	}
}

// TestDDLAnalyzeEdition checks what changes on an edition without online operations (Express, 4).
func TestDDLAnalyzeEdition(t *testing.T) {
	rep := Analyze(ddlTestBase(4), []ingest.Input{{Name: "1.sql", SQL: `SET LOCK_TIMEOUT 0
ALTER TABLE orders ADD made datetime2 NOT NULL DEFAULT sysdatetime()
CREATE INDEX by_qty ON orders (qty) WITH (ONLINE = ON)
CREATE INDEX by_qty ON orders (qty)`}})
	got := ddlFindings(rep)
	for k, v := range map[string][]string{
		"1.sql:2": {"DBS-MSSQL-BLOCKS-TABLE/critical"},
		"1.sql:3": {"DBS-MSSQL-FAILS/high"},
		"1.sql:4": {"DBS-MSSQL-OFFLINE-INDEX/critical"},
	} {
		if !slices.Equal(got[k], v) {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
	if s := rep.Statements[3]; s.Rewrite != nil || !strings.Contains(s.Findings[0].SafeAlternative, "no online index builds") {
		t.Errorf("express index %+v", s)
	}
}

func TestDDLDown(t *testing.T) {
	rep := Analyze(ddlTestBase(3), []ingest.Input{
		{Name: "1_drop.up.sql", SQL: "SET LOCK_TIMEOUT 1000\nALTER TABLE orders DROP COLUMN note"},
		{Name: "1_drop.down.sql", SQL: "ALTER TABLE orders ADD note varchar(50) NULL"},
		{Name: "2_x.up.sql", SQL: "SET LOCK_TIMEOUT 1000"},
	})
	if len(rep.Statements) != 3 {
		t.Fatalf("down files are not migrations: %d statements", len(rep.Statements))
	}
	got := ddlFindings(rep)
	if !slices.Contains(got["1_drop.up.sql:1"], "DBS-DOWN-LOSES-DATA/medium") || !slices.Contains(got["2_x.up.sql:1"], "DBS-DOWN-MISSING/medium") {
		t.Errorf("%v", got)
	}
}

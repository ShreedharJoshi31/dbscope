package mssql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

// TestIndexFindings seeds duplicate, unused and missing indexes, and reads
// them as a db_datareader login before and after it may read the DMVs.
func TestIndexFindings(t *testing.T) {
	for _, name := range []string{"2025", "2022"} {
		t.Run(name, func(t *testing.T) { testIndexFindings(t, name) })
	}
}

func testIndexFindings(t *testing.T, name string) {
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
	const rows = `WITH n AS (SELECT TOP (%d) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS i FROM sys.all_objects a CROSS JOIN sys.all_objects b) `
	for _, q := range []string{
		// Missing: events and logs are filtered on unindexed columns; quiet has the index the same query needs.
		"CREATE TABLE dbo.events (id int IDENTITY PRIMARY KEY, kind int NOT NULL, at datetime2 NOT NULL, note nvarchar(100))",
		strings.Replace(rows, "%d", "20000", 1) + "INSERT dbo.events (kind, at, note) SELECT i % 5000, DATEADD(MINUTE, i, '2026-01-01'), N'x' FROM n",
		"CREATE TABLE dbo.logs (id int IDENTITY PRIMARY KEY, src int NOT NULL, lvl int NOT NULL, msg nvarchar(100))",
		strings.Replace(rows, "%d", "20000", 1) + "INSERT dbo.logs (src, lvl, msg) SELECT i % 4000, i % 7, N'm' FROM n",
		"CREATE TABLE dbo.quiet (id int IDENTITY PRIMARY KEY, kind int NOT NULL, at datetime2 NOT NULL, note nvarchar(100))",
		"CREATE INDEX ix_quiet_kind_at ON dbo.quiet(kind, at) INCLUDE (note)",
		strings.Replace(rows, "%d", "20000", 1) + "INSERT dbo.quiet (kind, at, note) SELECT i % 5000, DATEADD(MINUTE, i, '2026-01-01'), N'x' FROM n",
		// Unused: status is written and never read, customer is read, code is unique.
		"CREATE TABLE dbo.orders (id int IDENTITY PRIMARY KEY, customer int NOT NULL, status int NOT NULL, code varchar(20))",
		"CREATE INDEX ix_orders_status ON dbo.orders(status)",
		"CREATE INDEX ix_orders_customer ON dbo.orders(customer)",
		"CREATE UNIQUE INDEX ux_orders_code ON dbo.orders(code) WHERE code IS NOT NULL",
		strings.Replace(rows, "%d", "5000", 1) + "INSERT dbo.orders (customer, status, code) SELECT i % 1000, i % 4, 'c' + CAST(i AS varchar(10)) FROM n",
		"INSERT dbo.orders (customer, status) VALUES (1, 1)",
		"INSERT dbo.orders (customer, status) VALUES (2, 2)",
		// Duplicates.
		"CREATE TABLE dbo.dup_same (id int PRIMARY KEY, a int, b int)",
		"CREATE INDEX ix_same1 ON dbo.dup_same(a, b)",
		"CREATE INDEX ix_same2 ON dbo.dup_same(a, b)",
		"CREATE INDEX ix_same_id ON dbo.dup_same(id)",
		"CREATE TABLE dbo.dup_prefix (id int PRIMARY KEY, a int, b int)",
		"CREATE INDEX ix_prefix_a ON dbo.dup_prefix(a)",
		"CREATE INDEX ix_prefix_ab ON dbo.dup_prefix(a, b)",
		// Not duplicates: another order, another direction, a unique prefix,
		// another filter, an included column the longer index lacks.
		"CREATE TABLE dbo.dup_none (id int PRIMARY KEY, a int, b int, c int)",
		"CREATE INDEX ix_none_ab ON dbo.dup_none(a, b)",
		"CREATE INDEX ix_none_ba ON dbo.dup_none(b, a)",
		"CREATE INDEX ix_none_bdesc_a ON dbo.dup_none(b DESC, a)",
		"CREATE UNIQUE INDEX ux_none_a ON dbo.dup_none(a)",
		"CREATE INDEX ix_none_a_filtered ON dbo.dup_none(a) WHERE a > 0",
		"CREATE INDEX ix_none_a_inc ON dbo.dup_none(a) INCLUDE (c)",
		// The same filter is a duplicate, once the login may read filters.
		"CREATE TABLE dbo.dup_filtered (id int PRIMARY KEY, a int)",
		"CREATE INDEX ix_filtered1 ON dbo.dup_filtered(a) WHERE a > 0",
		"CREATE INDEX ix_filtered2 ON dbo.dup_filtered(a) WHERE a > 0",
		"CREATE INDEX ix_filtered3 ON dbo.dup_filtered(a) WHERE a > 1",
		"CREATE LOGIN ro WITH PASSWORD = '" + roPass + "', CHECK_POLICY = OFF",
		"CREATE USER ro FOR LOGIN ro",
		"ALTER ROLE db_datareader ADD MEMBER ro",
	} {
		mustExec(t, db, q)
	}
	// A trivial plan gets no missing index suggestion; ORDER BY makes the optimizer weigh plans.
	for i := range 100 {
		for _, table := range []string{"events", "quiet"} {
			r, err := db.QueryContext(ctx, "SELECT id, note FROM dbo."+table+" WHERE kind = @p1 AND at > @p2 ORDER BY at", i, "2026-01-02")
			if err != nil {
				t.Fatal(err)
			}
			r.Close()
		}
	}
	for i := range 3 {
		r, err := db.QueryContext(ctx, "SELECT msg FROM dbo.logs WHERE src = @p1 ORDER BY lvl", i)
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM dbo.orders WHERE customer = @p1", 7).Scan(&n); err != nil || n != 5 {
		t.Fatal(n, err)
	}

	ro := p
	ro.User = "ro"
	rodb, err := Open(ro, roPass)
	if err != nil {
		t.Fatal(err)
	}
	defer rodb.Close()
	findings := func(m *schema.Model, table, rule string) []schema.Finding {
		t.Helper()
		r := m.Relation(schema.QualifiedName{Schema: "dbo", Name: table})
		if r == nil {
			t.Fatalf("no table %s", table)
		}
		var out []schema.Finding
		for _, f := range r.Findings {
			if f.RuleID == rule {
				out = append(out, f)
			}
		}
		return out
	}
	checkDuplicates := func(m *schema.Model, seesFilters bool) {
		t.Helper()
		if f := findings(m, "dup_same", "DBS-MSSQL-DUPLICATE-INDEX"); len(f) != 2 || f[0].Level != "warn" ||
			f[0].Message != "The index ix_same2 has the same key columns (a, b) as ix_same1, so it looks redundant: every write maintains both, and dropping it loses no rule" ||
			f[0].Column != "a" || !strings.HasPrefix(f[1].Message, "The index ix_same_id has the same key columns (id) as PK__dup_same") || f[1].Column != "id" {
			t.Errorf("dup_same %+v", f)
		}
		if f := findings(m, "dup_prefix", "DBS-MSSQL-DUPLICATE-INDEX"); len(f) != 1 || f[0].Column != "a" ||
			f[0].Message != "The key columns of the index ix_prefix_a (a) lead those of ix_prefix_ab (a, b), which serves the same seeks, so it looks redundant: every write maintains both, and dropping it loses no rule" {
			t.Errorf("dup_prefix %+v", f)
		}
		if f := findings(m, "dup_none", "DBS-MSSQL-DUPLICATE-INDEX"); len(f) != 0 {
			t.Errorf("dup_none %+v", f)
		}
		// Without VIEW DEFINITION filter_definition is NULL, so filtered indexes are never compared.
		f := findings(m, "dup_filtered", "DBS-MSSQL-DUPLICATE-INDEX")
		if seesFilters && (len(f) != 1 || !strings.HasPrefix(f[0].Message, "The index ix_filtered2 has the same key columns (a) as ix_filtered1,")) ||
			!seesFilters && len(f) != 0 {
			t.Errorf("dup_filtered %+v", f)
		}
	}

	// Without VIEW SERVER PERFORMANCE STATE only the catalog rule runs.
	m, err := Introspect(ctx, rodb, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkDuplicates(m, false)
	for _, table := range []string{"events", "logs", "orders"} {
		if f := append(findings(m, table, "DBS-MSSQL-MISSING-INDEX"), findings(m, table, "DBS-MSSQL-UNUSED-INDEX")...); len(f) != 0 {
			t.Errorf("%s without the permission %+v", table, f)
		}
	}

	mustExec(t, admin, "GRANT VIEW SERVER PERFORMANCE STATE TO ro")
	mustExec(t, db, "GRANT VIEW DEFINITION TO ro")
	// The server started minutes ago, under the 7 days unused indexes need.
	if m, err = Introspect(ctx, rodb, nil); err != nil {
		t.Fatal(err)
	}
	checkDuplicates(m, true)
	if f := findings(m, "orders", "DBS-MSSQL-UNUSED-INDEX"); len(f) != 0 {
		t.Errorf("unused on a fresh server %+v", f)
	}
	ev := findings(m, "events", "DBS-MSSQL-MISSING-INDEX")
	if len(ev) != 1 || ev[0].Level != "warn" || ev[0].Column != "kind" ||
		!strings.HasPrefix(ev[0].Message, "The optimizer suggests an index on (kind, at) for queries that compare kind with = and at with another comparison. It estimates they would cost ") ||
		!strings.Contains(ev[0].Message, "% less; they could have used it 100 times since the server started.") {
		t.Errorf("events %+v", ev)
	}
	lg := findings(m, "logs", "DBS-MSSQL-MISSING-INDEX")
	if len(lg) != 1 || lg[0].Level != "info" || lg[0].Column != "src" ||
		!strings.HasPrefix(lg[0].Message, "The optimizer suggests an index on (src) ") || !strings.Contains(lg[0].Message, "could have used it 3 times") {
		t.Errorf("logs %+v", lg)
	}
	if f := findings(m, "quiet", "DBS-MSSQL-MISSING-INDEX"); len(f) != 0 {
		t.Errorf("quiet %+v", f)
	}

	defer func(d time.Duration) { unusedMinUptime = d }(unusedMinUptime)
	unusedMinUptime = 0
	if m, err = Introspect(ctx, rodb, nil); err != nil {
		t.Fatal(err)
	}
	// ix_orders_status was kept up to date by the three inserts, one statement each.
	if f := findings(m, "orders", "DBS-MSSQL-UNUSED-INDEX"); len(f) != 1 || f[0].Level != "warn" || f[0].Column != "status" ||
		!strings.HasPrefix(f[0].Message, "No query read the index ix_orders_status since the server started ") ||
		!strings.Contains(f[0].Message, " ago, but 3 write statements paid to keep it up to date.") {
		t.Errorf("orders %+v", f)
	}
	for _, table := range []string{"events", "logs", "quiet", "dup_same", "dup_prefix", "dup_none"} {
		if f := findings(m, table, "DBS-MSSQL-UNUSED-INDEX"); len(f) != 0 {
			t.Errorf("%s unused %+v", table, f)
		}
	}
}

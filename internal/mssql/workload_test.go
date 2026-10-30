package mssql

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

func TestWorkloadNames(t *testing.T) {
	m := &schema.Model{Relations: []schema.Relation{{Schema: "dbo", Name: "customers"}, {Schema: "sales", Name: "orders"},
		{Schema: "dbo", Name: "items"}, {Schema: "sales", Name: "items"}, {Schema: "dbo", Name: "order"}}}
	ix := newRelIndex(m, "Shop")
	for text, want := range map[string]string{
		"SELECT count(*) FROM sales.orders o JOIN dbo.customers c ON c.id = o.customer_id ORDER BY 1": "sales.orders,dbo.customers",
		"SELECT [total] FROM [Sales].[Orders] WHERE [id]=@1":                                          "sales.orders",
		"SELECT * FROM shop.dbo.customers, shop..customers, other.dbo.items":                          "dbo.customers",
		"SELECT * FROM items":                                          "",
		"SELECT * FROM [order] WHERE x = N'customers'":                 "dbo.order",
		"SELECT o.customers, dbo.fn(1), #customers, @customers FROM t": "",
		"SELECT * FROM sys.objects o JOIN dbo.customers c ON 1 = 1":    "dbo.customers",
	} {
		keys, own := ix.tablesIn(text)
		if strings.Join(keys, ",") != want || own {
			t.Errorf("%s: %v %v, want %s", text, keys, own, want)
		}
	}
	for _, text := range []string{"SET SHOWPLAN_XML ON", "SELECT name FROM sys.tables", "SELECT * FROM INFORMATION_SCHEMA.TABLES",
		"SELECT permission_name FROM fn_my_permissions(NULL, 'DATABASE')"} {
		if keys, own := ix.tablesIn(text); !own || len(keys) != 0 {
			t.Errorf("%s: %v %v, want own", text, keys, own)
		}
	}
}

// TestWorkload runs a known workload on a fresh server and reads it as a
// db_datareader login, before and after it is granted the server permission.
func TestWorkload(t *testing.T) {
	for _, name := range []string{"2025", "2022"} {
		t.Run(name, func(t *testing.T) { testWorkload(t, name) })
	}
}

func testWorkload(t *testing.T, name string) {
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
		"CREATE SCHEMA sales",
		"CREATE TABLE dbo.customers (id int PRIMARY KEY, name nvarchar(100), email varchar(100))",
		"CREATE INDEX ix_customers_name ON dbo.customers(name)",
		"CREATE TABLE sales.orders (id int PRIMARY KEY, customer_id int, total int)",
		"CREATE TABLE dbo.audit (what nvarchar(100))",
		"CREATE TABLE dbo.items (id int PRIMARY KEY)",
		"CREATE TABLE sales.items (id int PRIMARY KEY)",
		"CREATE TABLE dbo.[order] (id int PRIMARY KEY)",
		"INSERT dbo.customers VALUES (1, N'Ada', 'a'), (2, N'Bo', 'b')",
		"INSERT sales.orders VALUES (1, 1, 10), (2, 2, 20)",
		"CREATE LOGIN ro WITH PASSWORD = '" + roPass + "', CHECK_POLICY = OFF",
		"CREATE USER ro FOR LOGIN ro",
		"ALTER ROLE db_datareader ADD MEMBER ro",
		"GRANT SHOWPLAN TO ro",
	} {
		mustExec(t, db, q)
	}
	ro := p
	ro.User = "ro"
	rodb, err := Open(ro, roPass)
	if err != nil {
		t.Fatal(err)
	}
	defer rodb.Close()
	m, err := Introspect(ctx, rodb, nil)
	if err != nil {
		t.Fatal(err)
	}

	snap, err := Workload(ctx, rodb, m)
	if err != nil || snap.Statements.Status != workload.StatusUnavailable || len(snap.Tables) != 0 ||
		!strings.HasSuffix(snap.Statements.Message, "An admin runs, in master: GRANT VIEW SERVER PERFORMANCE STATE TO [ro];") {
		t.Fatalf("without the permission %+v %v", snap, err)
	}
	grant := snap.Statements.Message[strings.Index(snap.Statements.Message, "GRANT "):]

	const join = "SELECT count(*) FROM sales.orders o JOIN dbo.customers c ON c.id = o.customer_id WHERE o.total > @p1"
	for range 4 {
		var n int
		if err := rodb.QueryRowContext(ctx, join, 5).Scan(&n); err != nil || n != 2 {
			t.Fatal(n, err)
		}
	}
	var s string
	if err := rodb.QueryRowContext(ctx, "SELECT TOP (1) name FROM customers WHERE name = @p1 ORDER BY name", "Ada").Scan(&s); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := rodb.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE id > @p1", 0).Scan(&n); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := db.ExecContext(ctx, "INSERT dbo.audit VALUES (@p1)", "x"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "UPDATE dbo.customers SET email = @p1 WHERE id = @p2", "z", i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Run(ctx, rodb, "SELECT total FROM sales.orders WHERE id = 1", 10, false, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if m, err = Introspect(ctx, rodb, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(grant); err == nil || !strings.Contains(err.Error(), "master") {
		t.Errorf("server grant outside master: %v", err)
	}
	mustExec(t, admin, grant)
	// The second read sees the first one's statements in the plan cache.
	for range 2 {
		if snap, err = Workload(ctx, rodb, m); err != nil {
			t.Fatal(err)
		}
	}
	st := snap.Statements
	if st.Status != workload.StatusOK || st.Since == nil || time.Since(*st.Since) > 30*time.Minute || time.Since(*st.Since) < 0 ||
		snap.TableStatsReset == nil || !snap.TableStatsReset.Equal(*st.Since) {
		t.Fatalf("statements %+v", st)
	}
	// Join 4, bare name 1, ambiguous name 1, inserts 3, updates 3, the query runner's 1, the two seeding inserts.
	if st.TotalCalls != 15 || st.UnattributedCalls != 1 || st.UnreadCalls != 0 || st.TotalTimeMs <= 0 {
		t.Errorf("totals %+v", st)
	}
	// Scans follow the plans both versions choose: the join scans orders and
	// seeks customers 4 times; the updates seek customers, the bare name
	// seeks its index, the query runner seeks orders and the ambiguous name
	// seeks dbo.items. Writes count statements, not rows.
	for key, want := range map[string]workload.Table{
		"dbo.customers": {IdxScans: 4 + 3 + 1, QueryCalls: 4 + 1 + 3 + 1, RowsWritten: 1 + 3, LiveRows: 2},
		"sales.orders":  {SeqScans: 4, IdxScans: 1, QueryCalls: 4 + 1 + 1, RowsWritten: 1, LiveRows: 2},
		"dbo.audit":     {QueryCalls: 3, RowsWritten: 3, LiveRows: 3},
		"dbo.order":     {},
		"dbo.items":     {IdxScans: 1},
		"sales.items":   {},
	} {
		tb := snap.Tables[key]
		if tb == nil || tb.SeqScans != want.SeqScans || tb.IdxScans != want.IdxScans || tb.QueryCalls != want.QueryCalls ||
			tb.RowsWritten != want.RowsWritten || tb.LiveRows != want.LiveRows {
			t.Errorf("%s %+v, want %+v", key, tb, want)
			continue
		}
		for _, q := range tb.TopQueries {
			if strings.Contains(strings.ToLower(q.Text), "sys.") || strings.HasPrefix(strings.ToUpper(q.Text), "SET ") {
				t.Errorf("%s: dbscope's own statement %q", key, q.Text)
			}
		}
	}
	for _, key := range []string{"dbo.customers", "sales.orders"} {
		if !slices.ContainsFunc(snap.Tables[key].TopQueries, func(q workload.Query) bool {
			return q.Text == join && q.Calls == 4 && q.MeanMs > 0 && q.TotalMs >= q.MeanMs
		}) {
			t.Errorf("%s top queries %+v", key, snap.Tables[key].TopQueries)
		}
	}
}

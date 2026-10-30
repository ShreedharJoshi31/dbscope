package oracle

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

func TestWorkloadNames(t *testing.T) {
	m := &schema.Model{Relations: []schema.Relation{{Schema: "SHOP", Name: "ORDERS"}, {Schema: "SHOP", Name: "CUSTOMERS"},
		{Schema: "SHOP", Name: "ITEMS"}, {Schema: "SALES", Name: "ITEMS"}, {Schema: "SHOP", Name: "Mixed"}, {Schema: "SHOP", Name: "ORDER_LINES"}}}
	ix := newRelIndex(m)
	ix.synonyms[schema.QualifiedName{Schema: "APP", Name: "ORD"}] = "SHOP.ORDERS"
	ix.synonyms[schema.QualifiedName{Schema: "PUBLIC", Name: "CUST"}] = "SHOP.CUSTOMERS"
	for _, c := range []struct{ text, parsing, want string }{
		{"SELECT count(*) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.total > :1", "SHOP", "SHOP.ORDERS,SHOP.CUSTOMERS"},
		{"select * from Shop.Orders, sales.items i where i.id = :b1", "APP", "SHOP.ORDERS,SALES.ITEMS"},
		{"SELECT * FROM items", "SHOP", "SHOP.ITEMS"},
		{"SELECT * FROM items", "SALES", "SALES.ITEMS"},
		{"SELECT * FROM items", "APP", ""},
		{"SELECT * FROM ord", "APP", "SHOP.ORDERS"},
		{"SELECT * FROM ord", "SHOP", ""},
		{"SELECT * FROM cust", "APP", "SHOP.CUSTOMERS"},
		{`SELECT "x" FROM "Mixed" m, mixed`, "SHOP", "SHOP.Mixed"},
		{"WITH customers AS (SELECT id FROM orders) SELECT count(*) FROM customers", "SHOP", "SHOP.ORDERS"},
		{"WITH x (a) AS (SELECT id FROM items) SELECT * FROM x, customers", "SHOP", "SHOP.ITEMS,SHOP.CUSTOMERS"},
		{"INSERT INTO order_lines (id, orders) SELECT id, 1 FROM items", "SHOP", "SHOP.ORDER_LINES,SHOP.ITEMS"},
		{"UPDATE customers SET name = (SELECT max(id) FROM orders) WHERE id = :1", "SHOP", "SHOP.CUSTOMERS,SHOP.ORDERS"},
		{"DELETE customers WHERE id = 1", "SHOP", "SHOP.CUSTOMERS"},
		{"SELECT id FROM customers FOR UPDATE OF name", "SHOP", "SHOP.CUSTOMERS"},
		{"MERGE INTO customers c USING (SELECT id FROM orders) s ON (c.id = s.id) WHEN MATCHED THEN UPDATE SET c.name = 'x' DELETE WHERE c.id = 3", "SHOP", "SHOP.CUSTOMERS,SHOP.ORDERS"},
		{"SELECT extract(day FROM created), trim(' ' FROM items) FROM orders", "SHOP", "SHOP.ORDERS"},
		{"SELECT customers, orders.id FROM orders", "SHOP", "SHOP.ORDERS"},
		{"SELECT * FROM orders@remote, items", "SHOP", "SHOP.ITEMS"},
		{"SELECT * FROM orders o CROSS APPLY (SELECT * FROM items i WHERE i.id = o.id)", "SHOP", "SHOP.ORDERS,SHOP.ITEMS"},
		{"SELECT 'FROM customers' FROM dual JOIN orders USING (id)", "SHOP", "SHOP.ORDERS"},
		{"SELECT * FROM all_tables t JOIN orders o ON 1 = 1", "SHOP", "SHOP.ORDERS"},
	} {
		keys, own := ix.tablesIn(c.text, c.parsing)
		if strings.Join(keys, ",") != c.want || own {
			t.Errorf("%s (%s): %v %v, want %s", c.text, c.parsing, keys, own, c.want)
		}
	}
	for _, text := range []string{"SELECT version_full FROM product_component_version WHERE rownum = 1", "SELECT USER FROM dual",
		"SELECT owner, table_name FROM all_tables WHERE owner IN (:1)", "SELECT sql_id FROM v$sql", "SELECT 1 FROM sys.obj$",
		"SELECT id FROM plan_table WHERE statement_id = :1"} {
		if keys, own := ix.tablesIn(text, "SHOP"); !own || len(keys) != 0 {
			t.Errorf("%s: %v %v, want own", text, keys, own)
		}
	}
	if keys, own := ix.tablesIn("SELECT * FROM somewhere_else", "SHOP"); own || len(keys) != 0 {
		t.Errorf("unknown table: %v %v", keys, own)
	}
}

func TestSkippedLines(t *testing.T) {
	doc := `<other_xml><info type="adaptive_plan" note="y">yes</info><display_map><row op="1" dis="1" par="0" prt="0" dep="1" skp="0"/>` +
		`<row op="2" dis="1" par="1" prt="0" dep="1" skp="1"/><row op="9" dis="6" par="1" prt="0" dep="1" skp="1"/></display_map></other_xml>`
	ids, ok := skippedLines(doc)
	if !ok || len(ids) != 2 || !ids[2] || !ids[9] {
		t.Errorf("skipped %v %v", ids, ok)
	}
	if _, ok := skippedLines(`<other_xml><info type="plan_hash">1</info></other_xml>`); ok {
		t.Error("no display map read as one")
	}
}

const (
	shopPass = "ShopTest2026x"
	appPass  = "AppTest2026x"
	halfPass = "HalfTest2026x"
)

// TestWorkload runs a known workload on a fresh server and reads it as a
// user with SELECT_CATALOG_ROLE, one with grants on V$SQL alone, and one
// with neither.
func TestWorkload(t *testing.T) {
	testWorkload(t, mytest.Oracle(t))
}

func testWorkload(t *testing.T, p conn.Profile) {
	ctx := context.Background()
	db, err := Open(p, mytest.OraclePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE USER shop IDENTIFIED BY " + shopPass + " DEFAULT TABLESPACE sysaux QUOTA UNLIMITED ON sysaux",
		"GRANT CREATE SESSION TO shop",
		"CREATE USER sales IDENTIFIED BY " + shopPass + " DEFAULT TABLESPACE sysaux QUOTA UNLIMITED ON sysaux",
		"CREATE TABLE shop.customers (id NUMBER PRIMARY KEY, name VARCHAR2(50))",
		"CREATE TABLE shop.orders (id NUMBER PRIMARY KEY, customer_id NUMBER, total NUMBER)",
		"CREATE TABLE shop.audit_log (what VARCHAR2(50))",
		"CREATE TABLE shop.items (id NUMBER PRIMARY KEY)",
		"CREATE TABLE shop.events (id NUMBER, k NUMBER) PARTITION BY RANGE (k) (PARTITION p1 VALUES LESS THAN (10), PARTITION p2 VALUES LESS THAN (MAXVALUE))",
		"CREATE TABLE sales.items (id NUMBER PRIMARY KEY)",
		"CREATE VIEW shop.big_orders AS SELECT id, total FROM shop.orders WHERE total > 50",
		"INSERT INTO shop.customers SELECT level, 'c' || level FROM dual CONNECT BY level <= 20",
		"INSERT INTO shop.orders SELECT level, mod(level, 20) + 1, level FROM dual CONNECT BY level <= 100",
		"INSERT INTO shop.items VALUES (1)",
		"COMMIT",
		// Gathering statistics resets ALL_TAB_MODIFICATIONS: the seeding rows are not counted.
		"BEGIN DBMS_STATS.GATHER_SCHEMA_STATS('SHOP'); DBMS_STATS.GATHER_SCHEMA_STATS('SALES'); END;",
		"CREATE USER app IDENTIFIED BY " + appPass,
		"GRANT CREATE SESSION TO app",
		"GRANT READ ON shop.orders TO app",
		"GRANT READ ON shop.customers TO app",
		"GRANT READ ON shop.audit_log TO app",
		"GRANT READ ON shop.items TO app",
		"GRANT READ ON shop.events TO app",
		"GRANT READ ON shop.big_orders TO app",
		"GRANT READ ON sales.items TO app",
		"GRANT SELECT_CATALOG_ROLE TO app",
		"CREATE SYNONYM app.ord FOR shop.orders",
		"CREATE USER half IDENTIFIED BY " + halfPass,
		"GRANT CREATE SESSION TO half",
		"CREATE USER ro IDENTIFIED BY " + roPass,
		"GRANT CREATE SESSION TO ro",
		"GRANT READ ON shop.orders TO ro",
		"GRANT READ ON shop.audit_log TO ro",
		"GRANT READ ON shop.events TO ro",
	} {
		mustExec(t, db, q)
	}
	// SYSTEM may not grant on SYS's views (ORA-01031); SYS may.
	sys, err := sql.Open("oracle", go_ora.BuildUrl(p.Host, p.Port, p.Database, "sys", mytest.OraclePassword,
		map[string]string{"DBA PRIVILEGE": "SYSDBA"}))
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	mustExec(t, sys, "GRANT READ ON SYS.V_$SQL TO half")
	if _, err := db.ExecContext(ctx, "GRANT READ ON SYS.V_$SQL_PLAN TO half"); err == nil || !strings.Contains(err.Error(), "ORA-01031") {
		t.Errorf("SYSTEM granting on V_$SQL_PLAN: %v", err)
	}

	open := func(user, pass string) *sql.DB {
		t.Helper()
		q := p
		q.User = user
		x, err := Open(q, pass)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { x.Close() })
		return x
	}
	shop, app, half, ro := open("shop", shopPass), open("app", appPass), open("half", halfPass), open("ro", roPass)
	m, err := Introspect(ctx, db, []string{"SHOP", "SALES"})
	if err != nil {
		t.Fatal(err)
	}

	snap, err := Workload(ctx, ro, m)
	if err != nil || snap.Statements.Status != workload.StatusUnavailable ||
		!strings.HasPrefix(snap.Statements.Message, "This user may not read Oracle's shared pool (V$SQL). An admin runs: GRANT SELECT_CATALOG_ROLE TO RO;") {
		t.Fatalf("without the role %+v %v", snap, err)
	}
	if tb := snap.Tables["SHOP.ORDERS"]; tb == nil || tb.LiveRows != 100 || tb.QueryCalls != 0 {
		t.Errorf("without the role, orders %+v", tb)
	}

	// A freshly started server ages most of the shared pool out once while
	// it warms up (seen on 23.26 Free, at varying times in its first
	// minute), so each statement's cursor is kept as soon as it has run.
	pin := func() {
		t.Helper()
		mustExec(t, sys, `BEGIN FOR c IN (SELECT DISTINCT address, hash_value FROM v$sqlarea
			WHERE parsing_schema_name IN ('SHOP', 'APP') AND kept_versions = 0) LOOP
				sys.dbms_shared_pool.keep(rawtohex(c.address) || ',' || c.hash_value, 'C');
			END LOOP; END;`)
	}
	// The join is hinted so its plan is the same on every run: orders read
	// in full, customers through its primary key.
	const join = "SELECT /*+ LEADING(o) USE_NL(c) FULL(o) INDEX(c) */ count(*) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.total > :1"
	for range 4 {
		var n int
		if err := shop.QueryRowContext(ctx, join, 10).Scan(&n); err != nil || n != 90 {
			t.Fatal(n, err)
		}
		pin()
	}
	for i := range 3 {
		if _, err := shop.ExecContext(ctx, "INSERT INTO audit_log VALUES (:1)", "x"); err != nil {
			t.Fatal(err)
		}
		if _, err := shop.ExecContext(ctx, "UPDATE customers SET name = :1 WHERE id = :2", "z", i+1); err != nil {
			t.Fatal(err)
		}
		pin()
	}
	// One row in each partition.
	for _, k := range []int{1, 20} {
		if _, err := shop.ExecContext(ctx, "INSERT INTO events VALUES (:1, :2)", k, k); err != nil {
			t.Fatal(err)
		}
		pin()
	}
	var n int
	// A bare name resolves in the parsing schema: SHOP.ITEMS, not SALES.ITEMS.
	if err := shop.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE id > :1", 0).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	pin()
	// A CTE named like a table is not the table.
	if err := shop.QueryRowContext(ctx, "WITH customers AS (SELECT id FROM orders WHERE id < 3) SELECT count(*) FROM customers").Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	pin()
	if _, err := shop.ExecContext(ctx, "BEGIN NULL; END;"); err != nil {
		t.Fatal(err)
	}
	pin()
	if err := shop.QueryRowContext(ctx, "SELECT count(*) FROM big_orders").Scan(&n); err != nil || n != 50 {
		t.Fatal(n, err)
	}
	pin()
	if err := app.QueryRowContext(ctx, "SELECT count(*) FROM ord").Scan(&n); err != nil || n != 100 {
		t.Fatal(n, err)
	}
	pin()
	// Literals differ, the force matching signature does not: one query.
	for _, id := range []string{"1", "2"} {
		if err := app.QueryRowContext(ctx, "SELECT total FROM shop.orders WHERE id = "+id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		pin()
	}
	// dbscope's own query runner: its EXPLAIN PLAN, PLAN_TABLE and dictionary reads are left out.
	if _, err := Run(ctx, app, "SELECT total FROM shop.orders WHERE id = 3", 10, false); err != nil {
		t.Fatal(err)
	}
	pin()
	if m, err = Introspect(ctx, app, []string{"SHOP", "SALES"}); err != nil {
		t.Fatal(err)
	}

	// The second read sees the first one's statements in the shared pool.
	for range 2 {
		if snap, err = Workload(ctx, app, m); err != nil {
			t.Fatal(err)
		}
	}
	st := snap.Statements
	if st.Status != workload.StatusOK || st.Since == nil || time.Since(*st.Since) > 30*time.Minute || time.Since(*st.Since) < 0 ||
		strings.Contains(st.Message, "could not") || strings.Contains(st.Message, "may not") || !strings.Contains(st.Message, "come from DBA_TAB_MODIFICATIONS") {
		t.Fatalf("statements %+v", st)
	}
	// Join 4, inserts 3 + 2, updates 3, items 1, CTE 1, PL/SQL 1, view 1, synonym 1, literals 2 + the query runner's 1.
	if st.TotalCalls != 20 || st.UnattributedCalls != 1 || st.UnreadCalls != 0 || st.TotalTimeMs <= 0 {
		t.Errorf("totals %+v", st)
	}
	// Scans follow the plans: the join reads orders in full and customers
	// through its key, as do the updates; the view's plan reads orders in
	// full (its text names only the view); the CTE, the synonym and the
	// lookups by id read orders' primary key. Rows written count rows.
	for key, want := range map[string]workload.Table{
		"SHOP.CUSTOMERS":  {IdxScans: 4 + 3, QueryCalls: 4 + 3, RowsWritten: 3, LiveRows: 20},
		"SHOP.ORDERS":     {SeqScans: 4 + 1, IdxScans: 1 + 1 + 3, QueryCalls: 4 + 1 + 1 + 3, LiveRows: 100},
		"SHOP.AUDIT_LOG":  {QueryCalls: 3, RowsWritten: 3},
		"SHOP.ITEMS":      {IdxScans: 1, QueryCalls: 1, LiveRows: 1},
		"SHOP.EVENTS":     {QueryCalls: 2, RowsWritten: 2},
		"SHOP.BIG_ORDERS": {QueryCalls: 1},
		"SALES.ITEMS":     {},
	} {
		tb := snap.Tables[key]
		if tb == nil || tb.SeqScans != want.SeqScans || tb.IdxScans != want.IdxScans || tb.QueryCalls != want.QueryCalls ||
			tb.RowsWritten != want.RowsWritten || tb.LiveRows != want.LiveRows {
			t.Errorf("%s %+v, want %+v", key, tb, want)
			continue
		}
		for _, q := range tb.TopQueries {
			if l := strings.ToLower(q.Text); strings.Contains(l, "all_") || strings.Contains(l, "v$") || strings.Contains(l, "plan_table") {
				t.Errorf("%s: dbscope's own statement %q", key, q.Text)
			}
		}
	}
	for _, key := range []string{"SHOP.CUSTOMERS", "SHOP.ORDERS"} {
		if !slices.ContainsFunc(snap.Tables[key].TopQueries, func(q workload.Query) bool {
			return q.Text == join && q.Calls == 4 && q.MeanMs > 0 && q.TotalMs >= q.MeanMs
		}) {
			t.Errorf("%s top queries %+v", key, snap.Tables[key].TopQueries)
		}
	}
	if !slices.ContainsFunc(snap.Tables["SHOP.ORDERS"].TopQueries, func(q workload.Query) bool {
		return strings.HasPrefix(q.Text, "SELECT total FROM shop.orders WHERE id = ") && q.Calls == 3
	}) {
		t.Errorf("literals not grouped %+v", snap.Tables["SHOP.ORDERS"].TopQueries)
	}

	// V$SQL alone: statements, no scans, and the grant SYS runs.
	if snap, err = Workload(ctx, half, m); err != nil {
		t.Fatal(err)
	}
	st = snap.Statements
	if st.Status != workload.StatusOK || !strings.Contains(st.Message, "as SYS, an admin runs GRANT READ ON SYS.V_$SQL_PLAN TO HALF") {
		t.Errorf("half %+v", st)
	}
	// HALF may not read SHOP.ORDERS, so ALL_SYNONYMS does not show it APP.ORD.
	if tb := snap.Tables["SHOP.ORDERS"]; tb == nil || tb.QueryCalls != 4+1+3 || tb.SeqScans != 0 || tb.IdxScans != 0 {
		t.Errorf("half orders %+v", tb)
	}

	// Without the role: rows written still come from ALL_TAB_MODIFICATIONS,
	// which does not show RO the partitions of SHOP.EVENTS.
	if snap, err = Workload(ctx, ro, m); err != nil {
		t.Fatal(err)
	}
	if st := snap.Statements; st.Status != workload.StatusUnavailable || !strings.Contains(st.Message, "come from ALL_TAB_MODIFICATIONS") {
		t.Errorf("ro %+v", st)
	}
	if a, e := snap.Tables["SHOP.AUDIT_LOG"], snap.Tables["SHOP.EVENTS"]; a == nil || a.RowsWritten != 3 || e == nil || e.RowsWritten != 0 {
		t.Errorf("ro audit_log %+v, events %+v", a, e)
	}
}

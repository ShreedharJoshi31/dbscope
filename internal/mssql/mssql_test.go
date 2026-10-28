package mssql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	mssqldrv "github.com/microsoft/go-mssqldb"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestCheck(t *testing.T) {
	for text, want := range map[string]guard.Reason{
		"SELECT * FROM dbo.t":                                  "",
		"with x as (select 1 a) select a from x;":              "",
		"SELECT [delete], 'update' FROM t /* drop */":          "",
		"SELECT 1 /* a /* nested */ DELETE */":                 "",
		"SELECT name FROM t FOR JSON PATH":                     "",
		"SELECT 1; SELECT 2":                                   guard.MultipleStatements,
		"SELECT 1 DELETE FROM t":                               guard.NotSelect,
		"EXEC sp_who":                                          guard.NotSelect,
		"UPDATE t SET a = 1":                                   guard.NotSelect,
		"SELECT * INTO t2 FROM t":                              guard.SelectInto,
		"SELECT * FROM OPENROWSET('SQLNCLI', 'x', 'SELECT 1')": guard.FunctionNotAllowed,
		"SELECT NEXT VALUE FOR dbo.seq":                        guard.WritesData,
		"SELECT * FROM t WITH (UPDLOCK)":                       guard.LockingClause,
		"WAITFOR DELAY '00:01'":                                guard.NotSelect,
		"SELECT 1 WAITFOR DELAY '00:01'":                       guard.FunctionNotAllowed,
		"  -- nothing":                                         guard.Syntax,
	} {
		err := Check(text)
		var ge *guard.Error
		switch {
		case want == "" && err != nil:
			t.Errorf("Check(%q) = %v", text, err)
		case want != "" && (!errors.As(err, &ge) || ge.Reason != want):
			t.Errorf("Check(%q) = %v, want %s", text, err, want)
		}
	}
}

func TestTypeTextAndDefaults(t *testing.T) {
	for _, c := range []struct {
		typ             string
		max, prec, scal int
		want            string
	}{
		{"nvarchar", 100, 0, 0, "nvarchar(50)"}, {"varchar", -1, 0, 0, "varchar(max)"}, {"decimal", 9, 10, 2, "decimal(10,2)"},
		{"datetime2", 8, 27, 7, "datetime2"}, {"datetime2", 6, 19, 0, "datetime2(0)"}, {"int", 4, 10, 0, "int"},
	} {
		if got := typeText(c.typ, c.max, c.prec, c.scal); got != c.want {
			t.Errorf("typeText(%s) = %s, want %s", c.typ, got, c.want)
		}
	}
	for in, want := range map[string]string{"((0))": "0", "(getdate())": "getdate()", "(N'a)b')": "N'a)b'", "((1)+(2))": "(1)+(2)", "": ""} {
		if got := unwrapDefault(in); got != want {
			t.Errorf("unwrapDefault(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMSSQL(t *testing.T) {
	for _, name := range []string{"2025", "2022"} {
		t.Run(name, func(t *testing.T) { testServer(t, name) })
	}
}

const roPass = "Ro-Test-Pass-2026!"

func testServer(t *testing.T, name string) {
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
		`CREATE TABLE dbo.customers (id int IDENTITY PRIMARY KEY, name nvarchar(100) NOT NULL, email varchar(200) UNIQUE,
			created datetime2 DEFAULT sysutcdatetime(), name_len AS len(name) PERSISTED)`,
		"EXEC sys.sp_addextendedproperty 'MS_Description', N'People who order', 'SCHEMA', 'dbo', 'TABLE', 'customers'",
		`CREATE TABLE sales.orders (id uniqueidentifier DEFAULT NEWID() PRIMARY KEY CLUSTERED,
			customer_id int NOT NULL REFERENCES dbo.customers(id) ON DELETE CASCADE, total decimal(10,2), note nvarchar(max),
			status varchar(20) CONSTRAINT ck_status CHECK (status IN ('new', 'paid')), big bigint)`,
		"CREATE TABLE dbo.audit (at datetime2, what nvarchar(200))",
		"CREATE TABLE dbo.items (id int PRIMARY KEY, order_id uniqueidentifier)",
		"ALTER TABLE dbo.items WITH NOCHECK ADD CONSTRAINT fk_items_orders FOREIGN KEY (order_id) REFERENCES sales.orders(id)",
		"CREATE INDEX ix_customers_name ON dbo.customers(name) INCLUDE (email)",
		"ALTER INDEX ix_customers_name ON dbo.customers DISABLE",
		"CREATE VIEW dbo.paid AS SELECT id, total FROM sales.orders WHERE status = 'paid'",
		"INSERT dbo.customers (name, email) VALUES (N'Ada', 'ada@example.com'), (N'Bo', NULL), (N'Cy', 'cy@example.com')",
		`INSERT sales.orders (id, customer_id, total, status, big) VALUES
			('6F9619FF-8B86-D011-B42D-00C04FC964FF', 1, 9.50, 'paid', 9007199254740993), (NEWID(), 2, 20, 'new', 1)`,
		"INSERT dbo.audit VALUES ('2026-01-02T03:04:05.1234567', N'boot')",
		"CREATE LOGIN ro WITH PASSWORD = '" + roPass + "', CHECK_POLICY = OFF",
		"CREATE USER ro FOR LOGIN ro",
		"ALTER ROLE db_datareader ADD MEMBER ro",
		"GRANT SHOWPLAN TO ro",
	} {
		mustExec(t, db, q)
	}

	if res := Test(ctx, p, mytest.MSSQLPassword); !res.OK || !strings.Contains(strings.Join(res.WritePrivileges, ","), "sysadmin") {
		t.Errorf("admin test %+v %+v", res, res.Error)
	}
	ro := p
	ro.User = "ro"
	res := Test(ctx, ro, roPass)
	if !res.OK || res.Engine != conn.EngineSQLServer || len(res.WritePrivileges) != 0 || res.ServerVersionNum < 160000 ||
		len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "VIEW DEFINITION") {
		t.Errorf("read-only test %+v %+v", res, res.Error)
	}
	rodb, err := Open(ro, roPass)
	if err != nil {
		t.Fatal(err)
	}
	defer rodb.Close()
	m, err := Introspect(ctx, rodb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Relation(schema.QualifiedName{Schema: "dbo", Name: "customers"}).Column("name_len"); c.GeneratedKind != "stored" || c.Generated != "" {
		t.Errorf("computed column without VIEW DEFINITION %+v", c)
	}
	mustExec(t, db, "GRANT VIEW DEFINITION TO ro")
	if res := Test(ctx, ro, roPass); !res.OK || len(res.Warnings) != 0 {
		t.Errorf("read-only test with VIEW DEFINITION %+v", res.Warnings)
	}
	if bad := Test(ctx, ro, "wrong-pass"); bad.OK || bad.Error.Kind != "auth" {
		t.Errorf("wrong password %+v %+v", bad, bad.Error)
	}

	t.Run("introspect", func(t *testing.T) {
		m, err = Introspect(ctx, rodb, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(m.Schemas, ",") != "dbo,sales" {
			t.Errorf("schemas %v", m.Schemas)
		}
		cust := m.Relation(schema.QualifiedName{Schema: "dbo", Name: "customers"})
		orders := m.Relation(schema.QualifiedName{Schema: "sales", Name: "orders"})
		if cust == nil || orders == nil || m.Relation(schema.QualifiedName{Schema: "dbo", Name: "paid"}).Kind != schema.KindView {
			t.Fatalf("relations %+v", m.Relations)
		}
		if cust.Comment != "People who order" || *cust.RowEstimate != 3 || cust.Column("name").Type != "nvarchar(100)" ||
			cust.Column("id").Identity == "" || cust.Column("created").Default != "sysutcdatetime()" ||
			cust.Column("name_len").GeneratedKind != "stored" || strings.Join(cust.PrimaryKey, ",") != "id" || len(cust.Uniques) != 1 {
			t.Errorf("customers %+v", cust)
		}
		if !hasFinding(cust.Findings, "DBS-MSSQL-DISABLED-INDEX", "") {
			t.Errorf("customers findings %+v", cust.Findings)
		}
		if orders.Column("total").Type != "decimal(10,2)" || orders.Column("note").Type != "nvarchar(max)" ||
			len(orders.ForeignKeys) != 1 || orders.ForeignKeys[0].Ref.Name != "customers" || orders.ForeignKeys[0].OnDelete != "cascade" ||
			len(orders.Checks) != 1 || !hasFinding(orders.Findings, "DBS-MSSQL-GUID-CLUSTER", "id") {
			t.Errorf("orders %+v", orders)
		}
		items := m.Relation(schema.QualifiedName{Schema: "dbo", Name: "items"})
		if items.ForeignKeys[0].Validated || !hasFinding(items.Findings, "DBS-MSSQL-UNTRUSTED-FK", "order_id") {
			t.Errorf("items %+v", items)
		}
		if a := m.Relation(schema.QualifiedName{Schema: "dbo", Name: "audit"}); !hasFinding(a.Findings, "DBS-MSSQL-HEAP", "") {
			t.Errorf("audit %+v", a.Findings)
		}
	})

	t.Run("run", func(t *testing.T) {
		r, err := Run(ctx, rodb, `SELECT o.id, c.name, o.total, o.big, c.created, a.at FROM sales.orders o
			JOIN dbo.customers c ON c.id = o.customer_id CROSS JOIN dbo.audit a WHERE o.status = 'paid'`, 10, false, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		row := r.Rows[0]
		if r.RowCount != 1 || string(row[0]) != `"6F9619FF-8B86-D011-B42D-00C04FC964FF"` || string(row[1]) != `"Ada"` ||
			string(row[2]) != `"9.50"` || string(row[3]) != `"9007199254740993"` || string(row[5]) != `"2026-01-02T03:04:05.1234567"` {
			t.Errorf("rows %+v %s", r.Columns, r.Rows)
		}
		if r.Plan == nil || r.Plan.NodeType != "SELECT" || len(r.Plan.Children) == 0 || !hasFlag(r.Plan, "full_scan") {
			t.Errorf("plan %+v", r.Plan)
		}
		r, err = Run(ctx, rodb, "SELECT name FROM dbo.customers ORDER BY id", 2, false, 10*time.Second)
		if err != nil || r.RowCount != 2 || !r.Truncated {
			t.Errorf("cap %+v %v", r, err)
		}
		r, err = Run(ctx, rodb, "SELECT 1 AS one FOR JSON PATH", 2, false, 10*time.Second)
		if err != nil || r.RowCount != 1 {
			t.Errorf("json %+v %v", r, err)
		}
		r, err = Run(ctx, rodb, "SELECT * FROM dbo.customers WHERE email = 'ada@example.com'", 2, true, 10*time.Second)
		if err != nil || r.RowCount != 0 || r.Plan == nil || len(r.Plan.Children) == 0 {
			t.Errorf("explain %+v %v", r, err)
		}
	})

	t.Run("gate", func(t *testing.T) {
		// Check would refuse these; the gate must too, with sa's rights.
		var ge *guard.Error
		for text, want := range map[string]guard.Reason{
			"DELETE FROM dbo.audit":                             guard.NotSelect,
			"SELECT 1 DELETE FROM dbo.audit":                    guard.MultipleStatements,
			"SELECT * INTO dbo.copy FROM dbo.audit":             guard.NotSelect,
			"WITH x AS (SELECT * FROM dbo.audit) DELETE FROM x": guard.NotSelect,
		} {
			if _, err := Run(ctx, db, text, 10, false, 10*time.Second); !errors.As(err, &ge) || ge.Reason != want {
				t.Errorf("%s: %v, want %s", text, err, want)
			}
		}
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM dbo.audit").Scan(&n); err != nil || n != 1 {
			t.Errorf("audit rows %d %v", n, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sys.tables WHERE name = 'copy'").Scan(&n); err != nil || n != 0 {
			t.Errorf("SELECT INTO ran: %d %v", n, err)
		}
	})

	t.Run("session reset", func(t *testing.T) {
		one, err := Open(ro, roPass)
		if err != nil {
			t.Fatal(err)
		}
		defer one.Close()
		one.SetMaxOpenConns(1)
		if _, err := Run(ctx, one, "SELECT name FROM dbo.customers", 1, false, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := one.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT name FROM dbo.customers) x").Scan(&n); err != nil || n != 3 {
			t.Errorf("after a run the pooled session returned %d, %v", n, err)
		}
		rows, err := one.QueryContext(ctx, "SELECT name FROM dbo.customers")
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for rows.Next() {
			got++
		}
		rows.Close()
		if got != 3 {
			t.Errorf("ROWCOUNT or SHOWPLAN stayed on: %d rows", got)
		}
	})

	t.Run("lock timeout", func(t *testing.T) {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "UPDATE dbo.audit SET what = N'locked'"); err != nil {
			t.Fatal(err)
		}
		_, err = Run(ctx, rodb, "SELECT what FROM dbo.audit", 10, false, time.Second)
		var me mssqldrv.Error
		if !errors.As(err, &me) || me.Number != 1222 {
			t.Errorf("blocked read: %v", err)
		}
	})
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func hasFinding(fs []schema.Finding, rule, column string) bool {
	for _, f := range fs {
		if f.RuleID == rule && f.Column == column {
			return true
		}
	}
	return false
}

func hasFlag(n *plan.Node, code string) bool {
	for _, f := range n.Flags {
		if f.Code == code {
			return true
		}
	}
	for _, c := range n.Children {
		if hasFlag(c, code) {
			return true
		}
	}
	return false
}

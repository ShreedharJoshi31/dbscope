package cassandra

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/exec"
)

func TestCheck(t *testing.T) {
	for stmt, ok := range map[string]bool{
		"SELECT * FROM orders WHERE customer = 'c1';":        true,
		"select json * from app.orders -- note; not a split": true,
		"SELECT * FROM t WHERE a = 'x;y'":                    true,
		"INSERT INTO orders (a) VALUES (1)":                  false,
		"SELECT * FROM t; DROP TABLE t":                      false,
		"/* x */ UPDATE t SET a = 1 WHERE b = 2":             false,
		"SELECT salted_hash FROM system.roles":               false,
		"SELECT * FROM system_auth.roles":                    false,
		`SELECT * FROM "system"."config"`:                    false,
		"TRUNCATE orders":                                    false,
		"":                                                   false,
	} {
		if err := Check(stmt); (err == nil) != ok {
			t.Errorf("Check(%q) = %v", stmt, err)
		}
	}
}

func TestRestrictions(t *testing.T) {
	rs, af := restrictions(tokens(`SELECT * FROM t WHERE token(a, b) > 5 AND (c, d) >= (1, 2) AND e IN (1, 2) AND f CONTAINS 'x' LIMIT 3 ALLOW FILTERING`))
	if !af || len(rs) != 4 || !rs[0].token || strings.Join(rs[0].cols, ",") != "a,b" || strings.Join(rs[1].cols, ",") != "c,d" || rs[1].op != ">=" || rs[2].op != "in" || rs[3].op != "contains" {
		t.Errorf("%+v %v", rs, af)
	}
	if ks, table := target(tokens(`SELECT * FROM "App"."Orders" WHERE x = 1`)); ks != "App" || table != "Orders" {
		t.Errorf("target %q %q", ks, table)
	}
}

func TestCassandra(t *testing.T) {
	for _, flavor := range []string{"cassandra", "scylla"} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			testServer(t, flavor)
		})
	}
}

func testServer(t *testing.T, flavor string) {
	admin := mytest.Cassandra(t, flavor)
	ctx := context.Background()
	setup := admin
	setup.Database = ""
	s, err := Connect(setup, mytest.CassandraPassword)
	if err != nil {
		t.Fatal(err)
	}
	ks := "CREATE KEYSPACE app WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}"
	if flavor == "scylla" {
		ks += " AND tablets = {'enabled': false}" // tablet keyspaces refuse secondary indexes
	}
	stmts := []string{ks,
		`CREATE TABLE app.orders (customer text, placed timestamp, id uuid, status text, total decimal, note text static, tags set<text>,
			PRIMARY KEY ((customer), placed, id)) WITH CLUSTERING ORDER BY (placed DESC, id ASC) AND default_time_to_live = 86400`,
		"CREATE INDEX by_status ON app.orders (status)",
		"CREATE TABLE app.audit (id uuid PRIMARY KEY, what text) WITH gc_grace_seconds = 0",
		"INSERT INTO app.orders (customer, placed, id, status, total, note, tags) VALUES ('c1', '2026-10-01', 5b6962dd-3f90-4c93-8f61-eabfa4a803e2, 'paid', 12.50, 'vip', {'a'})",
		"INSERT INTO app.orders (customer, placed, id, status, total) VALUES ('c1', '2026-10-02', 6b6962dd-3f90-4c93-8f61-eabfa4a803e2, 'new', 99999999999999999999.01)",
		"INSERT INTO app.orders (customer, placed, id, status) VALUES ('c2', '2026-10-03', 7b6962dd-3f90-4c93-8f61-eabfa4a803e2, 'paid')",
		"CREATE ROLE ro WITH LOGIN = true AND PASSWORD = 'ro-pass'",
		"GRANT SELECT ON KEYSPACE app TO ro",
	}
	if flavor == "cassandra" {
		stmts = append(stmts, "CREATE INDEX by_total ON app.orders (total) USING 'sai'")
	}
	for _, q := range stmts {
		if err := s.Query(q).ExecContext(ctx); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()

	if res := Test(ctx, admin, mytest.CassandraPassword); !res.OK || len(res.WritePrivileges) != 1 || res.WritePrivileges[0] != "SUPERUSER" {
		t.Errorf("superuser test %+v %+v", res, res.Error)
	}
	ro := admin
	ro.User = "ro"
	res := Test(ctx, ro, "ro-pass")
	wantEngine := conn.EngineCassandra
	if flavor == "scylla" {
		wantEngine = conn.EngineScylla
	}
	if !res.OK || res.Engine != wantEngine || len(res.WritePrivileges) != 0 || len(res.Warnings) != 0 {
		t.Errorf("read-only test %+v %+v", res, res.Error)
	}
	if bad := Test(ctx, ro, "wrong"); bad.OK || bad.Error.Kind != "auth" {
		t.Errorf("wrong password %+v %+v", bad, bad.Error)
	}

	s, err = Connect(ro, "ro-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Query("INSERT INTO orders (customer, placed, id) VALUES ('x', '2026-10-01', uuid())").ExecContext(ctx); err == nil {
		t.Error("the SELECT-only role could write")
	}

	t.Run("introspect", func(t *testing.T) {
		m, err := Introspect(ctx, s, "app")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Relations) != 2 || m.Relations[0].Name != "audit" { // ScyllaDB's index view is folded into the index
			t.Fatalf("relations %+v", m.Relations)
		}
		if f := m.Relations[0].Findings; len(f) != 1 || f[0].RuleID != "DBS-CQL-GC-GRACE-ZERO" {
			t.Errorf("audit findings %+v", f)
		}
		o := m.Relations[1]
		if strings.Join(o.PrimaryKey, ",") != "customer,placed,id" || o.Columns[0].Comment != "partition key" || o.Columns[1].Comment != "clustering key, desc" ||
			o.Columns[3].Name != "note" || !strings.HasPrefix(o.Columns[3].Comment, "static") || !strings.Contains(o.Comment, "default TTL 1 day") || !strings.Contains(o.Comment, "gc_grace 10 days") {
			t.Errorf("orders %+v", o)
		}
		kinds := map[string]string{}
		for _, ix := range o.Indexes {
			kinds[ix.Name] = ix.Method
		}
		if flavor == "scylla" && kinds["by_status"] != "global index" || flavor == "cassandra" && (kinds["by_status"] != "secondary index" || kinds["by_total"] != "sai") {
			t.Errorf("indexes %+v", o.Indexes)
		}
	})

	t.Run("run", func(t *testing.T) {
		run := func(stmt string, explain bool) (string, []string, *exec.Result) {
			t.Helper()
			r, err := Run(ctx, s, "app", stmt, 10, explain, 10*time.Second)
			if err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
			var codes []string
			for _, f := range r.Plan.Children[0].Flags {
				codes = append(codes, f.Code)
			}
			return r.Plan.Children[0].NodeType, codes, r
		}
		node, codes, r := run("SELECT customer, total, note, tags FROM orders WHERE customer = 'c1'", false)
		if node != "Single-partition read (customer)" || len(codes) != 0 || r.RowCount != 2 ||
			string(r.Rows[0][1]) != `"99999999999999999999.01"` || string(r.Rows[0][3]) != "null" || string(r.Rows[1][3]) != `"[\"a\"]"` {
			t.Errorf("key read %s %v %s", node, codes, r.Rows)
		}
		if r.Columns[1].Type != "decimal" || r.Columns[3].Type != "set<text>" {
			t.Errorf("column types %+v", r.Columns)
		}
		if node, codes, _ = run("SELECT * FROM orders", true); node != "Full scan" || codes[0] != "full_scan" {
			t.Errorf("scan %s %v", node, codes)
		}
		node, codes, _ = run("SELECT * FROM orders WHERE note = 'vip'", true)
		if node != "Full scan with filtering" || strings.Join(codes, ",") != "allow_filtering,server_refuses" {
			t.Errorf("filtering %s %v", node, codes)
		}
		if _, err := Run(ctx, s, "app", "SELECT * FROM orders WHERE note = 'vip'", 10, false, 10*time.Second); err == nil || !strings.Contains(err.Error(), "ALLOW FILTERING") {
			t.Errorf("filtering ran: %v", err)
		}
		want := "Index read (secondary index by_status)"
		if flavor == "scylla" {
			want = "Index read (global index by_status)"
		}
		if node, _, r = run("SELECT * FROM orders WHERE status = 'paid'", false); node != want || r.RowCount != 2 {
			t.Errorf("index read %s %d", node, r.RowCount)
		}
	})
}

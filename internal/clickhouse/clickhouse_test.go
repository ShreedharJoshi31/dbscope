package clickhouse

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/plan"
)

func TestCheck(t *testing.T) {
	for stmt, ok := range map[string]bool{
		"SELECT * FROM events WHERE kind = 'insert';":       true,
		"WITH x AS (SELECT 1) SELECT * FROM x":              true,
		"SHOW TABLES":                                       true,
		"DESCRIBE events":                                   true,
		"SELECT * FROM numbers(10)":                         true,
		"SELECT url FROM t -- url( in a comment":            true,
		"INSERT INTO t VALUES (1)":                          false,
		"ALTER TABLE t DELETE WHERE 1":                      false,
		"SET readonly = 0":                                  false,
		"RESTORE TABLE t FROM Disk('backups', 'x')":         false,
		"BACKUP TABLE t TO Disk('backups', 'x')":            false,
		"EXPLAIN ANALYZE SELECT 1":                          false,
		"SELECT * FROM url('http://169.254.169.254/', CSV)": false,
		"SELECT * FROM s3('https://b/x.csv')":               false,
		"SELECT 1 INTO OUTFILE 'x.csv'":                     false,
		"SELECT 1; SELECT 2":                                false,
		"":                                                  false,
	} {
		if err := Check(stmt); (err == nil) != ok {
			t.Errorf("Check(%q) = %v", stmt, err)
		}
	}
	if got := splitKey("kind, user_id, toDate(at, 'UTC')"); strings.Join(got, "|") != "kind|user_id|toDate(at, 'UTC')" {
		t.Errorf("splitKey %q", got)
	}
}

func TestClickHouse(t *testing.T) {
	for _, name := range []string{"26.8", "25.8"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testServer(t, name)
		})
	}
}

func testServer(t *testing.T, name string) {
	p := mytest.ClickHouse(t, name)
	ctx := context.Background()
	admin, err := Connect(p, mytest.ClickHousePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, q := range []string{
		"CREATE DATABASE shop",
		"CREATE TABLE shop.events (day Date, user_id UInt64, kind LowCardinality(String), amount Nullable(Decimal(12,2)), big UInt64, tags Array(String)) ENGINE = MergeTree PARTITION BY toYYYYMM(day) ORDER BY (kind, user_id)",
		"CREATE TABLE shop.raw (line String) ENGINE = MergeTree ORDER BY tuple()",
		"CREATE TABLE shop.daily (day Date, kind String, n UInt64) ENGINE = SummingMergeTree ORDER BY (day, kind)",
		"CREATE MATERIALIZED VIEW shop.daily_mv TO shop.daily AS SELECT day, kind, count() AS n FROM shop.events GROUP BY day, kind",
		"INSERT INTO shop.events SELECT toDate('2026-09-01') + number % 60, number % 5000, ['view','click','buy'][number % 3 + 1], if(number % 7 = 0, NULL, number / 4), 18446744073709551615, ['a'] FROM numbers(2000000)",
		"INSERT INTO shop.raw SELECT toString(number) FROM numbers(100)",
		"CREATE USER ro0 IDENTIFIED BY 'ro-test-pass'", "GRANT SELECT ON shop.* TO ro0", "GRANT SELECT ON system.parts TO ro0", "GRANT SELECT ON system.data_skipping_indices TO ro0",
		"CREATE USER ro1 IDENTIFIED BY 'ro-test-pass' SETTINGS readonly = 1", "GRANT SELECT ON shop.* TO ro1",
		"CREATE USER ro2 IDENTIFIED BY 'ro-test-pass' SETTINGS readonly = 2", "GRANT SELECT ON shop.* TO ro2",
		"CREATE USER rw IDENTIFIED BY 'ro-test-pass'", "GRANT SELECT, INSERT ON shop.* TO rw",
	} {
		if err := admin.Conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p.Database = "shop"
	if res := Test(ctx, p, mytest.ClickHousePassword); !res.OK || len(res.WritePrivileges) == 0 {
		t.Errorf("admin test %+v %+v", res, res.Error)
	}
	rw := p
	rw.User = "rw"
	if res := Test(ctx, rw, "ro-test-pass"); !res.OK || len(res.WritePrivileges) != 1 || !strings.Contains(res.WritePrivileges[0], "INSERT") {
		t.Errorf("insert grant %+v", res.WritePrivileges)
	}
	if bad := Test(ctx, rw, "wrong"); bad.OK || bad.Error.Kind != "auth" {
		t.Errorf("wrong password %+v %+v", bad, bad.Error)
	}

	for _, user := range []string{"ro0", "ro1", "ro2", "rw"} {
		t.Run(user, func(t *testing.T) {
			up := p
			up.User = user
			res := Test(ctx, up, "ro-test-pass")
			if !res.OK || *res.DefaultReadOnly != (user == "ro1") {
				t.Fatalf("test %+v %+v", res, res.Error)
			}
			c, err := Connect(up, "ro-test-pass")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			m, err := Introspect(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			byName := map[string]int{}
			for i, r := range m.Relations {
				byName[r.Name] = i
			}
			events := m.Relations[byName["events"]]
			if strings.Join(events.PrimaryKey, ",") != "kind,user_id" || events.PartitionKey != "toYYYYMM(day)" || *events.RowEstimate != 2000000 ||
				events.Column("kind").Comment != "sorting key" || !events.Column("amount").Nullable {
				t.Errorf("events %+v", events)
			}
			if user == "ro0" && !strings.Contains(events.Comment, "2 partitions") {
				t.Errorf("parts with grant: %s", events.Comment)
			}
			if user == "ro1" && !strings.Contains(events.Comment, "need SELECT on system.parts") {
				t.Errorf("parts without grant: %s", events.Comment)
			}
			raw := m.Relations[byName["raw"]]
			if len(raw.Findings) != 1 || raw.Findings[0].RuleID != "DBS-CH-NO-PRIMARY-INDEX" || m.Relations[byName["daily_mv"]].Kind != "materialized_view" ||
				len(events.Findings) != 0 {
				t.Errorf("raw / mv %+v", m.Relations)
			}

			run := func(stmt string, explain bool, maxRows int) *exec.Result {
				t.Helper()
				r, err := Run(ctx, c, stmt, maxRows, explain, 10*time.Second)
				if err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
				return r
			}
			r := run("SELECT kind, user_id, amount, big, tags FROM events WHERE kind = 'buy' AND user_id = 2 ORDER BY day LIMIT 50", false, 3)
			if r.RowCount != 3 || !r.Truncated || string(r.Rows[0][3]) != `"18446744073709551615"` || string(r.Rows[0][4]) != `"[\"a\"]"` {
				t.Errorf("rows %+v %s", r.Columns, r.Rows)
			}
			if r = run("SELECT count() FROM events WHERE amount > 10", true, 10); !hasFlag(r.Plan, "full_scan") {
				t.Errorf("non-key filter not flagged: %+v", r.Plan)
			}
			// kind has three values, so generic exclusion search prunes on the second key column.
			if r = run("SELECT count() FROM events WHERE user_id = 7", true, 10); hasFlag(r.Plan, "full_scan") {
				t.Errorf("second key column flagged")
			}
			if r = run("SELECT count() FROM events WHERE kind = 'buy' AND user_id = 7", true, 10); hasFlag(r.Plan, "full_scan") {
				t.Errorf("key prefix flagged")
			}
			if r = run("SELECT * FROM daily FINAL", true, 10); !hasFlag(r.Plan, "final") {
				t.Errorf("final not flagged")
			}
			// The server refuses writes and setting changes on dbscope's connection, whatever Check says.
			if user != "rw" {
				return
			}
			if _, err := Run(ctx, c, "SELECT 1 SETTINGS readonly = 0", 10, false, 10*time.Second); err == nil {
				t.Error("a query undid readonly")
			}
			qctx, err := c.readCtx(ctx, 10*time.Second, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Conn.Exec(qctx, "INSERT INTO raw VALUES ('x')"); err == nil {
				t.Error("an INSERT ran under dbscope's settings for a user allowed to insert")
			}
		})
	}
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

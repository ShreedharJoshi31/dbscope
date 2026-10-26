package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/query/guard"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

const readerPass = "reader-pass"

// fixture is the test schema. orders is large enough for a Seq Scan with a
// filter to be "danger" in a plan-only overlay.
const fixture = `
CREATE SCHEMA app;
CREATE SCHEMA "Mixed Case";
CREATE TYPE app.mood AS ENUM ('happy', 'sad');
CREATE TABLE app.types (id int PRIMARY KEY, i2 int2, i4 int4, i8 int8, big int8, neg int8, f4 float4, f8 float8,
  nan float8, inf float8, num numeric, b bool, j json, jb jsonb, by bytea, ts timestamptz, d date, iv interval,
  u uuid, arr int[], tarr text[], m app.mood, nul text, o oid, pt point);
INSERT INTO app.types VALUES (1, 1, 2, 9007199254740991, 9007199254740993, -9007199254740993, 1.5, 2.25,
  'NaN', 'Infinity', 12345678901234567890.123, true, '{"a": [1, 2]}', '{"big": 12345678901234567890}',
  '\xdeadbeef', '2026-10-27 01:02:03+05:30', '2026-10-27', '1 day 2 hours',
  'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '{1,2,3}', '{a,"b c"}', 'happy', NULL, 42, '(1,2)');
CREATE TABLE app.accounts (id bigint PRIMARY KEY, email text NOT NULL);
INSERT INTO app.accounts SELECT g, 'u' || g || '@example.com' FROM generate_series(1, 1000) g;
CREATE TABLE app.orders (id bigint PRIMARY KEY, acct_id bigint NOT NULL REFERENCES app.accounts, total numeric NOT NULL, note text);
INSERT INTO app.orders SELECT g, 1 + g % 1000, g, 'n' FROM generate_series(1, 200000) g;
CREATE INDEX orders_acct ON app.orders (acct_id);
CREATE TABLE app.events (id bigint NOT NULL, at date NOT NULL, PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
CREATE TABLE app.events_2026 PARTITION OF app.events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
CREATE TABLE app.events_2027 PARTITION OF app.events FOR VALUES FROM ('2027-01-01') TO ('2028-01-01');
INSERT INTO app.events SELECT g, DATE '2026-01-01' + (g % 700) FROM generate_series(1, 20000) g;
CREATE VIEW app.big_orders AS SELECT o.id, a.email FROM app.orders o JOIN app.accounts a ON a.id = o.acct_id WHERE o.total > 100;
CREATE SEQUENCE app.s;
CREATE FUNCTION app.writer() RETURNS int LANGUAGE sql VOLATILE AS $$INSERT INTO app.accounts VALUES (-1, 'x') RETURNING 1$$;
ANALYZE;
`

type testDB struct {
	*pgtest.DB
	srv    *pgtest.Server
	reader *pgxpool.Pool
	super  *pgxpool.Pool
}

// setupDB creates the fixture on srv and pools (built by conn.NewPool, as
// the agent builds them) for a SELECT-only role and the superuser.
func setupDB(t *testing.T, srv *pgtest.Server) *testDB {
	t.Helper()
	d := srv.NewDB(t)
	d.Exec(t, fixture)
	role := d.Role("reader")
	d.Exec(t, `CREATE ROLE `+role+` LOGIN PASSWORD '`+readerPass+`';
		GRANT USAGE ON SCHEMA app, "Mixed Case" TO `+role+`; GRANT SELECT ON ALL TABLES IN SCHEMA app TO `+role)
	td := &testDB{DB: d, srv: srv}
	td.reader = newPool(t, d.As(t, role), readerPass)
	sp, pw := d.Superuser(t)
	td.super = newPool(t, sp, pw)
	return td
}

func newPool(t *testing.T, p conn.Profile, password string) *pgxpool.Pool {
	t.Helper()
	pool, err := conn.NewPool(context.Background(), p, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func check(t *testing.T, sql string) *guard.Parsed {
	t.Helper()
	p, err := guard.Check(sql)
	if err != nil {
		t.Fatalf("guard rejected %q: %v", sql, err)
	}
	return p
}

// unguarded builds a Parsed that skipped the guard: the seam for testing
// what the transaction alone enforces.
func unguarded(sql string) *guard.Parsed {
	return &guard.Parsed{SQL: sql, Deparsed: sql}
}

func req(p *guard.Parsed) Request {
	return Request{Parsed: p, Schemas: []string{"app"}, StatementTimeout: 10 * time.Second, MaxRows: 500}
}

func run(t *testing.T, db DB, r Request) *Result {
	t.Helper()
	res, err := Run(context.Background(), db, r)
	if err != nil {
		t.Fatalf("%s: %v", r.Parsed.Deparsed, err)
	}
	return res
}

func eachServer(t *testing.T, f func(t *testing.T, d *testDB)) {
	for _, srv := range pgtest.Servers(t, "14", "18") {
		t.Run(srv.String(), func(t *testing.T) { f(t, setupDB(t, srv)) })
	}
}

func TestValueMapping(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		res := run(t, d.reader, req(check(t, "SELECT *, 1 + 1 AS expr FROM types")))
		want := map[string]string{
			"i2": `1`, "i4": `2`, "i8": `9007199254740991`, "big": `"9007199254740993"`, "neg": `"-9007199254740993"`,
			"f4": `1.5`, "f8": `2.25`, "nan": `"NaN"`, "inf": `"Infinity"`, "num": `"12345678901234567890.123"`,
			"b": `true`, "j": `{"a": [1, 2]}`, "jb": `{"big": 12345678901234567890}`, "by": `"\\xdeadbeef"`,
			"ts": `"2026-10-26 19:32:03+00"`, "d": `"2026-10-27"`, "iv": `"P1DT2H"`,
			"u": `"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"`, "arr": `"{1,2,3}"`, "tarr": `"{a,\"b c\"}"`,
			"m": `"happy"`, "nul": `null`, "o": `42`, "pt": `"(1,2)"`, "expr": `2`,
		}
		types := map[string]string{"i2": "smallint", "big": "bigint", "num": "numeric", "m": "mood", // app is on the search path
			"arr": "integer[]", "ts": "timestamp with time zone", "expr": "integer", "jb": "jsonb"}
		if len(res.Rows) != 1 || res.RowCount != 1 || res.Truncated {
			t.Fatalf("rows %d rowCount %d truncated %v", len(res.Rows), res.RowCount, res.Truncated)
		}
		for i, c := range res.Columns {
			if w, ok := want[c.Name]; ok && string(res.Rows[0][i]) != w {
				t.Errorf("%s = %s, want %s", c.Name, res.Rows[0][i], w)
			}
			if w, ok := types[c.Name]; ok && c.Type != w {
				t.Errorf("%s type %q, want %q", c.Name, c.Type, w)
			}
			switch {
			case c.Name == "expr" && (c.Relation != "" || c.Column != ""):
				t.Errorf("expression column mapped to %s.%s", c.Relation, c.Column)
			case c.Name != "expr" && (c.Relation != "app.types" || c.Column != c.Name):
				t.Errorf("column %s mapped to %q.%q", c.Name, c.Relation, c.Column)
			}
		}
		// The whole result must be valid JSON.
		if _, err := json.Marshal(res); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTruncatedAndSettings(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		r := req(check(t, "SELECT g FROM generate_series(1, 10) g"))
		r.MaxRows = 3
		res := run(t, d.reader, r)
		if res.RowCount != 3 || !res.Truncated || string(res.Rows[2][0]) != "3" {
			t.Fatalf("maxRows 3: rowCount %d truncated %v", res.RowCount, res.Truncated)
		}
		r.MaxRows = 10
		if res := run(t, d.reader, r); res.RowCount != 10 || res.Truncated {
			t.Fatalf("maxRows 10: rowCount %d truncated %v", res.RowCount, res.Truncated)
		}

		setting := func(schemas []string, name string) string {
			r := req(check(t, "SELECT current_setting('"+name+"')"))
			r.Schemas = schemas
			return strings.Trim(string(run(t, d.reader, r).Rows[0][0]), `"`)
		}
		if got := setting([]string{"app", "Mixed Case"}, "search_path"); got != `pg_catalog, \"app\", \"Mixed Case\", pg_temp` {
			t.Errorf("search_path %s", got)
		}
		if got := setting(nil, "search_path"); got != `pg_catalog, \"Mixed Case\", app, public, pg_temp` {
			t.Errorf("default search_path %s", got)
		}
		for name, want := range map[string]string{"statement_timeout": "10s", "lock_timeout": "2s",
			"TimeZone": "UTC", "IntervalStyle": "iso_8601", "transaction_isolation": "repeatable read",
			"transaction_read_only": "on", "cursor_tuple_fraction": "1"} {
			if got := setting(nil, name); got != want {
				t.Errorf("%s = %s, want %s", name, got, want)
			}
		}
		if d.srv.Major >= 17 {
			if got := setting(nil, "transaction_timeout"); got != "15s" {
				t.Errorf("transaction_timeout %s", got)
			}
		}
	})
}

// TestRollback proves the transaction ends in ROLLBACK: a pg_notify (which a
// read-only transaction accepts) is never delivered.
func TestRollback(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		ctx := context.Background()
		sp, pw := d.Superuser(t)
		listener := d.Connect(t, sp.User, pw)
		if _, err := listener.Exec(ctx, "LISTEN dbscope_test"); err != nil {
			t.Fatal(err)
		}
		run(t, d.reader, req(unguarded("SELECT pg_notify('dbscope_test', 'from-dbscope')")))
		wctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		n, err := listener.WaitForNotification(wctx)
		cancel()
		if err == nil {
			t.Fatalf("notification delivered: %+v", n)
		}
		// Control: the same notify in a committed read-only transaction arrives.
		tx, err := d.reader.BeginTx(ctx, txOptions)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "SELECT pg_notify('dbscope_test', 'committed')"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		wctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if n, err := listener.WaitForNotification(wctx); err != nil || n.Payload != "committed" {
			t.Fatalf("control notification: %v %+v", err, n)
		}
	})
}

// TestReadOnly runs writes that skipped the guard as a superuser: only the
// READ ONLY transaction stops them.
func TestReadOnly(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		for _, sql := range []string{"SELECT nextval('app.s')", "SELECT app.writer()"} {
			_, err := Run(context.Background(), d.super, req(unguarded(sql)))
			var de *DBError
			if !errors.As(err, &de) || de.Err.Code != "25006" {
				t.Errorf("%s: %v, want 25006", sql, err)
			}
		}
		var n int
		if err := d.Admin.QueryRow(context.Background(), "SELECT count(*) FROM app.accounts WHERE id = -1").Scan(&n); err != nil || n != 0 {
			t.Fatalf("write happened: %d %v", n, err)
		}
	})
}

func TestErrors(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		// The guard's function check runs inside the transaction.
		_, err := Run(context.Background(), d.reader, req(check(t, "SELECT pg_sleep(1)")))
		var ge *guard.Error
		if !errors.As(err, &ge) || ge.Reason != guard.FunctionNotAllowed {
			t.Fatalf("pg_sleep: %v", err)
		}
		// Unqualified user functions resolve through the pinned search_path.
		_, err = Run(context.Background(), d.reader, req(check(t, "SELECT writer()")))
		if !errors.As(err, &ge) || ge.Reason != guard.FunctionNotAllowed || !strings.Contains(ge.Message, "app.writer()") {
			t.Fatalf("writer via search_path: %v", err)
		}
		// A database error carries its position in the executed SQL.
		p := check(t, "select nosuch from orders")
		_, err = Run(context.Background(), d.reader, req(p))
		var de *DBError
		if !errors.As(err, &de) || de.Err.Code != "42703" || de.Position != strings.Index(p.Deparsed, "nosuch")+1 {
			t.Fatalf("unknown column: %v (position %d in %q)", err, de.Position, p.Deparsed)
		}
		// Statement timeout.
		r := req(check(t, "SELECT count(*) FROM generate_series(1, 100000000000)"))
		r.StatementTimeout = 300 * time.Millisecond
		start := time.Now()
		_, err = Run(context.Background(), d.reader, r)
		if !errors.As(err, &de) || de.Err.Code != "57014" || !strings.Contains(de.Err.Message, "statement timeout") {
			t.Fatalf("timeout: %v", err)
		}
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("timeout took %v", el)
		}
		// The pool's connection still works afterwards.
		run(t, d.reader, req(check(t, "SELECT 1")))
	})
}

// TestCancel cancels the context mid-statement and checks that the server
// stops running it, not just that the client gave up.
func TestCancel(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := Run(ctx, d.reader, req(unguarded("SELECT pg_sleep(30)")))
			done <- err
		}()
		waitActive(t, d, true)
		cancel()
		start := time.Now()
		waitActive(t, d, false)
		if el := time.Since(start); el > 2*time.Second {
			t.Fatalf("backend still active %v after cancel", el)
		}
		if err := <-done; err == nil {
			t.Fatal("cancelled run returned no error")
		}
		run(t, d.reader, req(check(t, "SELECT 1")))
	})
}

// waitActive waits up to 2s for a dbscope backend to be (or stop being)
// active in the test database.
func waitActive(t *testing.T, d *testDB, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var n int
		err := d.Admin.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = 'dbscope' AND state = 'active'
			  AND pid <> pg_backend_pid()`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if (n > 0) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("active=%v not reached within 2s", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCostGate(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		r := req(check(t, "SELECT * FROM orders"))
		r.MaxCost = 100
		_, err := Run(context.Background(), d.reader, r)
		var ce *CostError
		if !errors.As(err, &ce) || ce.EstimatedCost <= 100 || ce.EstimatedRows < 150000 || ce.Limit != 100 {
			t.Fatalf("cost gate: %v %+v", err, ce)
		}
		r.ConfirmCost = true
		r.MaxRows = 5
		res := run(t, d.reader, r)
		if res.RowCount != 5 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "over this profile's limit") {
			t.Fatalf("confirmed: %d rows, warnings %q", res.RowCount, res.Warnings)
		}
		// Plain EXPLAIN executes nothing, so it is not gated.
		r = req(check(t, "SELECT * FROM orders"))
		r.MaxCost, r.ExplainOnly = 100, true
		if res := run(t, d.reader, r); res.Plan == nil || len(res.Rows) != 0 {
			t.Fatal("explain only")
		}
		r.Analyze = true
		if _, err := Run(context.Background(), d.reader, r); !errors.As(err, &ce) {
			t.Fatalf("explain analyze not gated: %v", err)
		}
	})
}

func TestColumnRelations(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		for sql, want := range map[string][]string{
			"SELECT e.id, e.at, 1 AS one FROM events_2026 e LIMIT 1":                                 {"app.events.id", "app.events.at", ""},
			"SELECT id FROM events LIMIT 1":                                                          {"app.events.id"},
			"SELECT o.id, a.email AS mail FROM orders o JOIN accounts a ON a.id = o.acct_id LIMIT 1": {"app.orders.id", "app.accounts.email"},
			// A view's columns map to the view, which the ERD also shows.
			"SELECT id, email FROM big_orders LIMIT 1": {"app.big_orders.id", "app.big_orders.email"},
		} {
			res := run(t, d.reader, req(check(t, sql)))
			var got []string
			for _, c := range res.Columns {
				if c.Relation == "" {
					got = append(got, "")
				} else {
					got = append(got, c.Relation+"."+c.Column)
				}
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: %q, want %q", sql, got, want)
			}
		}
	})
}

func TestOverlay(t *testing.T) {
	eachServer(t, func(t *testing.T, d *testDB) {
		explain := func(sql string, analyze bool) *Result {
			r := req(check(t, sql))
			r.ExplainOnly, r.Analyze = true, analyze
			return run(t, d.reader, r)
		}
		level := func(res *Result, rel string) string {
			if ro := res.Overlay.Relations[rel]; ro != nil {
				return ro.Level
			}
			return "missing"
		}
		res := explain("SELECT * FROM orders WHERE note = 'x'", false)
		if level(res, "app.orders") != "danger" || res.Plan.CostShare == 0 {
			t.Errorf("filtered seq scan: %+v", res.Overlay.Relations["app.orders"])
		}
		if res := explain("SELECT * FROM orders", false); level(res, "app.orders") != "warn" {
			t.Errorf("plain seq scan: %+v", res.Overlay.Relations["app.orders"])
		}
		if res := explain("SELECT * FROM orders WHERE id = 5", false); level(res, "app.orders") != "good" ||
			res.Overlay.Relations["app.orders"].Scans[0].NodeType != "Index Scan" {
			t.Errorf("index scan: %+v", res.Overlay.Relations["app.orders"])
		}
		res = explain("SELECT * FROM orders WHERE note = 'x'", true)
		ro := res.Overlay.Relations["app.orders"]
		if ro == nil || ro.Level != "danger" || len(ro.Reasons) == 0 || !strings.Contains(ro.Reasons[0], "discards") ||
			res.Plan.TimeShare == nil || ro.Scans[0].ActualRows == nil {
			t.Errorf("analyzed filtered seq scan: %+v", ro)
		}
		res = explain("SELECT * FROM events WHERE id > 5", false)
		if _, ok := res.Overlay.Relations["app.events"]; !ok || len(res.Overlay.Relations) != 1 {
			t.Errorf("partitions not mapped to the root: %v", keys(res.Overlay.Relations))
		}
		if n := len(res.Overlay.Relations["app.events"].Scans); n != 2 {
			t.Errorf("partition scans %d, want 2", n)
		}
		res = explain("SELECT * FROM big_orders WHERE id < 1000", false)
		if !slices.Equal(keys(res.Overlay.Relations), []string{"app.accounts", "app.orders"}) || len(res.Overlay.Joins) != 1 {
			t.Errorf("view: relations %v joins %+v", keys(res.Overlay.Relations), res.Overlay.Joins)
		}
		j := res.Overlay.Joins[0]
		pair := []string{j.From, j.To}
		slices.Sort(pair)
		if !slices.Equal(pair, []string{"app.accounts", "app.orders"}) || j.JoinType != "Inner" {
			t.Errorf("join %+v", j)
		}
	})
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestWritePlanFixtures regenerates internal/query/plan/testdata from real
// servers when DBSCOPE_WRITE_PLAN_FIXTURES=1.
func TestWritePlanFixtures(t *testing.T) {
	if os.Getenv("DBSCOPE_WRITE_PLAN_FIXTURES") != "1" {
		t.Skip("set DBSCOPE_WRITE_PLAN_FIXTURES=1 to regenerate plan fixtures")
	}
	queries := map[string]string{
		"seqscan_filter": "SELECT * FROM app.orders WHERE note = 'x'",
		"index_scan":     "SELECT * FROM app.orders WHERE id = 5",
		"join":           "SELECT o.id, a.email FROM app.orders o JOIN app.accounts a ON a.id = o.acct_id WHERE o.total < 5000",
		"partitions":     "SELECT * FROM app.events WHERE id > 5",
		"bitmap":         "SELECT * FROM app.orders WHERE acct_id IN (1, 2, 3)",
		"cte_sort_limit": "WITH t AS MATERIALIZED (SELECT acct_id, sum(total) s FROM app.orders GROUP BY 1) SELECT * FROM t ORDER BY s DESC LIMIT 10",
		"nested_loop":    "SELECT o.id, a.email FROM app.accounts a JOIN app.orders o ON o.acct_id = a.id WHERE a.id < 3",
		"disk_sort":      "SELECT * FROM app.orders ORDER BY note, total DESC",
	}
	var mu sync.Mutex
	for _, srv := range pgtest.Servers(t, "14", "18") {
		d := setupDB(t, srv)
		ctx := context.Background()
		for name, sql := range queries {
			for _, analyze := range []bool{false, true} {
				tx, err := d.super.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				tx.Exec(ctx, "SET LOCAL work_mem = '64kB'")
				tx.Exec(ctx, "SET LOCAL max_parallel_workers_per_gather = 0")
				prefix, suffix := explainPrefix, ""
				if analyze {
					prefix, suffix = analyzePrefix, "_analyze"
				}
				var out []byte
				if err := tx.QueryRow(ctx, prefix+sql, pgx.QueryExecModeExec).Scan(&out); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				tx.Rollback(ctx)
				var b bytes.Buffer
				if err := json.Indent(&b, out, "", "  "); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				path := filepath.Join("..", "plan", "testdata", "pg"+srv.Name+"_"+name+suffix+".json")
				if err := os.WriteFile(path, append(b.Bytes(), '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				mu.Unlock()
			}
		}
	}
}

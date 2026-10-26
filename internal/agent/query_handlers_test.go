package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/store"
)

// apiErrorBody is the Phase 2 error envelope.
type apiErrorBody struct {
	Error struct {
		Code          string   `json:"code"`
		Message       string   `json:"message"`
		Detail        string   `json:"detail"`
		SQLState      string   `json:"sqlstate"`
		Position      int      `json:"position"`
		EstimatedCost *float64 `json:"estimatedCost"`
		EstimatedRows *float64 `json:"estimatedRows"`
	} `json:"error"`
}

func (e *env) history(id string) []store.HistoryEntry {
	e.t.Helper()
	w := e.do("GET", "/api/profiles/"+id+"/history", "")
	expect(e.t, w, 200, "")
	return decodeBody[[]store.HistoryEntry](e.t, w)
}

func TestQueryHandlers(t *testing.T) {
	e, f := fakeEnv(t)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"prod-replica","schemas":["app"],"password":"` + testPass + `"}`)
	base := "/api/profiles/" + p.ID

	// Success passes the profile's settings to the runner.
	w := e.do("POST", base+"/query", `{"sql":"select 1 as one -- c\n;","maxRows":10,"analyze":true}`)
	expect(t, w, 200, "")
	var res exec.Result
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.ExecutedSQL != "SELECT 1 AS one" || res.RowCount != 1 {
		t.Fatalf("result %s", w.Body.String())
	}
	r := f.requests[0]
	if r.MaxRows != 10 || !r.Analyze || r.ExplainOnly || r.MaxCost != 1e6 || r.StatementTimeout != 10*time.Second ||
		len(r.Schemas) != 1 || r.Schemas[0] != "app" || f.passwords[0] != testPass {
		t.Fatalf("request %+v", r)
	}
	expect(t, e.do("POST", base+"/query", `{"sql":"SELECT 1"}`), 200, "")
	if f.requests[1].MaxRows != 500 {
		t.Fatalf("default maxRows %d", f.requests[1].MaxRows)
	}

	// Explain only returns the plan fields.
	w = e.do("POST", base+"/explain", `{"sql":"SELECT 1","analyze":true}`)
	expect(t, w, 200, "")
	var ex map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &ex)
	if len(ex) != 4 || ex["plan"] == nil || ex["overlay"] == nil || ex["executedSql"] == nil || ex["warnings"] == nil {
		t.Fatalf("explain body %s", w.Body.String())
	}
	if r := f.requests[2]; !r.ExplainOnly || !r.Analyze {
		t.Fatalf("explain request %+v", r)
	}

	// Validation.
	for _, body := range []string{`{"sql":""}`, `{"sql":"  "}`, `{"sql":"SELECT 1","maxRows":0}`,
		`{"sql":"SELECT 1","maxRows":5001}`, `{"sql":"SELECT 1","extra":1}`, `{}`} {
		expect(t, e.do("POST", base+"/query", body), 400, "validation")
	}
	expect(t, e.do("POST", base+"/explain", `{"sql":"SELECT 1","maxRows":5}`), 400, "validation")
	expect(t, e.do("POST", "/api/profiles/missing/query", `{"sql":"SELECT 1"}`), 404, "not_found")

	// Guard rejections never reach the runner.
	before := len(f.requests)
	for sql, want := range map[string]string{
		"COMMIT; DROP TABLE users":                       "multiple_statements",
		"COPY (SELECT 1) TO PROGRAM 'id'":                "not_select",
		"WITH d AS (DELETE FROM t RETURNING *) SELECT 1": "writes_data",
		"SELECT * FROM t FOR UPDATE":                     "locking_clause",
		"SELECT * INTO x FROM t":                         "select_into",
	} {
		w := e.do("POST", base+"/query", mustJSON(map[string]string{"sql": sql}))
		expect(t, w, 422, "rejected")
		if got := decodeBody[apiErrorBody](t, w).Error; got.Detail != want || got.Position != 0 {
			t.Errorf("%s: %+v, want detail %s", sql, got, want)
		}
	}
	w = e.do("POST", base+"/query", `{"sql":"select from where"}`)
	expect(t, w, 422, "rejected")
	if got := decodeBody[apiErrorBody](t, w).Error; got.Detail != "syntax" || got.Position != 13 {
		t.Errorf("syntax: %+v", got)
	}
	if len(f.requests) != before {
		t.Fatal("rejected statements reached the runner")
	}

	// Runner errors map to the contract.
	pgErr := func(code, msg string) *pgconn.PgError {
		return &pgconn.PgError{Code: code, Message: msg, Severity: "ERROR"}
	}
	for _, c := range []struct {
		err          error
		status       int
		code         string
		sqlstate     string
		position     int
		historyState string
	}{
		{&exec.CostError{EstimatedCost: 2e6, EstimatedRows: 5e5, Limit: 1e6}, 409, "cost_confirmation_required", "", 0, "rejected"},
		{&exec.DBError{Err: &pgconn.PgError{Code: "42703", Message: "column \"x\" does not exist", Severity: "ERROR", Position: 99}, Position: 8}, 400, "database", "42703", 8, "error"},
		{&exec.DBError{Err: &pgconn.PgError{Code: "25006", Message: "cannot execute nextval() in a read-only transaction", Severity: "ERROR"}}, 400, "database", "25006", 0, "error"},
		{&exec.DBError{Err: pgErr("57014", "canceling statement due to statement timeout")}, 504, "timeout", "57014", 0, "error"},
		{pgErr("40001", "canceling statement due to conflict with recovery"), 503, "recovery_conflict", "40001", 0, "error"},
		{pgErr("40001", "could not serialize access"), 400, "database", "40001", 0, "error"},
		{fmt.Errorf("%w: %w", exec.ErrConnect, &net.OpError{Op: "dial", Err: errors.New("connection refused")}), 502, "upstream", "", 0, "error"},
		{fmt.Errorf("%w: %w", exec.ErrConnect, pgErr("28P01", "password authentication failed")), 502, "upstream", "28P01", 0, "error"},
		{&pgconn.PgError{Code: "25P04", Message: "terminating connection due to transaction timeout", Severity: "FATAL"}, 504, "timeout", "25P04", 0, "error"},
		{&pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command", Severity: "FATAL"}, 502, "upstream", "57P01", 0, "error"},
		{errClosed, 503, "internal", "", 0, "error"},
	} {
		f.queryErr = c.err
		w := e.do("POST", base+"/query", `{"sql":"SELECT 1"}`)
		expect(t, w, c.status, c.code)
		got := decodeBody[apiErrorBody](t, w).Error
		if got.SQLState != c.sqlstate || got.Position != c.position {
			t.Errorf("%v: %+v", c.err, got)
		}
		if c.code == "cost_confirmation_required" && (got.EstimatedCost == nil || *got.EstimatedCost != 2e6 || *got.EstimatedRows != 5e5) {
			t.Errorf("cost body %s", w.Body.String())
		}
		if h := e.history(p.ID)[0]; h.Status != c.historyState || h.SQL != "SELECT 1" {
			t.Errorf("%v: history %+v", c.err, h)
		}
	}

	// History: newest first, never row data, limit validated.
	hist := e.history(p.ID)
	if len(hist) != 2+6+11 || hist[len(hist)-1].RowCount != 1 || hist[len(hist)-1].Status != "ok" ||
		hist[len(hist)-1].SQL != "select 1 as one -- c\n;" {
		t.Fatalf("history %d entries, oldest %+v", len(hist), hist[len(hist)-1])
	}
	if h := hist[len(hist)-3]; h.Status != "rejected" || h.ErrorCode == "" {
		t.Errorf("rejected entry %+v", h)
	}
	w = e.do("GET", base+"/history?limit=3", "")
	expect(t, w, 200, "")
	if got := decodeBody[[]store.HistoryEntry](t, w); len(got) != 3 || got[0].ID != hist[0].ID {
		t.Fatalf("limit 3: %+v", got)
	}
	for _, q := range []string{"0", "201", "x"} {
		expect(t, e.do("GET", base+"/history?limit="+q, ""), 400, "validation")
	}
	expect(t, e.do("GET", "/api/profiles/missing/history", ""), 404, "not_found")
	for _, h := range hist {
		if strings.Contains(mustJSON(h), `"rows"`) {
			t.Fatal("history holds row data")
		}
	}
	expect(t, e.do("DELETE", base, ""), 204, "")
	if _, err := e.store.ListHistory(t.Context(), p.ID, 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("history after delete: %v", err)
	}
}

func TestQueryLockedVault(t *testing.T) {
	e := lockedEnv(t, "pp")
	pi, _ := conn.ProfileInput{Name: "a", Host: "h", Database: "d", User: "u", Env: conn.EnvLocal}.Profile()
	pi.HasPassword = true
	p, err := e.store.CreateProfile(t.Context(), pi)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT 1"}`), 423, "secrets_locked")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/explain", `{"sql":"SELECT 1"}`), 423, "secrets_locked")
}

func TestProfileMaxCost(t *testing.T) {
	e, f := fakeEnv(t)
	body := func(extra string) string {
		return `{"name":"a","host":"h","database":"d","user":"u","env":"staging"` + extra + `}`
	}
	expect(t, e.do("POST", "/api/profiles", body(`,"maxCost":-1`)), 400, "validation")
	p := e.createProfile(body(""))
	w := e.do("GET", "/api/profiles", "")
	if strings.Contains(w.Body.String(), "maxCost") {
		t.Fatalf("unset maxCost serialised: %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT 1"}`), 200, "")
	w = e.do("PUT", "/api/profiles/"+p.ID, body(`,"maxCost":0`))
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"maxCost":0`) {
		t.Fatalf("maxCost 0 not returned: %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT 1"}`), 200, "")
	if f.requests[0].MaxCost != 1e7 || f.requests[1].MaxCost != 0 {
		t.Fatalf("cost limits %v, %v", f.requests[0].MaxCost, f.requests[1].MaxCost)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestQueryEndToEnd runs the routes against PostgreSQL 18 through the
// real pools, then cancels a running query by disconnecting the client.
func TestQueryEndToEnd(t *testing.T) {
	srv := pgtest.Servers(t, "18")[0]
	d := srv.NewDB(t)
	reader := d.Role("reader")
	fixture, err := os.ReadFile("../introspect/testdata/fixture.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(fixture), "ROLE reader", "ROLE "+reader)
	sql = strings.ReplaceAll(sql, "TO reader", "TO "+reader)
	d.Exec(t, strings.Replace(sql, "PASSWORD 'r'", "PASSWORD '"+testPass+"'", 1))
	d.Exec(t, `INSERT INTO app.accounts SELECT 'eu', g, 'u' || g || '@x' FROM generate_series(1, 5000) g; ANALYZE`)

	e := keyringEnv(t)
	prof := d.As(t, reader)
	body := func(extra string) string {
		return `{"name":"e2e","host":"` + prof.Host + `","port":` + strconv.Itoa(prof.Port) + `,"database":"` + prof.Database +
			`","user":"` + reader + `","sslMode":"` + string(prof.SSLMode) + `","env":"local","password":"` + testPass + `"` + extra + `}`
	}
	p := e.createProfile(body(`,"maxCost":50`))
	base := "/api/profiles/" + p.ID

	w := e.do("POST", base+"/query", `{"sql":"SELECT id, email FROM app.accounts WHERE id = 7"}`)
	expect(t, w, 200, "")
	res := decodeBody[exec.Result](t, w)
	if res.RowCount != 1 || string(res.Rows[0][0]) != "7" || res.Columns[1].Relation != "app.accounts" ||
		res.Overlay.Relations["app.accounts"] == nil {
		t.Fatalf("result %s", w.Body.String())
	}

	// The cost gate, then confirmation.
	w = e.do("POST", base+"/query", `{"sql":"SELECT * FROM app.accounts"}`)
	expect(t, w, 409, "cost_confirmation_required")
	if got := decodeBody[apiErrorBody](t, w).Error; got.EstimatedCost == nil || *got.EstimatedCost <= 50 || *got.EstimatedRows != 5000 {
		t.Fatalf("409 body %s", w.Body.String())
	}
	w = e.do("POST", base+"/query", `{"sql":"SELECT * FROM app.accounts","confirmCost":true,"maxRows":5}`)
	expect(t, w, 200, "")
	if res := decodeBody[exec.Result](t, w); res.RowCount != 5 || !res.Truncated || len(res.Warnings) != 1 {
		t.Fatalf("confirmed %s", w.Body.String())
	}

	// Errors from the real database.
	w = e.do("POST", base+"/query", `{"sql":"SELECT nosuch FROM app.accounts"}`)
	expect(t, w, 400, "database")
	if got := decodeBody[apiErrorBody](t, w).Error; got.SQLState != "42703" || got.Position != 8 {
		t.Fatalf("database error %+v", got)
	}
	w = e.do("POST", base+"/query", `{"sql":"SELECT pg_sleep(1)"}`)
	expect(t, w, 422, "rejected")
	if got := decodeBody[apiErrorBody](t, w).Error; got.Detail != "function_not_allowed" {
		t.Fatalf("pg_sleep %+v", got)
	}
	w = e.do("POST", base+"/explain", `{"sql":"SELECT * FROM app.events WHERE id > 1"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"app.events"`) || strings.Contains(w.Body.String(), `"app.events_2026":{`) {
		t.Fatalf("explain overlay %s", w.Body.String())
	}

	// Statement timeout gives 504.
	expect(t, e.do("PUT", base, body(`,"statementTimeoutMs":300`)), 200, "")
	w = e.do("POST", base+"/query", `{"sql":"SELECT count(*) FROM generate_series(1, 100000000000)"}`)
	expect(t, w, 504, "timeout")
	expect(t, e.do("PUT", base, body("")), 200, "")

	// Client disconnect cancels the statement server-side.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	a, err := New(Config{Port: port, Token: testToken, Store: e.store, Secrets: e.vault})
	if err != nil {
		t.Fatal(err)
	}
	hs := &httptest.Server{Listener: ln, Config: &http.Server{Handler: a}}
	hs.Start()
	defer a.Close()
	defer hs.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "POST", hs.URL+base+"/query",
			strings.NewReader(`{"sql":"SELECT count(*) FROM generate_series(1, 100000000000)"}`))
		req.Header.Set(TokenHeader, testToken)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	active := func() int {
		var n int
		if err := d.Admin.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND usename = $1 AND state = 'active'`, reader).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	waitFor := func(what string, cond func() bool) time.Duration {
		start := time.Now()
		for !cond() {
			if time.Since(start) > 2*time.Second {
				t.Fatalf("%s: not within 2s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
		return time.Since(start)
	}
	waitFor("query starts", func() bool { return active() == 1 })
	cancel()
	el := waitFor("backend stops", func() bool { return active() == 0 })
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("client error %v", err)
	}
	t.Logf("backend stopped %v after the client disconnected", el)
	waitFor("history records the cancel", func() bool {
		h, _ := e.store.ListHistory(context.Background(), p.ID, 1)
		return len(h) == 1 && h[0].Status == store.HistoryCancelled
	})

	hist := e.history(p.ID)
	statuses := []string{}
	for _, h := range hist {
		statuses = append(statuses, h.Status)
	}
	want := []string{"cancelled", "error", "rejected", "error", "ok", "rejected", "ok"} // explain is not recorded
	if strings.Join(statuses, ",") != strings.Join(want, ",") {
		t.Fatalf("history statuses %v, want %v", statuses, want)
	}
}

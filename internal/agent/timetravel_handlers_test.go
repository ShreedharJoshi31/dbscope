package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mysql"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/exec"
)

func TestParseAsOf(t *testing.T) {
	now := time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want time.Time
		err  string
	}{
		{"2026-11-15T09:00:00Z", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC), ""},
		{"2026-11-15T14:30:00.123456789+05:30", time.Date(2026, 11, 15, 9, 0, 0, 123456789, time.UTC), ""},
		{"2026-11-15T09:30:01Z", time.Date(2026, 11, 15, 9, 30, 1, 0, time.UTC), ""},
		{"2026-11-15T09:30:01.5Z", time.Time{}, "in the future"},
		{"2026-11-15T09:00:00", time.Time{}, "RFC 3339"},
		{"2026-11-15 09:00:00Z", time.Time{}, "RFC 3339"},
		{"yesterday", time.Time{}, "RFC 3339"},
	} {
		got, err := parseAsOf(c.in, now)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || !got.Equal(c.want) || got.Location() != time.UTC {
			t.Errorf("%s: %s %v, want %s", c.in, got, err, c.want)
		}
	}
}

func TestAsOfRunError(t *testing.T) {
	for code, want := range map[string]string{
		"ora_1555":  "This table cannot be read that far back (ORA-01555): choose a later time",
		"ora_8180":  "This table cannot be read that far back (ORA-08180): choose a later time",
		"ora_1466":  "This table cannot be read that far back (ORA-01466): choose a later time",
		"ora_8186":  "The time is in the future by the database's clock (ORA-08186): choose an earlier time",
		"ora_41900": "Blocked: reading as of a past time needs the FLASHBACK privilege on every table and view the query reads, or FLASHBACK ANY TABLE (msg)",
		"ora_942":   "msg",
	} {
		r := &runError{histCode: code, message: "msg"}
		asOfRunError(r)
		if r.message != want {
			t.Errorf("%s: %q, want %q", code, r.message, want)
		}
	}
}

// TestAsOfRequest: asOf decodes on both routes, an engine without time travel
// refuses it, and an empty asOf is today's request.
func TestAsOfRequest(t *testing.T) {
	e, f := fakeEnv(t)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"local","password":"` + testPass + `"}`)
	base := "/api/profiles/" + p.ID
	expect(t, e.do("POST", base+"/query", `{"sql":"SELECT 1","asOf":""}`), 200, "")
	expect(t, e.do("POST", base+"/query", `{"sql":"SELECT 1","asOf":"2026-01-01T00:00:00Z"}`), 501, "unsupported")
	expect(t, e.do("POST", base+"/explain", `{"sql":"SELECT 1","asOf":"2026-01-01T00:00:00Z"}`), 501, "unsupported")
	expect(t, e.do("GET", base+"/time-travel?tables=public.a", ""), 501, "unsupported")
	if len(f.requests) != 1 {
		t.Fatalf("%d runs; only the request without asOf runs", len(f.requests))
	}
}

// TestAsOfKeepsGuard: on every engine with time travel, a statement the guard
// refuses gets the same refusal with asOf as without, before any connection.
func TestAsOfKeepsGuard(t *testing.T) {
	e := keyringEnv(t)
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for engine, stmts := range map[string][]string{
		"oracle": {"DELETE FROM tt.acct", "UPDATE tt.acct SET bal = 0", "INSERT INTO tt.acct SELECT * FROM tt.acct2",
			"SELECT * FROM tt.acct FOR UPDATE", "SELECT bal INTO :x FROM tt.acct", "SELECT * FROM tt.acct; DROP TABLE tt.acct"},
		"sqlserver": {"DELETE FROM dbo.acct", "UPDATE dbo.acct SET bal = 0", "INSERT INTO dbo.acct SELECT * FROM dbo.acct2",
			"SELECT * FROM dbo.acct WITH (UPDLOCK)", "SELECT * INTO dbo.copy FROM dbo.acct"},
		"mysql": {"DELETE FROM acct", "UPDATE acct SET bal = 0", "INSERT INTO acct SELECT * FROM acct2",
			"SELECT * FROM acct FOR UPDATE", "SELECT * INTO OUTFILE '/tmp/x' FROM acct"},
	} {
		p := e.createProfile(fmt.Sprintf(`{"name":%q,"engine":%q,"host":"127.0.0.1","port":%d,"database":"d","user":"u","env":"local"}`,
			engine, engine, closedPort(t)))
		if !p.Capabilities.TimeTravel {
			t.Fatalf("%s: no time travel capability", engine)
		}
		for _, stmt := range stmts {
			for _, route := range []string{"/query", "/explain"} {
				plain := e.do("POST", "/api/profiles/"+p.ID+route, mustJSON(map[string]string{"sql": stmt}))
				asOf := e.do("POST", "/api/profiles/"+p.ID+route, mustJSON(map[string]string{"sql": stmt, "asOf": at}))
				expect(t, plain, 422, "rejected")
				if asOf.Code != plain.Code || asOf.Body.String() != plain.Body.String() {
					t.Errorf("%s %s %s: with asOf %d %s, without %d %s", engine, route, stmt, asOf.Code, asOf.Body, plain.Code, plain.Body)
				}
			}
		}
		base := "/api/profiles/" + p.ID
		for _, body := range []string{`{"sql":"SELECT 1 FROM dual","asOf":"2026-01-01T00:00:00"}`,
			`{"sql":"SELECT 1 FROM dual","asOf":"` + time.Now().Add(time.Minute).UTC().Format(time.RFC3339) + `"}`} {
			w := e.do("POST", base+"/query", body)
			expect(t, w, 400, "validation")
		}
		// The zero time is still an as-of read: the rewriter refuses text that already reads history.
		w := e.do("POST", base+"/query", mustJSON(map[string]string{"sql": "SELECT * FROM t FOR SYSTEM_TIME ALL", "asOf": "0001-01-01T00:00:00Z"}))
		if engine != "mysql" {
			expect(t, w, 400, "validation")
		}
		for _, q := range []string{"", "?tables=", "?tables=nodot", "?tables=a.b,.c"} {
			expect(t, e.do("GET", base+"/time-travel"+q, ""), 400, "validation")
		}
	}
}

// TestMariaDBAsOf runs the HTTP path on a real server: the rewrite runs, the
// response shows the rewritten text, history keeps the text as written, and
// refusals and bounds come back in the API's shapes.
func TestMariaDBAsOf(t *testing.T) {
	srv := mytest.Server(t, "mariadb")
	db, err := mysql.Open(srv, mytest.Password, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		"CREATE TABLE acct (id INT PRIMARY KEY, bal INT) WITH SYSTEM VERSIONING",
		"CREATE TABLE plain (id INT PRIMARY KEY)",
		"INSERT INTO acct VALUES (1, 100)",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	at := time.Now().UTC()
	time.Sleep(1500 * time.Millisecond)
	if _, err := db.ExecContext(ctx, "UPDATE acct SET bal = 200"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	e := keyringEnv(t)
	p := e.createProfile(mustJSON(map[string]any{"name": "m", "engine": "mysql", "host": srv.Host, "port": srv.Port,
		"database": "app", "user": "root", "password": mytest.Password, "sslMode": "disable", "env": "local"}))
	base := "/api/profiles/" + p.ID
	sql := "SELECT bal FROM acct WHERE id = 1"
	w := e.do("POST", base+"/query", mustJSON(map[string]string{"sql": sql, "asOf": at.Format(time.RFC3339Nano)}))
	expect(t, w, 200, "")
	res := decodeBody[exec.Result](t, w)
	if want := "SELECT bal FROM acct " + mysql.AsOfClause(at) + " WHERE id = 1"; res.ExecutedSQL != want || string(res.Rows[0][0]) != "100" {
		t.Errorf("executed %q rows %s; want %q and 100", res.ExecutedSQL, res.Rows, want)
	}
	w = e.do("POST", base+"/explain", mustJSON(map[string]string{"sql": sql, "asOf": at.Format(time.RFC3339)}))
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), "FOR SYSTEM_TIME AS OF FROM_UNIXTIME(") {
		t.Errorf("explain %s", w.Body.String())
	}
	if h := e.history(p.ID); len(h) != 1 || h[0].SQL != sql || h[0].Status != "ok" {
		t.Errorf("history %+v", h)
	}
	w = e.do("POST", base+"/query", mustJSON(map[string]string{"sql": "SELECT * FROM acct, plain", "asOf": at.Format(time.RFC3339)}))
	expect(t, w, 400, "validation")
	if got := decodeBody[apiErrorBody](t, w).Error.Message; got != "Cannot read as of a past time: plain is not a system-versioned table" {
		t.Errorf("refusal %q", got)
	}
	w = e.do("GET", base+"/time-travel?tables=app.acct,app.plain", "")
	expect(t, w, 200, "")
	var b struct {
		Now    time.Time
		Tables []map[string]any
		Note   string
	}
	json.Unmarshal(w.Body.Bytes(), &b)
	if len(b.Tables) != 2 || b.Tables[0]["kind"] != "system-versioned" || b.Tables[0]["oldest"] != nil ||
		b.Tables[0]["source"] != "none" || b.Tables[1]["kind"] != nil || b.Tables[1]["available"] != false || b.Note == "" {
		t.Errorf("bounds %s", w.Body.String())
	}
}

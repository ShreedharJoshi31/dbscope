package agent

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestSQLiteProfile(t *testing.T) {
	e := keyringEnv(t)
	path := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users);
		INSERT INTO users (email) VALUES ('a@x')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	body, _ := json.Marshal(map[string]any{"name": "Shop", "engine": "sqlite", "path": path, "env": "local"})
	w := e.do("POST", "/api/profiles/test", string(body))
	expect(t, w, 200, "")
	var tr conn.TestResult
	json.Unmarshal(w.Body.Bytes(), &tr)
	if !tr.OK || tr.ServerVersionNum < 3_037_000 {
		t.Fatalf("test %s", w.Body.String())
	}

	w = e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Engine != conn.EngineSQLite || p.Path != path || !p.Capabilities.Query || !p.Capabilities.Migrations || p.Capabilities.Workload {
		t.Fatalf("created %s", w.Body.String())
	}

	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "sqlite" || len(m.Relations) != 2 || len(m.Relations[0].ForeignKeys) != 1 {
		t.Fatalf("schema %s", w.Body.String())
	}

	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT u.email, count(o.id) AS n FROM users u LEFT JOIN orders o ON o.user_id = u.id GROUP BY u.email"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"rows":[["a@x",0]]`) || !strings.Contains(w.Body.String(), `"name":"email"`) {
		t.Errorf("query %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/explain", `{"sql":"SELECT * FROM orders WHERE user_id = 1"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"plan":{"nodeType":"Query"`) {
		t.Errorf("explain %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"DELETE FROM users"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM nope"}`), 400, "database")
	w = e.do("GET", "/api/profiles/"+p.ID+"/history", "")
	if !strings.Contains(w.Body.String(), `"status":"rejected"`) || !strings.Contains(w.Body.String(), `"rowCount":1`) {
		t.Errorf("history %s", w.Body.String())
	}

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/workload", ""},
	} {
		w := e.do(c.method, "/api/profiles/"+p.ID+c.path, c.body)
		expect(t, w, 501, "unsupported")
		if !strings.Contains(w.Body.String(), "sqlite") {
			t.Errorf("%s: %s", c.path, w.Body.String())
		}
	}

	w = e.do("POST", "/api/profiles/"+p.ID+"/migrations/analyze", `{"files":[{"name":"1.sql","sql":"ALTER TABLE users ADD COLUMN age INTEGER NOT NULL;"}]}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"engine":"sqlite"`) || !strings.Contains(w.Body.String(), "DBS-SQLITE-NOT-NULL-NO-DEFAULT") {
		t.Errorf("analyze %s", w.Body.String())
	}

	// The statement timeout answers 504, not SQLite's "interrupted".
	slow, _ := json.Marshal(map[string]any{"name": "Shop", "engine": "sqlite", "path": path, "env": "local", "statementTimeoutMs": 200})
	expect(t, e.do("PUT", "/api/profiles/"+p.ID, string(slow)), 200, "")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query",
		`{"sql":"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"}`), 504, "timeout")

	missing, _ := json.Marshal(map[string]any{"name": "Gone", "engine": "sqlite", "path": filepath.Join(t.TempDir(), "gone.db"), "env": "local"})
	w = e.do("POST", "/api/profiles/test", string(missing))
	json.Unmarshal(w.Body.Bytes(), &tr)
	if tr.OK || tr.Error == nil {
		t.Fatalf("missing file tested ok: %s", w.Body.String())
	}
}

// Switching a profile with a stored password to SQLite drops the secret.
func TestSwitchToSQLiteDropsPassword(t *testing.T) {
	e := keyringEnv(t)
	w := e.do("POST", "/api/profiles", `{"name":"Orders","host":"db.example","database":"app","user":"reader","env":"staging","password":"`+testPass+`"}`)
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	path := filepath.Join(t.TempDir(), "x.db")
	body, _ := json.Marshal(map[string]any{"name": "Orders", "engine": "sqlite", "path": path, "env": "staging"})
	w = e.do("PUT", "/api/profiles/"+p.ID, string(body))
	expect(t, w, 200, "")
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.HasPassword {
		t.Fatal("hasPassword kept")
	}
	if pw, ok, _ := e.vault.Get(p.ID); ok || pw != "" {
		t.Fatal("secret kept in the vault")
	}
}

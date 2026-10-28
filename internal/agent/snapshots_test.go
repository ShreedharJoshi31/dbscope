package agent

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/store"
)

func TestSchemaSnapshots(t *testing.T) {
	e := keyringEnv(t)
	path := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"name": "Shop", "engine": "sqlite", "path": path, "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)

	list := func() []store.SnapshotInfo {
		t.Helper()
		w := e.do("GET", "/api/profiles/"+p.ID+"/snapshots", "")
		expect(t, w, 200, "")
		var out []store.SnapshotInfo
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("before any read %+v", got)
	}
	expect(t, e.do("GET", "/api/profiles/"+p.ID+"/schema", ""), 200, "")
	expect(t, e.do("GET", "/api/profiles/"+p.ID+"/schema", ""), 200, "")
	if got := list(); len(got) != 1 || got[0].Relations != 1 {
		t.Fatalf("an unchanged schema saved twice: %+v", got)
	}
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN name TEXT; CREATE TABLE orders (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	expect(t, e.do("GET", "/api/profiles/"+p.ID+"/schema", ""), 200, "")
	got := list()
	if len(got) != 2 || got[0].Relations != 2 {
		t.Fatalf("after a change %+v", got)
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/snapshots/"+itoa(got[1].ID), "")
	expect(t, w, 200, "")
	var old schema.Model
	json.Unmarshal(w.Body.Bytes(), &old)
	if len(old.Relations) != 1 || old.Relations[0].Column("name") != nil {
		t.Errorf("old snapshot %s", w.Body.String())
	}
	expect(t, e.do("GET", "/api/profiles/"+p.ID+"/snapshots/999999", ""), 404, "not_found")
	expect(t, e.do("GET", "/api/profiles/"+p.ID+"/snapshots/x", ""), 400, "validation")
	expect(t, e.do("GET", "/api/profiles/nope/snapshots", ""), 404, "not_found")
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

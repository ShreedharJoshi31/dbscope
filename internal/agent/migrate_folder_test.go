package agent

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/history"
)

func TestMigrationFolder(t *testing.T) {
	e := keyringEnv(t)
	dbPath := filepath.Join(t.TempDir(), "app.db")
	v1 := "CREATE TABLE a (id INTEGER PRIMARY KEY);\n"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Flyway's SQLite history table (flyway-sqlite createMetaDataTable.sql) with V1 applied.
	if _, err := db.Exec(`CREATE TABLE a (id INTEGER PRIMARY KEY);
		CREATE TABLE "flyway_schema_history" ("installed_rank" INT NOT NULL PRIMARY KEY, "version" VARCHAR(50), "description" VARCHAR(200) NOT NULL, "type" VARCHAR(20) NOT NULL, "script" VARCHAR(1000) NOT NULL, "checksum" INT, "installed_by" VARCHAR(100) NOT NULL, "installed_on" TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')), "execution_time" INT NOT NULL, "success" BOOLEAN NOT NULL);
		INSERT INTO flyway_schema_history (installed_rank, version, description, type, script, checksum, installed_by, execution_time, success)
			VALUES (1, '1', 'a', 'SQL', 'V1__a.sql', ?, 'ci', 3, 1)`, history.FlywayChecksum(v1)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	dir := filepath.Join(t.TempDir(), "migrations")
	for name, text := range map[string]string{
		"V1__a.sql":                v1,
		"V2__b.sql":                "ALTER TABLE a ADD COLUMN b TEXT;\n",
		"sub/V10__c.sql":           "DROP TABLE a;\n",
		"README.md":                "not a migration",
		"build.js":                 "console.log(1)",
		".git/V9__hidden.sql":      "DROP TABLE a;\n",
		"node_modules/x/V8__n.sql": "DROP TABLE a;\n",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(map[string]any{"name": "app", "engine": "sqlite", "path": dbPath, "env": "local", "migrationsDir": dir})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	p := decodeBody[conn.Profile](t, w)
	path := "/api/profiles/" + p.ID + "/migrations/analyze-folder"

	type response struct {
		Report  migrate.Report    `json:"report"`
		Sources []migrationSource `json:"sources"`
	}
	names := func(r response) []string {
		var out []string
		for _, s := range r.Sources {
			out = append(out, s.Name)
		}
		return out
	}

	w = e.do("POST", path, `{"pendingOnly":false}`)
	expect(t, w, 200, "")
	all := decodeBody[response](t, w)
	if want := []string{"migrations/build.js", "migrations/sub/V10__c.sql", "migrations/V1__a.sql", "migrations/V2__b.sql"}; !slices.Equal(names(all), want) {
		t.Fatalf("sources %v, want %v", names(all), want)
	}
	if all.Report.Pending != nil || len(all.Report.Statements) != 3 || all.Report.Engine != "sqlite" {
		t.Fatalf("report %+v", all.Report)
	}
	if !slices.ContainsFunc(all.Report.Warnings, func(s string) bool { return strings.Contains(s, "1 JavaScript file was skipped") }) {
		t.Errorf("warnings %q", all.Report.Warnings)
	}

	w = e.do("POST", path, `{"pendingOnly":true,"txMode":"statement","assumeLockTimeout":true}`)
	expect(t, w, 200, "")
	pend := decodeBody[response](t, w)
	if want := []string{"migrations/build.js", "migrations/sub/V10__c.sql", "migrations/V2__b.sql"}; !slices.Equal(names(pend), want) {
		t.Fatalf("pending sources %v, want %v", names(pend), want)
	}
	pd := pend.Report.Pending
	if pd == nil || pd.Tool != "flyway" || pd.Table != "flyway_schema_history" || !slices.Equal(pd.Applied, []string{"migrations/V1__a.sql"}) || len(pd.Changed) != 0 {
		t.Fatalf("pending %+v", pd)
	}
	if len(pend.Report.Statements) != 2 || slices.ContainsFunc(pend.Report.Statements, func(s migrate.Statement) bool { return s.File == "migrations/V1__a.sql" }) {
		t.Fatalf("statements %+v", pend.Report.Statements)
	}
	// The response wraps the report: the existing endpoint's shape is unchanged.
	if !strings.Contains(w.Body.String(), `"pending":{"tool":"flyway","table":"flyway_schema_history","applied":["migrations/V1__a.sql"],"changed":[]}`) {
		t.Errorf("body %s", w.Body.String())
	}

	expect(t, e.do("POST", path, `{"pendingOnly":true,"txMode":"sometimes"}`), 400, "validation")
	expect(t, e.do("POST", path, `{"files":[]}`), 400, "validation")

	body, _ = json.Marshal(map[string]any{"name": "no folder", "engine": "sqlite", "path": dbPath, "env": "local"})
	w = e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	other := decodeBody[conn.Profile](t, w)
	w = e.do("POST", "/api/profiles/"+other.ID+"/migrations/analyze-folder", `{"pendingOnly":true}`)
	expect(t, w, 400, "validation")
	if !strings.Contains(w.Body.String(), "no migrations folder") {
		t.Errorf("%s", w.Body.String())
	}
}

func TestMigrationFolderAllApplied(t *testing.T) {
	e := keyringEnv(t)
	dbPath := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// golang-migrate's SQLite table (database/sqlite3) at version 1, clean.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version uint64,dirty bool);
		INSERT INTO schema_migrations VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1_a.up.sql"), []byte("CREATE TABLE a (id int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"name": "app", "engine": "sqlite", "path": dbPath, "env": "local", "migrationsDir": dir})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	p := decodeBody[conn.Profile](t, w)
	w = e.do("POST", "/api/profiles/"+p.ID+"/migrations/analyze-folder", `{"pendingOnly":true}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"applied":["`+filepath.Base(dir)+`/1_a.up.sql"]`) || !strings.Contains(w.Body.String(), `"sources":[]`) ||
		!strings.Contains(w.Body.String(), "nothing pending") {
		t.Fatalf("%s", w.Body.String())
	}
}

func TestMigrationFolderState(t *testing.T) {
	e := keyringEnv(t)
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("V1__a.sql", "CREATE TABLE a (id INT);\n")
	write("notes.md", "not a migration")
	body, _ := json.Marshal(map[string]any{"name": "app", "engine": "sqlite", "path": filepath.Join(t.TempDir(), "app.db"), "env": "local", "migrationsDir": dir})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	p := decodeBody[conn.Profile](t, w)
	type state struct {
		Fingerprint string `json:"fingerprint"`
		Files       int    `json:"files"`
	}
	read := func() state {
		w := e.do("GET", "/api/profiles/"+p.ID+"/migrations/folder-state", "")
		expect(t, w, 200, "")
		return decodeBody[state](t, w)
	}
	first := read()
	if first.Files != 1 || first.Fingerprint == "" || read() != first {
		t.Fatalf("unstable or wrong: %+v", first)
	}
	write("notes.md", "edited, still not a migration")
	if read() != first {
		t.Fatal("a non-migration file changed the fingerprint")
	}
	write("V1__a.sql", "CREATE TABLE a (id INT, b INT);\n")
	if read().Fingerprint == first.Fingerprint {
		t.Fatal("an edited migration kept the fingerprint")
	}
	write("V2__b.sql", "DROP TABLE a;\n")
	if s := read(); s.Files != 2 {
		t.Fatalf("new file not counted: %+v", s)
	}
}

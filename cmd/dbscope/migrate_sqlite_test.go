package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
)

func TestMigrateCheckSQLite(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	dbPath := filepath.Join(root, "app.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT); INSERT INTO users (email) VALUES ('a')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	schema := write(t, root, "schema.sql", "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT);\n")
	dir := filepath.Join(root, "migrations")
	write(t, dir, "1.sql", "ALTER TABLE users ADD COLUMN age INTEGER NOT NULL;\n")
	write(t, dir, "2.sql", "ALTER TABLE users ADD COLUMN nick TEXT;\n")

	code, out, errb := cli(t, "migrate", "check", dir, "--sqlite", dbPath)
	if code != exitThreshold || !strings.Contains(out, "DBS-SQLITE-NOT-NULL-NO-DEFAULT") || !strings.Contains(out, "against SQLite 3.") ||
		strings.Contains(out, "lock_timeout") {
		t.Fatalf("live file: exit %d\n%s\n%s", code, out, errb)
	}
	// A schema script is a dump base: no sizes, and the same report shape.
	code, out, _ = cli(t, "migrate", "check", filepath.Join(dir, "2.sql"), "--sqlite-schema", schema, "--format", "json")
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitOK || rep.Engine != "sqlite" || rep.Base != "dump" {
		t.Fatalf("schema script: exit %d %v\n%s", code, err, out)
	}
	for name, args := range map[string][]string{
		"both bases":   {"migrate", "check", dir, "--sqlite", dbPath, "--sqlite-schema", schema},
		"with pg flag": {"migrate", "check", dir, "--sqlite", dbPath, "--tx-mode", "file"},
		"with dump":    {"migrate", "check", dir, "--sqlite", dbPath, "--schema-dump", schema},
	} {
		if code, _, errb := cli(t, args...); code != exitUsage || !strings.Contains(errb, "usage:") {
			t.Errorf("%s: exit %d %q", name, code, errb)
		}
	}
	if code, _, errb := cli(t, "migrate", "check", dir, "--sqlite", filepath.Join(root, "missing.db")); code != exitUnavailable {
		t.Errorf("missing file: exit %d %q", code, errb)
	}
}

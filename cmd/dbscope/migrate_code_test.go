package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
)

func TestMigrateCheckCode(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	schema := write(t, root, "schema.sql", "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, nick TEXT);\n")
	write(t, root, "app/user.py", "class User:\n    nick: str\n")
	write(t, root, "app/other.py", "nick = 'unrelated'\n")
	dir := filepath.Join(root, "migrations")
	write(t, dir, "1.sql", "ALTER TABLE users DROP COLUMN nick;\n")

	code, out, errb := cli(t, "migrate", "check", dir, "--sqlite-schema", schema, "--format", "json", "--code", root)
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitThreshold {
		t.Fatalf("exit %d %v\n%s\n%s", code, err, out, errb)
	}
	if len(rep.Usages) != 1 || rep.Usages[0] != (migrate.Usage{Name: "main.users.nick", File: "app/user.py", Line: 2, Text: "nick: str"}) {
		t.Fatalf("usages %+v", rep.Usages)
	}
	f := rep.Statements[0].Findings[len(rep.Statements[0].Findings)-1]
	if f.RuleID != "DBS-STILL-USED" || f.Message != "main.users.nick is still named in 1 place in the project, e.g. app/user.py:2" {
		t.Fatalf("finding %+v", f)
	}

	// The base schema file and the migrations are inputs, not application code: neither is reported.
	if strings.Contains(out, "schema.sql") || strings.Contains(out, `"file":"migrations/1.sql","line":1,"text"`) {
		t.Fatalf("inputs reported:\n%s", out)
	}

	// Without --code nothing is searched; DROP COLUMN still fails the check, as data loss.
	code, out, _ = cli(t, "migrate", "check", dir, "--sqlite-schema", schema, "--format", "json")
	if strings.Contains(out, "DBS-STILL-USED") || strings.Contains(out, `"usages"`) || code != exitThreshold {
		t.Fatalf("searched without --code: exit %d\n%s", code, out)
	}
	if code, _, errb := cli(t, "migrate", "check", dir, "--sqlite-schema", schema, "--code", filepath.Join(root, "missing")); code != exitUsage ||
		!strings.Contains(errb, "--code must be a folder") {
		t.Fatalf("missing --code folder: exit %d %q", code, errb)
	}
}

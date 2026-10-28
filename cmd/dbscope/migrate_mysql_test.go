package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
)

func TestMigrateCheckMySQL(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("DBSCOPE_DSN", "")
	t.Setenv("DBSCOPE_MYSQL_DSN", "")
	dir := filepath.Join(root, "migrations")
	write(t, dir, "1.sql", "ALTER TABLE orders ADD COLUMN note TEXT;\n")
	write(t, dir, "2.sql", "ALTER TABLE orders MODIFY total BIGINT;\n")

	code, out, errb := cli(t, "migrate", "check", dir, "--mysql-version", "8.4", "--format", "json")
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitThreshold || rep.Engine != "mysql" || rep.Base != "none" {
		t.Fatalf("exit %d %v\n%s\n%s", code, err, out, errb)
	}
	if code, out, _ := cli(t, "migrate", "check", filepath.Join(dir, "1.sql"), "--mysql-version", "mariadb-11.8"); code != exitOK || !strings.Contains(out, "against MariaDB 11.8.0") {
		t.Errorf("mariadb: exit %d\n%s", code, out)
	}
	for name, args := range map[string][]string{
		"both":        {"migrate", "check", dir, "--mysql-version", "8.4", "--mysql", "mysql://u@h/d"},
		"bad version": {"migrate", "check", dir, "--mysql-version", "oracle-19"},
		"pg flag":     {"migrate", "check", dir, "--mysql-version", "8.4", "--tx-mode", "file"},
		"bad url":     {"migrate", "check", dir, "--mysql", "postgres://u:hunter2@h/d"},
	} {
		code, _, errb := cli(t, args...)
		if code != exitUsage || !strings.Contains(errb, "usage:") || strings.Contains(errb, "hunter2") {
			t.Errorf("%s: exit %d %q", name, code, errb)
		}
	}
	// An unreachable server: exit 3, and the password is never printed.
	t.Setenv("DBSCOPE_MYSQL_DSN", "mysql://u:hunter2@127.0.0.1:1/app")
	if code, out, errb := cli(t, "migrate", "check", dir); code != exitUnavailable || strings.Contains(out+errb, "hunter2") {
		t.Errorf("unreachable: exit %d %q", code, errb)
	}
}

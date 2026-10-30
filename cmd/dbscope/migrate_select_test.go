package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/history"
	"github.com/dbscope/dbscope/internal/pgtest"
)

func files(rep migrate.Report) []string {
	var out []string
	for _, s := range rep.Statements {
		if !slices.Contains(out, s.File) {
			out = append(out, s.File)
		}
	}
	return out
}

func TestMigrateCheckPendingSQLite(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	dbPath := filepath.Join(root, "app.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// goose's SQLite table (internal/dialects/sqlite3.go) with version 1 applied.
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY);
		CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY AUTOINCREMENT, version_id INTEGER NOT NULL, is_applied INTEGER NOT NULL, tstamp TIMESTAMP DEFAULT (datetime('now')));
		INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := filepath.Join(root, "migrations")
	write(t, dir, "00001_users.sql", "-- +goose Up\nCREATE TABLE users (id INTEGER PRIMARY KEY);\n")
	write(t, dir, "00002_nick.sql", "-- +goose Up\nALTER TABLE users ADD COLUMN nick TEXT;\n")

	code, out, errb := cli(t, "migrate", "check", dir, "--sqlite", dbPath, "--pending", "--format", "json")
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitOK {
		t.Fatalf("exit %d %v\n%s\n%s", code, err, out, errb)
	}
	if p := rep.Pending; p == nil || p.Tool != "goose" || !slices.Equal(p.Applied, []string{"migrations/00001_users.sql"}) {
		t.Fatalf("pending %+v", rep.Pending)
	}
	if got := files(rep); !slices.Equal(got, []string{"migrations/00002_nick.sql"}) {
		t.Fatalf("analyzed %v", got)
	}
	if !strings.Contains(errb, "goose (goose_db_version) has 1 of these files applied; skipped:\n  migrations/00001_users.sql\n") {
		t.Errorf("stderr %q", errb)
	}

	// Without --pending every file is checked.
	_, out, _ = cli(t, "migrate", "check", dir, "--sqlite", dbPath, "--format", "json")
	rep = migrate.Report{}
	json.Unmarshal([]byte(out), &rep)
	if rep.Pending != nil || len(files(rep)) != 2 {
		t.Fatalf("all files: %+v", rep)
	}

	schema := write(t, root, "schema.sql", "CREATE TABLE users (id INTEGER PRIMARY KEY);\n")
	for name, args := range map[string][]string{
		"sqlite schema":  {"migrate", "check", dir, "--sqlite-schema", schema, "--pending"},
		"pg dump":        {"migrate", "check", dir, "--schema-dump", schema, "--pending"},
		"mysql version":  {"migrate", "check", dir, "--mysql-version", "8.4", "--pending"},
		"since bad flag": {"migrate", "check", dir, "--sqlite", dbPath, "--since"},
	} {
		if code, _, errb := cli(t, args...); code != exitUsage || !strings.Contains(errb, "usage:") {
			t.Errorf("%s: exit %d %q", name, code, errb)
		}
	}
}

func TestMigrateCheckPendingPostgres(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	d := srv.NewDB(t)
	v1 := "CREATE TABLE app_users (id bigint PRIMARY KEY);\n"
	// Flyway's PostgreSQL table (createMetaDataTable.sql) with V1 applied and V2 failed.
	d.Exec(t, fmt.Sprintf(`CREATE TABLE app_users (id bigint PRIMARY KEY);
CREATE TABLE "flyway_schema_history" ("installed_rank" INT NOT NULL, "version" VARCHAR(50), "description" VARCHAR(200) NOT NULL, "type" VARCHAR(20) NOT NULL, "script" VARCHAR(1000) NOT NULL, "checksum" INTEGER, "installed_by" VARCHAR(100) NOT NULL, "installed_on" TIMESTAMP NOT NULL DEFAULT now(), "execution_time" INTEGER NOT NULL, "success" BOOLEAN NOT NULL);
ALTER TABLE "flyway_schema_history" ADD CONSTRAINT "flyway_schema_history_pk" PRIMARY KEY ("installed_rank");
INSERT INTO flyway_schema_history (installed_rank, version, description, type, script, checksum, installed_by, execution_time, success)
VALUES (1, '1', 'users', 'SQL', 'V1__users.sql', %d, 'ci', 4, true), (2, '2', 'email', 'SQL', 'V2__email.sql', 7, 'ci', 4, false);`, history.FlywayChecksum(v1)))
	p, password := d.Superuser(t)
	if p.Port == 5432 && (p.Host == "localhost" || p.Host == "127.0.0.1") {
		t.Skip("refusing to connect to localhost:5432")
	}
	dsn := (&url.URL{Scheme: "postgres", User: url.UserPassword(p.User, password), Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		Path: "/" + p.Database, RawQuery: "sslmode=disable"}).String()
	root := t.TempDir()
	t.Chdir(root)
	dir := filepath.Join(root, "db")
	write(t, dir, "V1__users.sql", v1)
	write(t, dir, "V2__email.sql", "ALTER TABLE app_users ADD COLUMN email text;\n")

	code, out, errb := cli(t, "migrate", "check", dir, "--dsn", dsn, "--pending", "--format", "json")
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitOK {
		t.Fatalf("exit %d %v\n%s\n%s", code, err, out, errb)
	}
	if p := rep.Pending; p == nil || p.Tool != "flyway" || !slices.Equal(p.Applied, []string{"db/V1__users.sql"}) || len(p.Changed) != 0 {
		t.Fatalf("pending %+v", rep.Pending)
	}
	if got := files(rep); !slices.Equal(got, []string{"db/V2__email.sql"}) {
		t.Fatalf("analyzed %v", got)
	}
	if strings.Contains(errb, password) {
		t.Fatal("stderr holds the password")
	}
}

func TestMigrateCheckSince(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	schema := write(t, root, "schema.sql", "CREATE TABLE users (id INTEGER PRIMARY KEY);\n")
	dir := filepath.Join(root, "migrations")
	write(t, dir, "1_a.sql", "ALTER TABLE users ADD COLUMN a TEXT;\n")
	write(t, dir, "2_b.sql", "ALTER TABLE users ADD COLUMN b TEXT;\n")

	// Outside a repository --since is an error, said plainly.
	code, _, errb := cli(t, "migrate", "check", dir, "--sqlite-schema", schema, "--since", "HEAD")
	if code != exitUsage || !strings.Contains(errb, "--since needs a git repository") {
		t.Fatalf("no repo: exit %d %q", code, errb)
	}

	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	write(t, dir, "2_b.sql", "ALTER TABLE users ADD COLUMN b INTEGER;\n")
	write(t, dir, "3_c.sql", "ALTER TABLE users ADD COLUMN c TEXT;\n")
	write(t, filepath.Join(root, "other"), "9_z.sql", "ALTER TABLE users ADD COLUMN z TEXT;\n")

	code, out, errb := cli(t, "migrate", "check", dir, "--sqlite-schema", schema, "--since", "HEAD", "--format", "json")
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitOK {
		t.Fatalf("exit %d %v\n%s\n%s", code, err, out, errb)
	}
	if got := files(rep); !slices.Equal(got, []string{"migrations/2_b.sql", "migrations/3_c.sql"}) {
		t.Fatalf("since HEAD analyzed %v", got)
	}

	// A path given relative to a subdirectory resolves the same way.
	t.Chdir(dir)
	_, out, errb = cli(t, "migrate", "check", ".", "--sqlite-schema", schema, "--since", "HEAD", "--format", "json")
	rep = migrate.Report{}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || !slices.Equal(files(rep), []string{"migrations/2_b.sql", "migrations/3_c.sql"}) {
		t.Fatalf("from subdirectory %v %v\n%s", files(rep), err, errb)
	}

	code, _, errb = cli(t, "migrate", "check", ".", "--sqlite-schema", schema, "--since", "no-such-ref")
	if code != exitUsage || !strings.Contains(errb, "--since no-such-ref") {
		t.Fatalf("bad ref: exit %d %q", code, errb)
	}
}

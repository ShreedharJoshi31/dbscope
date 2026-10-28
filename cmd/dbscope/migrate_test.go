package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/report/sarifschema"
	"github.com/dbscope/dbscope/internal/migrate/stats"
	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/sample"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// cli runs a command in-process with an isolated HOME and no DBSCOPE_DSN.
func cli(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DBSCOPE_SECRETS", "file")
	var out, errb bytes.Buffer
	var code int
	switch args[0] {
	case "migrate":
		code = migrateCmd(args[1:], &out, &errb)
	case "stats":
		code = statsCmd(args[1:], &out, &errb)
	default:
		code = run(args)
	}
	return code, out.String(), errb.String()
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fixture writes a schema dump (the sample schema) and a Flyway directory
// into a fake repository, and makes it the working directory so file names
// are repo-relative.
func fixture(t *testing.T) (dump, dir string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	dump = write(t, root, "schema.sql", "--\n-- PostgreSQL database dump\n--\n-- Dumped from database version 17.2\n\n"+sample.SQL)
	dir = filepath.Join(root, "db", "migrations")
	write(t, dir, "V1__safe.sql", "SET lock_timeout = '2s';\nALTER TABLE shop.orders ADD COLUMN note text;\n")
	write(t, dir, "V2__index.sql", "SET lock_timeout = '2s';\nCREATE INDEX orders_note_idx ON shop.orders (note);\n")
	return dump, dir
}

func TestMigrateCheckUsage(t *testing.T) {
	dump, dir := fixture(t)
	t.Setenv("DBSCOPE_DSN", "")
	for name, args := range map[string][]string{
		"no subcommand":  {"migrate"},
		"wrong verb":     {"migrate", "lint", dir},
		"no paths":       {"migrate", "check", "--schema-dump", dump},
		"no base":        {"migrate", "check", dir},
		"bad fail-on":    {"migrate", "check", dir, "--schema-dump", dump, "--fail-on", "severe"},
		"bad format":     {"migrate", "check", dir, "--schema-dump", dump, "--format", "xml"},
		"bad tx-mode":    {"migrate", "check", dir, "--schema-dump", dump, "--tx-mode", "always"},
		"bad version":    {"migrate", "check", dir, "--schema-dump", dump, "--pg-version", "9.6"},
		"dsn and dump":   {"migrate", "check", dir, "--schema-dump", dump, "--dsn", "postgres://h/db"},
		"stats alone":    {"migrate", "check", dir, "--stats", dump, "--dsn", "postgres://h/db"},
		"missing path":   {"migrate", "check", filepath.Join(dir, "nope.sql"), "--schema-dump", dump},
		"unknown flag":   {"migrate", "check", dir, "--schema-dump", dump, "--strict"},
		"stats no dsn":   {"stats", "export"},
		"stats bad verb": {"stats", "import", "--dsn", "postgres://h/db"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, errb := cli(t, args...); code != exitUsage || !strings.Contains(errb, "usage:") {
				t.Fatalf("exit %d, stderr %q", code, errb)
			}
		})
	}
}

func TestMigrateCheckExitCodes(t *testing.T) {
	dump, dir := fixture(t)
	t.Setenv("DBSCOPE_DSN", "")
	v1 := filepath.Join(dir, "V1__safe.sql")

	if code, out, errb := cli(t, "migrate", "check", v1, "--schema-dump", dump); code != exitOK {
		t.Fatalf("safe migration: exit %d\n%s\n%s", code, out, errb)
	}
	// V2's CREATE INDEX on 157 MiB (from the stats-less dump: unknown size, tier 2) is Medium.
	if code, out, _ := cli(t, "migrate", "check", dir, "--schema-dump", dump); code != exitOK || !strings.Contains(out, "MEDIUM") {
		t.Fatalf("default fail-on high: exit %d\n%s", code, out)
	}
	if code, _, _ := cli(t, "migrate", "check", dir, "--schema-dump", dump, "--fail-on", "medium"); code != exitThreshold {
		t.Fatalf("fail-on medium: exit %d", code)
	}
	// Flags after and before positionals both work.
	if code, _, _ := cli(t, "migrate", "check", "--fail-on", "low", dir, "--schema-dump", dump); code != exitThreshold {
		t.Fatalf("fail-on low: exit %d", code)
	}
	// A file-wide transaction makes nothing fail here; CONCURRENTLY would.
	cc := write(t, t.TempDir(), "x.sql", "CREATE INDEX CONCURRENTLY i ON shop.orders (placed_at);")
	if code, out, _ := cli(t, "migrate", "check", cc, "--schema-dump", dump, "--tx-mode", "file", "--fail-on", "critical"); code != exitThreshold ||
		!strings.Contains(out, "DBS-TX-NOT-ALLOWED") {
		t.Fatalf("CONCURRENTLY in a transaction: exit %d\n%s", code, out)
	}

	// Base schema unavailable: exit 3, and the DSN is never echoed.
	if code, _, errb := cli(t, "migrate", "check", dir, "--schema-dump", filepath.Join(dir, "missing.sql")); code != exitUnavailable ||
		!strings.Contains(errb, "base schema unavailable") {
		t.Fatalf("missing dump: exit %d %q", code, errb)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	closed := fmt.Sprintf("postgres://u:hunter2-secret@127.0.0.1:%d/db?sslmode=disable&connect_timeout=2", port)
	for _, args := range [][]string{
		{"migrate", "check", dir, "--dsn", closed},
		{"migrate", "check", dir, "--dsn", "postgres://u:hunter2-secret@[bad"},
		{"stats", "export", "--dsn", closed},
	} {
		code, _, errb := cli(t, args...)
		if code != exitUnavailable || strings.Contains(errb, "hunter2") {
			t.Fatalf("%v: exit %d, stderr %q", args[:2], code, errb)
		}
	}
	// DBSCOPE_DSN is the fallback for --dsn.
	t.Setenv("DBSCOPE_DSN", closed)
	if code, _, errb := cli(t, "migrate", "check", dir); code != exitUnavailable || strings.Contains(errb, "hunter2") {
		t.Fatalf("DBSCOPE_DSN: exit %d %q", code, errb)
	}
}

func TestMigrateCheckFormats(t *testing.T) {
	dump, dir := fixture(t)
	t.Setenv("DBSCOPE_DSN", "")
	write(t, dir, "V3__risky.sql", "ALTER TABLE shop.orders ALTER COLUMN status TYPE text;\nALTER TABLE t ADD CONSTRAINT nn NOT NULL a NOT VALID;\n")
	args := func(format string) []string {
		return []string{"migrate", "check", dir, "--schema-dump", dump, "--format", format, "--pg-version", "18"}
	}

	code, out, _ := cli(t, args("json")...)
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitThreshold {
		t.Fatalf("json: exit %d %v\n%s", code, err, out)
	}
	if rep.PGVersion != 180000 || rep.Base != "dump" || rep.Rating != "critical" || len(rep.Statements) != 6 {
		t.Fatalf("report %+v", rep)
	}
	// Unknown size (dump without stats) is tier 2: rewrite 3*2*2 + Q 1 + T 3 = 16.
	if s := rep.Statements[4]; s.Score != 16 || s.Rating != "critical" || s.SizeBytes != nil {
		t.Fatalf("type change %+v", s)
	}
	// PostgreSQL 18 syntax the 17 grammar rejects is High, not a crash.
	last := rep.Statements[5]
	if last.Findings[0].RuleID != "DBS-PARSE" || last.Rating != "high" || last.File != "db/migrations/V3__risky.sql" || last.Line != 2 {
		t.Fatalf("PG18 syntax: %+v", last)
	}

	code, out, _ = cli(t, args("sarif")...)
	if code != exitThreshold {
		t.Fatalf("sarif exit %d", code)
	}
	if errs := sarifschema.Validate([]byte(out)); len(errs) > 0 {
		t.Fatalf("SARIF invalid:\n%s", strings.Join(errs, "\n"))
	}
	if !strings.Contains(out, `"uri": "db/migrations/V3__risky.sql"`) {
		t.Fatalf("SARIF URIs must be repo-relative:\n%s", out)
	}

	code, out, _ = cli(t, args("github")...)
	if code != exitThreshold || !strings.Contains(out, "::error file=") || !strings.Contains(out, "title=DBS-PARSE") {
		t.Fatalf("github: exit %d\n%s", code, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "::error ") && !strings.HasPrefix(line, "::warning ") && !strings.HasPrefix(line, "::notice ") {
			t.Fatalf("not a workflow command: %q", line)
		}
	}

	code, out, _ = cli(t, args("text")...)
	if code != exitThreshold || !strings.Contains(out, "db/migrations/V3__risky.sql\n") || !strings.Contains(out, "worst rating CRITICAL") {
		t.Fatalf("text: exit %d\n%s", code, out)
	}
}

func TestVersionAndHelpStillWork(t *testing.T) {
	if code, _, _ := cli(t, "version"); code != 0 {
		t.Fatalf("version exit %d", code)
	}
	if code, _, _ := cli(t, "migrate", "check", "-h"); code != exitOK {
		t.Fatalf("help exit %d", code)
	}
}

// TestStatsExportAndLiveCheck runs both commands against PostgreSQL as a
// SELECT-only role. The export must hold sizes but no row data.
func TestStatsExportAndLiveCheck(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	d := srv.NewDB(t)
	const marker = "row-value-must-not-leak-7f3a"
	d.Exec(t, `CREATE SCHEMA app;
CREATE TABLE app.users (id bigint PRIMARY KEY, email text NOT NULL);
CREATE TABLE app.posts (id bigint PRIMARY KEY, user_id bigint REFERENCES app.users (id), body text);
CREATE VIEW app.recent AS SELECT p.id, u.email FROM app.posts p JOIN app.users u ON u.id = p.user_id;
INSERT INTO app.users SELECT g, '`+marker+`' || g FROM generate_series(1, 5000) g;
INSERT INTO app.posts SELECT g, 1 + g % 5000, '`+marker+`' FROM generate_series(1, 5000) g;
ANALYZE;`)
	reader := d.Role("reader")
	const pw = "reader-pass"
	d.Exec(t, fmt.Sprintf(`CREATE ROLE %[1]s LOGIN PASSWORD '%[2]s';
GRANT USAGE ON SCHEMA app TO %[1]s; GRANT SELECT ON ALL TABLES IN SCHEMA app TO %[1]s;
ALTER ROLE %[1]s IN DATABASE %[3]s SET lock_timeout = '3s';`, reader, pw, d.Name))
	prof := d.As(t, reader)
	dsn := (&url.URL{Scheme: "postgres", User: url.UserPassword(reader, pw), Host: net.JoinHostPort(prof.Host, strconv.Itoa(prof.Port)),
		Path: "/" + prof.Database, RawQuery: "sslmode=disable"}).String()
	if prof.Port == 5432 && (prof.Host == "localhost" || prof.Host == "127.0.0.1") {
		t.Skip("refusing to connect to localhost:5432")
	}

	code, out, errb := cli(t, "stats", "export", "--dsn", dsn, "--schemas", "app")
	if code != exitOK {
		t.Fatalf("stats export: exit %d %s", code, errb)
	}
	if strings.Contains(out, marker) || strings.Contains(out, pw) {
		t.Fatal("stats export contains row data or the password")
	}
	f, err := stats.Decode(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if f.ServerVersion/10000 != srv.Major || len(f.Relations) != 2 || f.LockTimeout(reader) != "3s" || f.CurrentRole != reader {
		t.Fatalf("stats %+v", f)
	}
	for _, r := range f.Relations {
		if r.TableBytes <= 0 || r.RowEstimate == nil || *r.RowEstimate != 5000 {
			t.Fatalf("relation %+v", r)
		}
	}
	// Only the documented keys are exported.
	var raw map[string]json.RawMessage
	json.Unmarshal([]byte(out), &raw)
	for k := range raw {
		switch k {
		case "format", "serverVersion", "blockSize", "schemas", "relations", "indexes", "settings", "currentRole", "catalog", "exportedAt",
			"activitySince", "inRecovery", "totalScans", "totalRowsWritten":
		default:
			t.Errorf("unexpected key %q in stats export", k)
		}
	}

	dir := t.TempDir()
	mig := write(t, dir, "001_rename.up.sql", "ALTER TABLE app.users RENAME COLUMN email TO email_address;")
	code, out, errb = cli(t, "migrate", "check", mig, "--dsn", dsn, "--format", "json")
	var rep migrate.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("exit %d %v %s\n%s", code, err, errb, out)
	}
	s := rep.Statements[0]
	// The role's lock_timeout counts, the view depends on the column, so the rename is High.
	if code != exitThreshold || rep.Base != "live" || !s.LockTimeout || s.Rating != "high" {
		t.Fatalf("exit %d statement %+v", code, s)
	}
	if deps := rep.Impact["app.users.email"]; len(deps) == 0 || deps[0].Kind != "view" || deps[0].Certainty != "catalog" {
		t.Fatalf("impact %+v", rep.Impact)
	}

	// The stats file combined with a schema dump gives sizes without a connection.
	statsFile := write(t, dir, "stats.json", func() string { _, o, _ := cli(t, "stats", "export", "--dsn", dsn, "--schemas", "app"); return o }())
	dump := write(t, dir, "schema.sql", `CREATE SCHEMA app;
CREATE TABLE app.users (id bigint PRIMARY KEY, email text NOT NULL);
CREATE TABLE app.posts (id bigint PRIMARY KEY, user_id bigint REFERENCES app.users (id), body text);`)
	idx := write(t, dir, "002_idx.up.sql", "CREATE INDEX posts_body_idx ON app.posts (body);")
	code, out, _ = cli(t, "migrate", "check", idx, "--schema-dump", dump, "--stats", statsFile, "--format", "json")
	rep = migrate.Report{}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if s := rep.Statements[0]; rep.Base != "stats" || s.SizeBytes == nil || *s.SizeBytes <= 0 || !s.LockTimeout || code != exitOK {
		t.Fatalf("exit %d %+v", code, s)
	}
}

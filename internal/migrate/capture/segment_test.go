package capture

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// stream builds events on one connection; a statement prefixed with "+" is
// sent in the same Query as the previous one.
func stream(stmts ...string) []Event {
	var out []Event
	unit := 0
	inTx := false
	for _, s := range stmts {
		if !strings.HasPrefix(s, "+") {
			unit++
		}
		s = strings.TrimPrefix(s, "+")
		e := Event{Conn: 1, Unit: unit, InTx: inTx, SQL: s}
		if strings.HasPrefix(s, "!") {
			e.SQL, e.Error = s[1:], "42P07 already exists"
		}
		switch strings.ToUpper(strings.Fields(e.SQL)[0]) {
		case "BEGIN":
			inTx = true
		case "COMMIT", "ROLLBACK":
			inTx = false
		}
		out = append(out, e)
	}
	for i := range out {
		out[i].Seq = i + 1
	}
	return out
}

func keys(ms []Migration) [][]string {
	var out [][]string
	for _, m := range ms {
		out = append(out, m.Keys)
	}
	return out
}

func TestSegmentDjango(t *testing.T) {
	// Recorded from Django 6.1 with psycopg 3: setup, an atomic migration, a
	// non-atomic one, and one whose record is written after COMMIT because
	// it had deferred SQL.
	res := Segment(stream(
		"SELECT c.relname FROM pg_catalog.pg_class c",
		"BEGIN",
		`CREATE TABLE "django_migrations" ("id" bigint NOT NULL PRIMARY KEY, "app" varchar(255) NOT NULL, "name" varchar(255) NOT NULL, "applied" timestamptz NOT NULL)`,
		"COMMIT",
		"BEGIN",
		`CREATE TABLE "app_user" ("id" bigint NOT NULL PRIMARY KEY)`,
		`INSERT INTO "django_migrations" ("app", "name", "applied") VALUES ('app', '0001_initial', '2026-10-27 18:47:33+00'::timestamptz) RETURNING "django_migrations"."id"`,
		"COMMIT",
		`CREATE INDEX CONCURRENTLY "app_user_email_idx" ON "app_user" ("email")`,
		`INSERT INTO "django_migrations" ("app", "name", "applied") VALUES ('app', '0002_email_index', now())`,
		"BEGIN",
		`ALTER TABLE "app_user" ADD COLUMN "org_id" bigint NULL`,
		`ALTER TABLE "app_user" ADD CONSTRAINT "fk" FOREIGN KEY ("org_id") REFERENCES "org" ("id") DEFERRABLE INITIALLY DEFERRED`,
		"COMMIT",
		`INSERT INTO "django_migrations" ("app", "name", "applied") VALUES ('app', '0003_org', now())`,
		`SELECT "django_migrations"."id" FROM "django_migrations"`,
	))
	want := [][]string{{"app.0001_initial"}, {"app.0002_email_index"}, {"app.0003_org"}}
	if got := keys(res.Migrations); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	if res.Migrations[0].Framework != Django {
		t.Fatalf("framework %q", res.Migrations[0].Framework)
	}
	if got := res.Migrations[0].SQL; len(got) != 3 || got[0] != "BEGIN" || got[2] != "COMMIT" {
		t.Fatalf("0001 SQL %q", got)
	}
	if got := res.Migrations[1].SQL; len(got) != 1 || !strings.Contains(got[0], "CONCURRENTLY") {
		t.Fatalf("0002 SQL %q", got)
	}
	if got := res.Migrations[2].SQL; len(got) != 4 {
		t.Fatalf("0003 SQL %q", got)
	}
	if res.Unrecorded != nil {
		t.Fatalf("unrecorded %v", res.Unrecorded.SQL)
	}
}

func TestSegmentAlembicOneTransaction(t *testing.T) {
	// Alembic's default env.py: one transaction for the run; autocommit_block
	// commits it, runs bare, and opens the next one.
	res := Segment(stream(
		"BEGIN",
		"CREATE TABLE alembic_version (version_num VARCHAR(32) NOT NULL)",
		"CREATE TABLE users (id BIGSERIAL NOT NULL)",
		"INSERT INTO alembic_version (version_num) VALUES ('a1') RETURNING alembic_version.version_num",
		"COMMIT",
		"CREATE INDEX CONCURRENTLY users_email_idx ON users (email)",
		"BEGIN ISOLATION LEVEL READ COMMITTED",
		"UPDATE alembic_version SET version_num='a2' WHERE alembic_version.version_num = 'a1'",
		"SET LOCAL lock_timeout = '2s'",
		"ALTER TABLE users ADD COLUMN name VARCHAR(100)",
		"UPDATE alembic_version SET version_num='a3' WHERE alembic_version.version_num = 'a2'",
		"COMMIT",
	))
	want := [][]string{{"a1"}, {"a2", "a3"}}
	if got := keys(res.Migrations); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	got := res.Migrations[1].SQL
	wantSQL := []string{
		"CREATE INDEX CONCURRENTLY users_email_idx ON users (email)",
		"BEGIN ISOLATION LEVEL READ COMMITTED",
		"SET LOCAL lock_timeout = '2s'",
		"ALTER TABLE users ADD COLUMN name VARCHAR(100)",
		"COMMIT",
	}
	if !slices.Equal(got, wantSQL) {
		t.Fatalf("a2..a3 SQL\n%q\nwant\n%q", got, wantSQL)
	}
}

func TestSegmentDrizzleParameterMarkers(t *testing.T) {
	ev := stream(
		`CREATE SCHEMA IF NOT EXISTS "drizzle"`,
		`CREATE TABLE IF NOT EXISTS "drizzle"."__drizzle_migrations" (id SERIAL PRIMARY KEY, hash text NOT NULL, created_at bigint)`,
		`select id, hash, created_at from "drizzle"."__drizzle_migrations" order by created_at desc limit 1`,
		"begin",
		`CREATE TABLE "users" ("id" bigserial PRIMARY KEY NOT NULL)`,
		`insert into "drizzle"."__drizzle_migrations" ("hash", "created_at") values($1, $2)`,
		`ALTER TABLE "users" ADD COLUMN "name" text`,
		`insert into "drizzle"."__drizzle_migrations" ("hash", "created_at") values($1, $2)`,
		"commit",
	)
	ev[5].Params = []Param{{Value: []byte("9c1f")}, {Value: []byte("1767225601000")}}
	ev[7].Params = []Param{{Value: []byte("35e7")}, {Value: []byte("1767225602000")}}
	res := Segment(ev)
	want := [][]string{{"1767225601000", "1767225602000"}}
	if got := keys(res.Migrations); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	if got := res.Migrations[0].SQL; len(got) != 4 || got[0] != "begin" || got[3] != "commit" {
		t.Fatalf("SQL %q", got)
	}
}

func TestSegmentPrisma(t *testing.T) {
	insert := `INSERT INTO "_prisma_migrations" ("id","checksum","started_at","migration_name") VALUES ($1,$2,$3,$4)`
	finish := `UPDATE "_prisma_migrations" SET "finished_at" = $1 WHERE "id" = $2`
	// Prisma 6 sends a migration.sql as one query; tokio-postgres binds in binary.
	ev := stream(
		"SELECT pg_advisory_lock(72707369)",
		insert,
		`CREATE TABLE "User" ("id" SERIAL NOT NULL)`,
		`+CREATE INDEX "User_email_idx" ON "User"("email")`,
		`UPDATE "_prisma_migrations" SET "applied_steps_count" = ("applied_steps_count" + $1) WHERE "id" = $2`,
		finish,
	)
	bin := func(s string) Param { return Param{Binary: true, Value: []byte(s)} }
	ev[1].Params = []Param{bin("id-1"), bin("abc"), {Binary: true, Value: []byte{0, 2, 0x8b, 0x1f, 0, 0, 0, 1}}, bin("20260101000001_users")}
	ev[4].Params = []Param{{Binary: true, Value: []byte{0, 0, 0, 1}}, bin("id-1")}
	ev[5].Params = []Param{{Binary: true, Value: []byte{0, 2, 0x8b, 0x1f, 0, 0, 0, 2}}, bin("id-1")}
	res := Segment(ev)
	want := [][]string{{"20260101000001_users"}}
	if got := keys(res.Migrations); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	got := res.Migrations[0].SQL
	if len(got) != 4 || !strings.HasSuffix(got[0], "BEGIN") || got[3] != "COMMIT" {
		t.Fatalf("an implicit transaction should be made explicit: %q", got)
	}
}

func TestSegmentRollbackFailureAndUnrecorded(t *testing.T) {
	res := Segment(stream(
		"BEGIN",
		"CREATE TABLE t (id int)",
		"ROLLBACK",
		"!CREATE TABLE t (id int)",
		"SET client_min_messages TO warning",
		"SET lock_timeout = '1s'",
		"ALTER TABLE t ADD COLUMN x int",
	))
	if len(res.Migrations) != 0 || res.RolledBack != 1 || len(res.Failed) != 1 {
		t.Fatalf("migrations %d rolled back %d failed %d", len(res.Migrations), res.RolledBack, len(res.Failed))
	}
	if res.Unrecorded == nil || !slices.Equal(res.Unrecorded.SQL, []string{"SET lock_timeout = '1s'", "ALTER TABLE t ADD COLUMN x int"}) {
		t.Fatalf("unrecorded %+v", res.Unrecorded)
	}
}

func TestRender(t *testing.T) {
	ps := []Param{{Value: []byte("it's")}, {Null: true}, {Binary: true, Value: []byte{1}}, {Value: []byte("10")}}
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"SELECT $1, $2, $3, $4", "SELECT 'it''s', NULL, $3, '10'", false},
		{"SELECT '$1', \"$1\", $1 -- $1\n", "SELECT '$1', \"$1\", 'it''s' -- $1\n", true},
		{"SELECT /* $1 /* $2 */ $3 */ $4", "SELECT /* $1 /* $2 */ $3 */ '10'", true},
		{"SELECT $$ $1 $$, $q$ $2 $q$, $1", "SELECT $$ $1 $$, $q$ $2 $q$, 'it''s'", true},
		{`SELECT E'\' $1', $1`, `SELECT E'\' $1', 'it''s'`, true},
		{"SELECT $9, a$1", "SELECT $9, a$1", true},
	}
	for _, c := range cases {
		got, ok := Render(c.in, ps)
		if got != c.want || ok != c.ok {
			t.Errorf("Render(%q)\n got %q, %v\nwant %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestWithoutChannelBinding(t *testing.T) {
	sasl := append([]byte{0, 0, 0, 10}, "SCRAM-SHA-256-PLUS\x00SCRAM-SHA-256\x00\x00"...)
	if got := string(withoutChannelBinding(sasl)); got != "\x00\x00\x00\x0aSCRAM-SHA-256\x00\x00" {
		t.Fatalf("got %q", got)
	}
	cont := append([]byte{0, 0, 0, 11}, "r=abc"...)
	if got := withoutChannelBinding(cont); string(got) != string(cont) {
		t.Fatalf("SASLContinue changed: %q", got)
	}
}

func TestSegmentTransactionOutcomes(t *testing.T) {
	ev := stream(
		// Django nested atomic() with an IntegrityError caught inside RunPython.
		"BEGIN",
		"SAVEPOINT s1",
		"INSERT INTO t VALUES (1)",
		"ROLLBACK TO SAVEPOINT s1",
		"RELEASE SAVEPOINT s1",
		"SET search_path TO app",
		"SELECT setval('t_id_seq', 10)",
		"SELECT pg_try_advisory_lock(1)",
		"-- ping",
		`INSERT INTO "django_migrations" ("app", "name", "applied") VALUES ('app', '0001_a', now())`,
		"COMMIT",
		// A COMMIT the server answered with ROLLBACK (the transaction had failed).
		"BEGIN",
		"CREATE TABLE gone (id int)",
		`INSERT INTO "django_migrations" ("app", "name", "applied") VALUES ('app', '0002_b', now())`,
		"COMMIT",
	)
	ev[len(ev)-1].Tag = "ROLLBACK"
	res := Segment(ev)
	if got := keys(res.Migrations); !slices.EqualFunc(got, [][]string{{"app.0001_a"}}, slices.Equal) {
		t.Fatalf("keys %v", got)
	}
	want := []string{"BEGIN", "SET search_path TO app", "SELECT setval('t_id_seq', 10)", "COMMIT"}
	if got := res.Migrations[0].SQL; !slices.Equal(got, want) {
		t.Fatalf("SQL %q, want %q", got, want)
	}
	if res.RolledBack != 2 || res.Unrecorded != nil {
		t.Fatalf("rolled back %d, unrecorded %+v", res.RolledBack, res.Unrecorded)
	}
}

func TestSegmentConnectionsDoNotMix(t *testing.T) {
	a := stream("BEGIN", "CREATE TABLE a (id int)", `INSERT INTO "schema_migrations" ("version") VALUES ('1')`, "COMMIT")
	b := stream("BEGIN", "CREATE TABLE b (id int)", "ROLLBACK")
	for i := range b {
		b[i].Conn = 2
	}
	// a's BEGIN, b's BEGIN, a's statements and COMMIT, then b's ROLLBACK.
	ev := []Event{a[0], b[0], a[1], b[1], a[2], a[3], b[2]}
	res := Segment(ev)
	if got := keys(res.Migrations); !slices.EqualFunc(got, [][]string{{"1"}}, slices.Equal) {
		t.Fatalf("keys %v", got)
	}
	if got := res.Migrations[0].SQL; len(got) != 3 || got[1] != "CREATE TABLE a (id int)" {
		t.Fatalf("SQL %q", got)
	}
	if res.RolledBack != 1 || res.Unrecorded != nil {
		t.Fatalf("rolled back %d, unrecorded %+v", res.RolledBack, res.Unrecorded)
	}
}

func TestSegmentDisconnectInsideTransaction(t *testing.T) {
	ev := stream("begin", `CREATE TABLE "users" (id int)`)
	ev = append(ev, Event{Conn: 1, InTx: true, SQL: "ROLLBACK", Tag: "ROLLBACK", Error: "connection closed inside a transaction"})
	res := Segment(ev)
	if res.Unrecorded != nil || len(res.Failed) != 0 || res.RolledBack != 1 {
		t.Fatalf("unrecorded %+v failed %d rolled back %d", res.Unrecorded, len(res.Failed), res.RolledBack)
	}
}

func TestWriteKeepsStatementsApart(t *testing.T) {
	dir := t.TempDir() + "/out"
	ms := []Migration{{Framework: Rails, Keys: []string{"1"}, SQL: []string{"ALTER TABLE x ADD c int -- why", "-- note\nCREATE INDEX i ON x (c);"}}}
	files, err := Write(dir, ms, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(dir + "/" + files[0])
	if !strings.Contains(string(src), "ALTER TABLE x ADD c int;\n") || !strings.Contains(string(src), "-- note\nCREATE INDEX i ON x (c);\n") {
		t.Fatalf("file:\n%s", src)
	}
}

func TestStrayURLVars(t *testing.T) {
	t.Setenv("CACHE_DATABASE_URL", "postgres://prod/cache")
	if err := strayURLVars(nil); err == nil || !strings.Contains(err.Error(), "CACHE_DATABASE_URL") {
		t.Fatalf("want a refusal naming CACHE_DATABASE_URL, got %v", err)
	}
	if err := strayURLVars([]string{"CACHE_DATABASE_URL"}); err != nil {
		t.Fatalf("--url-env should redirect it: %v", err)
	}
	t.Setenv("CACHE_DATABASE_URL", "")
	t.Setenv("DIRECT_URL", "postgres://prod/app")
	if err := strayURLVars(nil); err != nil {
		t.Fatalf("DIRECT_URL is redirected by default: %v", err)
	}
}

func TestSegmentSettingsAloneAreNotAMigration(t *testing.T) {
	// Rails sets search_path back and forth while dumping the schema after migrating.
	res := Segment(stream(
		"BEGIN",
		`ALTER TABLE "users" ADD "name" character varying`,
		`INSERT INTO "schema_migrations" ("version") VALUES ('3') RETURNING "version"`,
		"COMMIT",
		"SET search_path TO public",
		`SELECT set_config('search_path', '"$user", public', false)`,
		"SELECT set_config('application_name', 'x', false)",
	))
	if len(res.Migrations) != 1 || res.Unrecorded != nil {
		t.Fatalf("migrations %d, unrecorded %+v", len(res.Migrations), res.Unrecorded)
	}
}

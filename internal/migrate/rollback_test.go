package migrate

import (
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/pgtest"
)

func downRules(rep *Report) map[string][]string {
	out := map[string][]string{}
	for _, s := range rep.Statements {
		for _, f := range s.Findings {
			if strings.HasPrefix(f.RuleID, "DBS-DOWN-") {
				out[s.File] = append(out[s.File], f.RuleID)
			}
		}
	}
	return out
}

// Each case runs up then down on the real server and checks that
// DBS-DOWN-INCOMPLETE is reported exactly when the schema PostgreSQL ends
// up with differs from the one before the up.
func TestLiveDownRoundTrip(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	const setup = `CREATE TYPE mood AS ENUM ('ok', 'sad');
		CREATE TABLE users (id bigint PRIMARY KEY, email varchar(200) NOT NULL DEFAULT 'none', note text, m mood);
		CREATE TABLE posts (id bigint PRIMARY KEY, user_id bigint REFERENCES users (id) ON DELETE CASCADE, body text);
		CREATE INDEX posts_user_idx ON posts (user_id);
		INSERT INTO users VALUES (1, 'a@b', 'n', 'ok')`
	cases := []struct {
		name, up, down string
		want           []string
		inMessage      string
	}{
		{name: "add column and index undone",
			up:   "ALTER TABLE users ADD COLUMN age int NOT NULL DEFAULT 0; CREATE INDEX users_age_idx ON users (age);",
			down: "DROP INDEX users_age_idx; ALTER TABLE users DROP COLUMN age;"},
		{name: "index left behind",
			up:   "ALTER TABLE users ADD COLUMN age int; CREATE INDEX users_note_idx ON users (note);",
			down: "ALTER TABLE users DROP COLUMN age;", want: []string{"DBS-DOWN-INCOMPLETE"}, inMessage: "public.users has different indexes"},
		{name: "drop column re-added identically",
			up:   "ALTER TABLE users DROP COLUMN email;",
			down: "ALTER TABLE users ADD COLUMN email varchar(200) NOT NULL DEFAULT 'none';", want: []string{"DBS-DOWN-LOSES-DATA"}, inMessage: "column public.users.email"},
		{name: "drop column re-added without its default",
			up:   "ALTER TABLE users DROP COLUMN email;",
			down: "ALTER TABLE users ADD COLUMN email varchar(200);", want: []string{"DBS-DOWN-INCOMPLETE", "DBS-DOWN-LOSES-DATA"}, inMessage: "column email is defined differently"},
		{name: "drop table recreated",
			up: "DROP TABLE posts;",
			down: `CREATE TABLE posts (id bigint PRIMARY KEY, user_id bigint REFERENCES users (id) ON DELETE CASCADE, body text);
				CREATE INDEX posts_user_idx ON posts (user_id);`,
			want: []string{"DBS-DOWN-LOSES-DATA"}, inMessage: "table public.posts"},
		{name: "drop table recreated without its foreign key",
			up:   "DROP TABLE posts;",
			down: "CREATE TABLE posts (id bigint PRIMARY KEY, user_id bigint, body text); CREATE INDEX posts_user_idx ON posts (user_id);",
			want: []string{"DBS-DOWN-INCOMPLETE", "DBS-DOWN-LOSES-DATA"}, inMessage: "public.posts has different indexes, constraints"},
		{name: "rename undone",
			up:   "ALTER TABLE users RENAME COLUMN note TO notes;",
			down: "ALTER TABLE users RENAME COLUMN notes TO note;"},
		{name: "rename not undone",
			up:   "ALTER TABLE users RENAME COLUMN note TO notes; ALTER TABLE posts RENAME TO articles;",
			down: "ALTER TABLE articles RENAME TO posts;", want: []string{"DBS-DOWN-INCOMPLETE"}, inMessage: "column note is still named notes"},
		{name: "new table not dropped",
			up:   "CREATE TABLE audit (id bigint);",
			down: "SELECT 1;", want: []string{"DBS-DOWN-INCOMPLETE"}, inMessage: "public.audit is still there"},
		{name: "enum value cannot be removed",
			up:   "CREATE TYPE color AS ENUM ('red'); ALTER TYPE mood ADD VALUE 'happy';",
			down: "DROP TYPE color;", want: []string{"DBS-DOWN-INCOMPLETE"}, inMessage: "type public.mood has different values"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := srv.NewDB(t)
			d.Exec(t, setup)
			base, err := LiveBase(t.Context(), d.Admin, []string{"public"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := Analyze(t.Context(), base, Request{Files: []ingest.Input{
				{Name: "db/0001_x.up.sql", SQL: c.up}, {Name: "db/0001_x.down.sql", SQL: c.down}}})
			if err != nil {
				t.Fatal(err)
			}
			got := downRules(rep)["db/0001_x.up.sql"]
			if !slices.Equal(got, c.want) {
				t.Fatalf("rules %v, want %v; findings %+v", got, c.want, rep.Statements[0].Findings)
			}
			if c.inMessage != "" && !strings.Contains(findingText(rep), c.inMessage) {
				t.Errorf("no %q in %s", c.inMessage, findingText(rep))
			}
			for _, s := range rep.Statements {
				if s.File != "db/0001_x.up.sql" {
					t.Errorf("statement from %s analyzed as a migration", s.File)
				}
			}

			// Ground truth: the server's schema after up and down.
			d.Exec(t, c.up)
			d.Exec(t, c.down)
			after, err := LiveBase(t.Context(), d.Admin, []string{"public"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			b, a := base.Model.Clone(), after.Model.Clone()
			normalizeModel(b)
			normalizeModel(a)
			st := apply.New(b, nil)
			st.Model = a
			diff := st.Diff()
			changed := len(diff.Added)+len(diff.Dropped)+len(diff.Altered) > 0 || len(enumChanges(b.Enums, a.Enums)) > 0
			if changed != slices.Contains(c.want, "DBS-DOWN-INCOMPLETE") {
				t.Errorf("server schema changed %v (%+v), but rules %v", changed, diff, got)
			}
		})
	}
}

func findingText(rep *Report) string {
	var b strings.Builder
	for _, s := range rep.Statements {
		for _, f := range s.Findings {
			b.WriteString(f.Message + "\n")
		}
	}
	return b.String()
}

// Tools with rollbacks get DBS-DOWN-MISSING for an up without one; tools
// without rollbacks never do. Down sections are not analyzed as migrations.
func TestLiveDownMissingAndSections(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	d := srv.NewDB(t)
	d.Exec(t, `CREATE TABLE users (id bigint PRIMARY KEY, note text)`)
	base, err := LiveBase(t.Context(), d.Admin, []string{"public"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		files []ingest.Input
		want  map[string][]string
		stmts int
	}{
		{name: "golang-migrate", files: []ingest.Input{
			{Name: "1_a.up.sql", SQL: "ALTER TABLE users ADD COLUMN a int;"},
			{Name: "1_a.down.sql", SQL: "ALTER TABLE users DROP COLUMN a;"},
			{Name: "2_b.up.sql", SQL: "ALTER TABLE users ADD COLUMN b int;"}},
			want: map[string][]string{"2_b.up.sql": {"DBS-DOWN-MISSING"}}, stmts: 2},
		{name: "flyway with undo", files: []ingest.Input{
			{Name: "V1__a.sql", SQL: "ALTER TABLE users ADD COLUMN a int;"},
			{Name: "U1.0__a.sql", SQL: "ALTER TABLE users DROP COLUMN a;"},
			{Name: "V2__b.sql", SQL: "ALTER TABLE users ADD COLUMN b int;"}},
			want: map[string][]string{"V2__b.sql": {"DBS-DOWN-MISSING"}}, stmts: 2},
		{name: "flyway without undo", files: []ingest.Input{
			{Name: "V1__a.sql", SQL: "ALTER TABLE users ADD COLUMN a int;"},
			{Name: "V2__b.sql", SQL: "ALTER TABLE users ADD COLUMN b int;"}},
			want: map[string][]string{}, stmts: 2},
		{name: "goose", files: []ingest.Input{
			{Name: "20240101_a.sql", SQL: "-- +goose Up\nALTER TABLE users ADD COLUMN a int;\n\n-- +goose Down\nALTER TABLE users DROP COLUMN a;\n"},
			{Name: "20240102_b.sql", SQL: "-- +goose Up\n-- +goose StatementBegin\nALTER TABLE users ADD COLUMN b int;\n-- +goose StatementEnd\n"},
			{Name: "20240103_c.sql", SQL: "-- +goose Up\nCREATE TABLE c (id int);\n-- +goose Down\nSELECT 1;\n"}},
			want: map[string][]string{"20240102_b.sql": {"DBS-DOWN-MISSING"}, "20240103_c.sql": {"DBS-DOWN-INCOMPLETE"}}, stmts: 3},
		{name: "dbmate", files: []ingest.Input{
			{Name: "20240101_a.sql", SQL: "-- migrate:up\nALTER TABLE users DROP COLUMN note;\n\n-- migrate:down\nALTER TABLE users ADD COLUMN note text;\n"},
			{Name: "20240102_b.sql", SQL: "-- migrate:up transaction:false\nCREATE INDEX CONCURRENTLY users_note ON users (id);\n"}},
			want: map[string][]string{"20240101_a.sql": {"DBS-DOWN-LOSES-DATA"}, "20240102_b.sql": {"DBS-DOWN-MISSING"}}, stmts: 2},
		{name: "plain and prisma", files: []ingest.Input{
			{Name: "001_a.sql", SQL: "ALTER TABLE users ADD COLUMN a int;"},
			{Name: "prisma/migrations/2024_x/migration.sql", SQL: "ALTER TABLE users ADD COLUMN b int;"}},
			want: map[string][]string{}, stmts: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep, err := Analyze(t.Context(), base, Request{Files: c.files, KeepOrder: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := downRules(rep); len(got) != len(c.want) || !mapsEqual(got, c.want) {
				t.Errorf("rules %v, want %v", got, c.want)
			}
			if len(rep.Statements) != c.stmts {
				t.Errorf("%d statements, want %d (down migrations must not be analyzed): %+v", len(rep.Statements), c.stmts, rep.Statements)
			}
		})
	}
}

func mapsEqual(a, b map[string][]string) bool {
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

package sqlite

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

func downRules(rep *migrate.Report, file string) []string {
	var out []string
	for _, s := range rep.Statements {
		if s.File != file {
			continue
		}
		for _, f := range s.Findings {
			if strings.HasPrefix(f.RuleID, "DBS-DOWN-") {
				out = append(out, f.RuleID+"/"+string(f.Rating))
			}
		}
	}
	return out
}

// Each case reads its base from a real database file, and then runs up
// and down on that file to confirm DBS-DOWN-INCOMPLETE matches what SQLite
// leaves behind.
func TestDownRoundTrip(t *testing.T) {
	const setup = `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, name TEXT);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users ON DELETE CASCADE, total REAL);
		CREATE INDEX orders_user ON orders (user_id);
		INSERT INTO users VALUES (1, 'a@b', 'a')`
	cases := []struct {
		name, up, down string
		want           []string
		inMessage      string
	}{
		{name: "add column undone",
			up:   "ALTER TABLE users ADD COLUMN age INTEGER; CREATE INDEX users_age ON users (age);",
			down: "DROP INDEX users_age; ALTER TABLE users DROP COLUMN age;"},
		{name: "index left behind",
			up:   "ALTER TABLE users ADD COLUMN age INTEGER; CREATE INDEX users_name ON users (name);",
			down: "ALTER TABLE users DROP COLUMN age;", want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "main.users has different indexes"},
		{name: "down fails",
			up:   "CREATE INDEX users_name ON users (name);",
			down: "DROP INDEX users_nam;", want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "no such index: users_nam"},
		{name: "drop column re-added",
			up:   "ALTER TABLE users DROP COLUMN name;",
			down: "ALTER TABLE users ADD COLUMN name TEXT;", want: []string{"DBS-DOWN-LOSES-DATA/medium"}, inMessage: "column users.name"},
		{name: "rebuild drops a column",
			up: `CREATE TABLE orders_new (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users ON DELETE CASCADE);
				INSERT INTO orders_new SELECT id, user_id FROM orders; DROP TABLE orders; ALTER TABLE orders_new RENAME TO orders;
				CREATE INDEX orders_user ON orders (user_id);`,
			down: "ALTER TABLE orders ADD COLUMN total REAL;", want: []string{"DBS-DOWN-LOSES-DATA/medium"}, inMessage: "drops column orders.total;"},
		{name: "rename is not a drop",
			up:   "ALTER TABLE orders RENAME TO purchases; ALTER TABLE users RENAME COLUMN name TO full_name;",
			down: "ALTER TABLE purchases RENAME TO orders; ALTER TABLE users RENAME COLUMN full_name TO name;"},
		{name: "drop table recreated without its index",
			up:   "DROP TABLE orders;",
			down: "CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users ON DELETE CASCADE, total REAL);",
			want: []string{"DBS-DOWN-INCOMPLETE/high", "DBS-DOWN-LOSES-DATA/medium"}, inMessage: "table orders"},
		{name: "new table not dropped",
			up: "CREATE TABLE audit (id INTEGER);", down: "", want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "main.audit is still there"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(setup); err != nil {
				t.Fatal(err)
			}
			base, err := ReadBase(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := Analyze(t.Context(), base, []ingest.Input{
				{Name: "1_x.up.sql", SQL: c.up}, {Name: "1_x.down.sql", SQL: c.down}})
			if err != nil {
				t.Fatal(err)
			}
			got := downRules(rep, "1_x.up.sql")
			if !slices.Equal(got, c.want) {
				t.Fatalf("rules %v, want %v; %+v", got, c.want, rep.Statements)
			}
			var msgs []string
			for _, s := range rep.Statements {
				if s.File != "1_x.up.sql" {
					t.Errorf("%s analyzed as a migration", s.File)
				}
				for _, f := range s.Findings {
					msgs = append(msgs, f.Message)
				}
			}
			if c.inMessage != "" && !strings.Contains(strings.Join(msgs, "\n"), c.inMessage) {
				t.Errorf("no %q in %q", c.inMessage, msgs)
			}

			before, err := Introspect(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(c.up); err != nil {
				t.Fatal(err)
			}
			_, downErr := db.Exec(c.down)
			after, err := Introspect(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			d := diff(before, after)
			changed := downErr != nil || len(d.Added)+len(d.Dropped)+len(d.Altered) > 0
			if changed != slices.Contains(c.want, "DBS-DOWN-INCOMPLETE/high") {
				t.Errorf("file changed %v (%v, %+v), rules %v", changed, downErr, d, got)
			}
		})
	}
}

func TestDownMissingAndSections(t *testing.T) {
	cases := []struct {
		name  string
		files []ingest.Input
		want  map[string][]string
		stmts int
	}{
		{name: "golang-migrate", files: []ingest.Input{
			{Name: "1_a.up.sql", SQL: "ALTER TABLE users ADD COLUMN a TEXT;"},
			{Name: "1_a.down.sql", SQL: "ALTER TABLE users DROP COLUMN a;"},
			{Name: "2_b.up.sql", SQL: "ALTER TABLE users ADD COLUMN b TEXT;"}},
			want: map[string][]string{"2_b.up.sql": {"DBS-DOWN-MISSING/medium"}}, stmts: 2},
		{name: "goose", files: []ingest.Input{
			{Name: "20240101_a.sql", SQL: "-- +goose Up\nALTER TABLE users ADD COLUMN a TEXT;\n-- +goose Down\nALTER TABLE users DROP COLUMN a;\n"},
			{Name: "20240102_b.sql", SQL: "-- +goose Up\nALTER TABLE users ADD COLUMN b TEXT;\n"}},
			want: map[string][]string{"20240102_b.sql": {"DBS-DOWN-MISSING/medium"}}, stmts: 2},
		{name: "dbmate", files: []ingest.Input{
			{Name: "20240101_a.sql", SQL: "-- migrate:up\nCREATE TABLE t (id INTEGER);\n-- migrate:down\n"}},
			want: map[string][]string{"20240101_a.sql": {"DBS-DOWN-INCOMPLETE/high"}}, stmts: 1},
		{name: "plain files", files: []ingest.Input{
			{Name: "001_a.sql", SQL: "ALTER TABLE users ADD COLUMN a TEXT;"},
			{Name: "002_b.sql", SQL: "ALTER TABLE users ADD COLUMN b TEXT;"}},
			want: map[string][]string{}, stmts: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep, err := Analyze(t.Context(), analyzeBase(), c.files)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string][]string{}
			for _, f := range c.files {
				if r := downRules(rep, f.Name); len(r) > 0 {
					got[f.Name] = r
				}
			}
			if len(got) != len(c.want) {
				t.Fatalf("rules %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if !slices.Equal(got[k], v) {
					t.Errorf("%s: %v, want %v", k, got[k], v)
				}
			}
			if len(rep.Statements) != c.stmts {
				t.Errorf("%d statements, want %d", len(rep.Statements), c.stmts)
			}
		})
	}
}

package mysql

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
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

// shape is what the rollback check compares: tables, columns with type and
// nullability, and index names.
func shape(t *testing.T, db *sql.DB) []string {
	t.Helper()
	m, err := Introspect(context.Background(), db, nil, "app")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range m.Relations {
		out = append(out, "table "+r.Name)
		for _, c := range r.Columns {
			out = append(out, "column "+r.Name+"."+c.Name+" "+columnType(c.Type)+" "+map[bool]string{true: "null", false: "not null"}[c.Nullable])
		}
		for _, i := range r.Indexes {
			out = append(out, "index "+r.Name+"."+i.Name)
		}
	}
	slices.Sort(out)
	return out
}

func execAll(t *testing.T, db *sql.DB, script string) {
	t.Helper()
	for _, p := range Split(script) {
		if _, err := db.Exec(p.SQL); err != nil {
			t.Fatalf("%s: %v", p.SQL, err)
		}
	}
}

// Each case runs up and down on the real server too, and checks that
// DBS-DOWN-INCOMPLETE is reported exactly when the tables, columns (type
// and nullability) and index names differ afterwards.
func TestDownRoundTrip(t *testing.T) {
	const setup = `DROP TABLE IF EXISTS posts, users, audit, people;
		CREATE TABLE users (id INT PRIMARY KEY, email VARCHAR(200) NOT NULL, note TEXT, age INT);
		CREATE TABLE posts (id BIGINT PRIMARY KEY, user_id INT, body TEXT, KEY posts_user (user_id));
		INSERT INTO users VALUES (1, 'a@b', 'n', 3)`
	cases := []struct {
		name, up, down string
		want           []string
		inMessage      string
	}{
		{name: "add column and index undone",
			up:   "ALTER TABLE users ADD COLUMN score INT NOT NULL DEFAULT 0, ADD INDEX users_score (score);",
			down: "ALTER TABLE users DROP INDEX users_score, DROP COLUMN score;"},
		{name: "index left behind",
			up:   "CREATE INDEX users_email ON users (email); ALTER TABLE users ADD COLUMN x INT;",
			down: "ALTER TABLE users DROP COLUMN x;", want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "index users_email on app.users is still there"},
		{name: "modify reverted",
			up:   "ALTER TABLE users MODIFY age BIGINT NOT NULL;",
			down: "ALTER TABLE users MODIFY COLUMN age INT;"},
		{name: "modify not reverted",
			up:   "ALTER TABLE users MODIFY age BIGINT NOT NULL;",
			down: "ALTER TABLE users MODIFY age BIGINT;", want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "column app.users.age is defined differently"},
		{name: "drop column re-added",
			up:   "ALTER TABLE users DROP COLUMN note;",
			down: "ALTER TABLE users ADD COLUMN note TEXT;", want: []string{"DBS-DOWN-LOSES-DATA/medium"}, inMessage: "drops column app.users.note"},
		{name: "drop column re-added as not null",
			up:   "ALTER TABLE users DROP COLUMN note;",
			down: "ALTER TABLE users ADD COLUMN note VARCHAR(10) NOT NULL DEFAULT '';", want: []string{"DBS-DOWN-INCOMPLETE/high", "DBS-DOWN-LOSES-DATA/medium"}},
		{name: "rename undone",
			up:   "ALTER TABLE users RENAME COLUMN note TO notes; RENAME TABLE posts TO articles;",
			down: "RENAME TABLE articles TO posts; ALTER TABLE users CHANGE notes note TEXT;"},
		{name: "rename not undone",
			up:   "ALTER TABLE users RENAME COLUMN note TO notes;",
			down: "SELECT 1;", want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "column app.users.note is not recreated"},
		{name: "drop table recreated",
			up:   "DROP TABLE posts;",
			down: "CREATE TABLE posts (id BIGINT PRIMARY KEY, user_id INT, body TEXT, KEY posts_user (user_id));",
			want: []string{"DBS-DOWN-LOSES-DATA/medium"}, inMessage: "drops table app.posts"},
		{name: "drop table recreated differently",
			up:   "DROP TABLE posts;",
			down: "CREATE TABLE posts (id INT PRIMARY KEY, user_id INT, KEY posts_user (user_id));",
			want: []string{"DBS-DOWN-INCOMPLETE/high", "DBS-DOWN-LOSES-DATA/medium"}, inMessage: "table app.posts is recreated with different columns"},
		{name: "new table not dropped",
			up: "CREATE TABLE audit (id INT);", down: "", want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "table app.audit is still there"},
		{name: "new table dropped",
			up: "CREATE TABLE audit (id INT); ALTER TABLE audit ADD COLUMN at DATETIME;", down: "DROP TABLE audit;"},
	}
	for _, flavor := range []string{"mysql", "mariadb"} {
		t.Run(flavor, func(t *testing.T) {
			p := mytest.Server(t, flavor)
			db, err := Open(p, mytest.Password, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					execAll(t, db, setup)
					base, err := ReadBase(context.Background(), db, nil, "app")
					if err != nil {
						t.Fatal(err)
					}
					rep := Analyze(base, []ingest.Input{{Name: "1_x.up.sql", SQL: c.up}, {Name: "1_x.down.sql", SQL: c.down}})
					got := downRules(rep, "1_x.up.sql")
					if !slices.Equal(got, c.want) {
						t.Fatalf("rules %v, want %v; %+v", got, c.want, rep.Statements[0].Findings)
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

					before := shape(t, db)
					execAll(t, db, c.up)
					execAll(t, db, c.down)
					after := shape(t, db)
					if changed := !slices.Equal(before, after); changed != slices.Contains(c.want, "DBS-DOWN-INCOMPLETE/high") {
						t.Errorf("server schema changed %v (%v -> %v), rules %v", changed, before, after, got)
					}
				})
			}
		})
	}
}

func TestDownMissing(t *testing.T) {
	base := &Base{Server: Server{Flavor: "mysql", Version: "8.4.0", Num: 80400}, Tables: map[string]*TableInfo{}, Source: "none"}
	cases := []struct {
		name  string
		files []ingest.Input
		want  map[string][]string
	}{
		{"golang-migrate", []ingest.Input{{Name: "1_a.up.sql", SQL: "ALTER TABLE t ADD COLUMN a INT;"}, {Name: "1_a.down.sql", SQL: "ALTER TABLE t DROP COLUMN a;"},
			{Name: "2_b.up.sql", SQL: "ALTER TABLE t ADD COLUMN b INT;"}}, map[string][]string{"2_b.up.sql": {"DBS-DOWN-MISSING/medium"}}},
		{"flyway with undo", []ingest.Input{{Name: "V1__a.sql", SQL: "ALTER TABLE t ADD COLUMN a INT;"}, {Name: "U1__a.sql", SQL: "ALTER TABLE t DROP COLUMN a;"},
			{Name: "V2__b.sql", SQL: "ALTER TABLE t ADD COLUMN b INT;"}}, map[string][]string{"V2__b.sql": {"DBS-DOWN-MISSING/medium"}}},
		{"flyway without undo", []ingest.Input{{Name: "V1__a.sql", SQL: "ALTER TABLE t ADD COLUMN a INT;"}}, map[string][]string{}},
		{"dbmate", []ingest.Input{{Name: "20240101_a.sql", SQL: "-- migrate:up\nALTER TABLE t ADD COLUMN a INT;\n-- migrate:down\nALTER TABLE t DROP COLUMN a;\n"}}, map[string][]string{}},
		{"plain", []ingest.Input{{Name: "001.sql", SQL: "ALTER TABLE t ADD COLUMN a INT;"}}, map[string][]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := Analyze(base, c.files)
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
			if len(rep.Statements) != len(c.files)-strings.Count(strings.Join(names(c.files), " "), "down")-strings.Count(strings.Join(names(c.files), " "), "U1") {
				t.Errorf("%d statements: down migrations analyzed", len(rep.Statements))
			}
		})
	}
}

func names(in []ingest.Input) []string {
	var out []string
	for _, f := range in {
		out = append(out, f.Name)
	}
	return out
}

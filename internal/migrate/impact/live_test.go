package impact

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

const fixtureSQL = `
CREATE SCHEMA app;
CREATE TABLE app.users (id bigint PRIMARY KEY, email text NOT NULL, name text,
    email_lower text GENERATED ALWAYS AS (lower(email)) STORED);
CREATE INDEX users_email_idx ON app.users (email);
CREATE INDEX users_lower_idx ON app.users (lower(name));
CREATE TABLE app.posts (id bigint PRIMARY KEY, user_id bigint REFERENCES app.users (id), body text);
CREATE VIEW app.user_emails AS SELECT id, email FROM app.users;
CREATE VIEW app.user_emails_2 AS SELECT email FROM app.user_emails;
CREATE MATERIALIZED VIEW app.post_counts AS SELECT user_id, count(*) FROM app.posts GROUP BY user_id;
CREATE FUNCTION app.email_of(bigint) RETURNS text LANGUAGE sql STABLE BEGIN ATOMIC SELECT email FROM app.users WHERE id = $1; END;
CREATE FUNCTION app.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.name := trim(NEW.name); RETURN NEW; END $$;
CREATE FUNCTION app.legacy_lookup(text) RETURNS bigint LANGUAGE plpgsql AS $$ BEGIN RETURN (SELECT id FROM app.users WHERE email = $1); END $$;
CREATE TRIGGER users_touch BEFORE UPDATE OF name ON app.users FOR EACH ROW EXECUTE FUNCTION app.touch();
ALTER TABLE app.users ENABLE ROW LEVEL SECURITY;
CREATE POLICY users_self ON app.users USING (email = current_user);
`

func q(s, n string) schema.QualifiedName { return schema.QualifiedName{Schema: s, Name: n} }

func has(items []Item, kind, name, certainty string) bool {
	return slices.ContainsFunc(items, func(it Item) bool {
		return it.Kind == kind && it.Name == name && (certainty == "" || it.Certainty == certainty)
	})
}

func TestLiveImpactSelectOnlyRole(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "14", "17+"+pgtest.PGStatStatements, "18") {
		t.Run(srv.String(), func(t *testing.T) {
			d := srv.NewDB(t)
			d.Exec(t, fixtureSQL)
			reader := d.Role("reader")
			d.Exec(t, fmt.Sprintf(`CREATE ROLE %[1]s LOGIN PASSWORD 'r';
GRANT USAGE ON SCHEMA app TO %[1]s; GRANT SELECT ON ALL TABLES IN SCHEMA app TO %[1]s;`, reader))
			pgss := strings.HasSuffix(srv.Name, pgtest.PGStatStatements)
			if pgss {
				d.Exec(t, "CREATE EXTENSION pg_stat_statements")
				d.Exec(t, "SELECT pg_stat_statements_reset()")
				for _, s := range []string{
					"SELECT email FROM app.users WHERE id = 1",
					"SELECT u.name FROM app.users u JOIN app.posts p ON p.user_id = u.id",
					"SELECT count(*) FROM app.posts",
				} {
					d.Exec(t, s)
				}
			}
			c := d.Connect(t, reader, "r")
			// The reader cannot write: the impact queries must be pure SELECTs.
			if _, err := c.Exec(context.Background(), "CREATE TABLE app.nope (id int)"); err == nil {
				t.Fatal("reader can create tables; the test role is not SELECT-only")
			}
			live := &Live{DB: c, Timeout: 10 * time.Second, StatStatements: true}
			targets := []Target{{Relation: q("app", "users")}, {Relation: q("app", "users"), Column: "email"},
				{Relation: q("app", "users"), Column: "name"}, {Relation: q("app", "missing")}}
			res, warnings, err := live.Impact(context.Background(), targets)
			if err != nil {
				t.Fatal(err)
			}
			users, email, name := res["app.users"], res["app.users.email"], res["app.users.name"]
			for _, w := range []struct {
				items                []Item
				kind, obj, certainty string
			}{
				{users, "fk_child", "app.posts (posts_user_id_fkey)", Catalog},
				{users, "view", "app.user_emails", Catalog},
				{users, "view", "app.user_emails_2", Catalog},
				{users, "function", "app.email_of(bigint)", Catalog},
				{users, "function", "app.legacy_lookup(text)", Heuristic},
				{users, "trigger", "users_touch ON app.users", Catalog},
				{users, "policy", "users_self ON app.users", Catalog},
				{email, "view", "app.user_emails", Catalog},
				{email, "view", "app.user_emails_2", Catalog},
				{email, "function", "app.email_of(bigint)", Catalog},
				{email, "function", "app.legacy_lookup(text)", Heuristic},
				{email, "policy", "users_self ON app.users", Catalog},
				{email, "generated_column", "app.users.email_lower", Catalog},
				{email, "index", "app.users_email_idx", Catalog},
				{name, "trigger", "users_touch ON app.users", Catalog},
				{name, "index", "app.users_lower_idx", Catalog},
			} {
				if !has(w.items, w.kind, w.obj, w.certainty) {
					t.Errorf("missing %s %s (%s) in %+v", w.kind, w.obj, w.certainty, w.items)
				}
			}
			if has(name, "view", "app.user_emails", "") || has(name, "index", "app.users_email_idx", "") || has(email, "trigger", "users_touch ON app.users", Catalog) {
				t.Errorf("column-level impact too broad: name %+v email %+v", name, email)
			}
			if has(users, "fk_child", "app.users (posts_user_id_fkey)", "") {
				t.Error("self reference")
			}
			if _, ok := res["app.missing"]; ok {
				t.Error("unknown relation has dependents")
			}
			if pgss {
				// Without pg_read_all_stats the reader sees no other role's query texts.
				if has(users, "query", "SELECT email FROM app.users WHERE id = $1", "") {
					t.Error("reader saw another role's query text")
				}
				d.Exec(t, "GRANT pg_read_all_stats TO "+reader)
				c2 := d.Connect(t, reader, "r")
				res, warnings, err = (&Live{DB: c2, Timeout: 10 * time.Second, StatStatements: true}).Impact(context.Background(), targets)
				if err != nil || len(warnings) > 0 {
					t.Fatalf("%v %v", err, warnings)
				}
				if !has(res["app.users"], "query", "SELECT email FROM app.users WHERE id = $1", Heuristic) ||
					!has(res["app.users.email"], "query", "SELECT email FROM app.users WHERE id = $1", Heuristic) ||
					has(res["app.users.email"], "query", "SELECT count(*) FROM app.posts", "") ||
					!has(res["app.users.name"], "query", "SELECT u.name FROM app.users u JOIN app.posts p ON p.user_id = u.id", Heuristic) {
					t.Fatalf("query matches %+v", res)
				}
			} else if len(warnings) != 1 || !strings.Contains(warnings[0], "pg_stat_statements is not installed") {
				t.Fatalf("warnings %v", warnings)
			}
		})
	}
}

func TestPGStatStatementsPermissionDenied(t *testing.T) {
	srv := pgtest.Servers(t, "17+"+pgtest.PGStatStatements)[0]
	if !strings.HasSuffix(srv.Name, pgtest.PGStatStatements) {
		t.Skip("needs a container with pg_stat_statements preloaded")
	}
	d := srv.NewDB(t)
	d.Exec(t, fixtureSQL+"CREATE EXTENSION pg_stat_statements; REVOKE ALL ON pg_stat_statements FROM PUBLIC;")
	reader := d.Role("reader")
	d.Exec(t, fmt.Sprintf("CREATE ROLE %[1]s LOGIN PASSWORD 'r'; GRANT USAGE ON SCHEMA app TO %[1]s; GRANT SELECT ON ALL TABLES IN SCHEMA app TO %[1]s;", reader))
	c := d.Connect(t, reader, "r")
	res, warnings, err := (&Live{DB: c, Timeout: 10 * time.Second, StatStatements: true}).Impact(context.Background(), []Target{{Relation: q("app", "users")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "42501") || len(res["app.users"]) == 0 {
		t.Fatalf("warnings %v result %v", warnings, res)
	}
}

func TestFromModel(t *testing.T) {
	m := &schema.Model{Relations: []schema.Relation{
		{Schema: "a", Name: "users", Columns: []schema.Column{{Name: "id"}, {Name: "email"}, {Name: "e2", Generated: "lower(email)"}},
			Indexes: []schema.Index{{Name: "users_email_idx", Columns: []string{"email"}}}},
		{Schema: "a", Name: "posts", ForeignKeys: []schema.ForeignKey{{Name: "p_fk", Columns: []string{"user_id"}, Ref: q("a", "users"), RefCols: []string{"id"}},
			{Name: "guess", Ref: q("a", "users"), RefCols: []string{"id"}, Inferred: true}}},
		{Schema: "a", Name: "v", Kind: schema.KindView, DependsOn: []schema.QualifiedName{q("a", "users")}},
		{Schema: "a", Name: "mv", Kind: schema.KindMatView, DependsOn: []schema.QualifiedName{q("a", "v")}},
	}}
	res := FromModel(m, []Target{{Relation: q("a", "users")}, {Relation: q("a", "users"), Column: "email"}, {Relation: q("a", "users"), Column: "id"}})
	users := res["a.users"]
	if !has(users, "fk_child", "a.posts (p_fk)", Catalog) || !has(users, "view", "a.v", Catalog) || !has(users, "matview", "a.mv", Catalog) || len(users) != 3 {
		t.Fatalf("users %+v", users)
	}
	email := res["a.users.email"]
	if !has(email, "index", "a.users_email_idx", Catalog) || !has(email, "generated_column", "a.users.e2", Heuristic) ||
		!has(email, "view", "a.v", Heuristic) || has(email, "fk_child", "a.posts (p_fk)", "") {
		t.Fatalf("email %+v", email)
	}
	if !has(res["a.users.id"], "fk_child", "a.posts (p_fk)", Catalog) {
		t.Fatalf("id %+v", res["a.users.id"])
	}
}

func TestMatchQueries(t *testing.T) {
	out := Result{}
	var texts []QueryText
	for i, s := range []string{"SELECT email FROM users", "SELECT 1", "not sql at all", "UPDATE other.users SET email = $1",
		"WITH x AS (SELECT id FROM app.users) SELECT * FROM x"} {
		texts = append(texts, QueryText{Text: s, Calls: int64(100 * (i + 1))})
	}
	MatchQueries(texts, []Target{{Relation: q("app", "users"), Column: "email"}, {Relation: q("app", "users")}}, out)
	if got := out["app.users.email"]; len(got) != 1 || got[0].Calls != 100 {
		t.Fatalf("email matches %+v", got)
	}
	if got := len(out["app.users"]); got != 2 {
		t.Fatalf("relation matches %+v", out["app.users"])
	}
}

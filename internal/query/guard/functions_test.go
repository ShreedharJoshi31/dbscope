package guard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// TestAllowFunctions runs the function rule against the live catalogs of
// PostgreSQL 14 and 18.
func TestAllowFunctions(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "14", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			d := srv.NewDB(t)
			d.Exec(t, `CREATE EXTENSION pg_trgm;
				CREATE SCHEMA app;
				CREATE FUNCTION app.stable_fn() RETURNS int LANGUAGE plpgsql STABLE AS $$BEGIN RETURN 1; END$$;
				CREATE FUNCTION app.lower(int) RETURNS int LANGUAGE sql VOLATILE AS 'SELECT 1';
				CREATE FUNCTION app.random() RETURNS float8 LANGUAGE sql VOLATILE AS 'SELECT 4';`)
			cases := []struct {
				sql    string
				path   string // search_path for the transaction
				denied string // substring of the rejection, "" = allowed
			}{
				{sql: "SELECT pg_sleep(1)", denied: "pg_sleep is not allowed: pg_catalog.pg_sleep(double precision) is VOLATILE"},
				{sql: "SELECT pg_catalog.pg_sleep(1)", denied: "pg_catalog.pg_sleep is not allowed"},
				{sql: "SELECT txid_current()", denied: "txid_current is not allowed: it assigns a transaction ID"},
				{sql: "SELECT pg_current_xact_id()", denied: "assigns a transaction ID"},
				{sql: "SELECT table_to_xml('pg_class', true, false, '')", denied: "returns a whole table"},
				{sql: "SELECT * FROM pg_read_file('/etc/passwd')", denied: "pg_read_file is not allowed"},
				{sql: "SELECT * FROM ROWS FROM (generate_series(1, 2), pg_ls_dir('.'))", denied: "pg_ls_dir is not allowed"},
				{sql: "SELECT nextval('s')", denied: "nextval is not allowed"},
				{sql: "SELECT set_config('a.b', 'c', false)", denied: "set_config is not allowed"},
				{sql: "SELECT now(), lower('A'), count(*), clock_timestamp(), gen_random_uuid()"},
				{sql: "SELECT random()"},
				{sql: "SELECT * FROM generate_series(1, 3), LATERAL unnest(ARRAY[1])"},
				{sql: "SELECT similarity('word', 'two words')"},
				{sql: "SELECT public.similarity('word', 'two words')"},
				{sql: "SELECT app.stable_fn()", denied: "app.stable_fn() is a user-defined function"},
				{sql: "SELECT stable_fn()", path: "app", denied: "is a user-defined function"},
				// lower has IMMUTABLE pg_catalog overloads and a VOLATILE app overload.
				{sql: "SELECT lower('A')", path: "app", denied: "app.lower(integer) is VOLATILE"},
				{sql: "SELECT lower('A')", path: "public"},
				// A user-defined function shadowing an allowlisted volatile name.
				{sql: "SELECT random()", path: "app", denied: "app.random() is a user-defined function"},
				{sql: "SELECT pg_temp.f()", denied: "temporary schemas"},
				{sql: "SELECT no_such_function()"}, // left for PostgreSQL to report
			}
			for _, c := range cases {
				p, err := Check(c.sql)
				if err != nil {
					t.Fatalf("%s: %v", c.sql, err)
				}
				path := c.path
				if path == "" {
					path = "public"
				}
				err = allowIn(t, d, p, path, false)
				if err2 := allowIn(t, d, p, path, true); (err == nil) != (err2 == nil) {
					t.Errorf("%s (path %s): explicit path %v, session path %v", c.sql, path, err, err2)
				}
				var ge *Error
				switch {
				case c.denied == "" && err != nil:
					t.Errorf("%s (path %s): rejected: %v", c.sql, path, err)
				case c.denied != "" && (!errors.As(err, &ge) || ge.Reason != FunctionNotAllowed || !strings.Contains(ge.Message, c.denied)):
					t.Errorf("%s (path %s): got %v, want rejection containing %q", c.sql, path, err, c.denied)
				}
			}
		})
	}
}

// allowIn runs AllowFunctions in a read-only transaction with search_path
// pinned the way the executor pins it, passing the path explicitly or
// (session) leaving AllowFunctions to read it.
func allowIn(t *testing.T, d *pgtest.DB, p *Parsed, path string, session bool) error {
	t.Helper()
	ctx := context.Background()
	tx, err := d.Admin.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var schemas []string
	if err := tx.QueryRow(ctx, "SELECT set_config('search_path', $1, true), current_schemas(false)::text[]", "pg_catalog, "+path).
		Scan(new(string), &schemas); err != nil {
		t.Fatal(err)
	}
	if session {
		schemas = nil
	}
	return AllowFunctions(ctx, tx, p.Functions, schemas)
}

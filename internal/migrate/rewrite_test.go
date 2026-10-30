package migrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/pgtest"
)

const rewriteSetup = `CREATE TABLE users (id int PRIMARY KEY, email text);
CREATE TABLE orders (id int PRIMARY KEY, user_id int, amount int, status text);
INSERT INTO users SELECT g, 'u' || g FROM generate_series(1, 200) g;
INSERT INTO orders SELECT g, 1 + g % 200, g, 'open' FROM generate_series(1, 2000) g;
CREATE INDEX orders_status_idx ON orders (status)`

// Each rewrite must run on a real server, leave the same objects as the
// original statement, and analyze strictly better than it.
func TestRewriteLive(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "17", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			for _, c := range []struct {
				name, pre, sql, post string
				mode                 ingest.Mode
				want                 string // "" for no rewrite
			}{
				{name: "create index", sql: "CREATE INDEX orders_amount_idx ON orders (amount)",
					want: "CREATE INDEX CONCURRENTLY orders_amount_idx ON orders (amount)"},
				{name: "create unique index if not exists", pre: "SET lock_timeout = '2s';", sql: "CREATE UNIQUE INDEX IF NOT EXISTS orders_amount_key ON orders (amount)",
					want: "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS orders_amount_key ON orders (amount)"},
				{name: "create index in BEGIN keeps the build but adds SET LOCAL", pre: "BEGIN;", sql: "CREATE INDEX orders_amount_idx ON orders (amount)", post: "COMMIT;",
					want: "SET LOCAL lock_timeout = '2s';\nCREATE INDEX orders_amount_idx ON orders (amount)"},
				{name: "create index in a golang-migrate file", pre: "SELECT 1;", sql: "CREATE INDEX orders_amount_idx ON orders (amount)", mode: ingest.ModeFile,
					want: "SET LOCAL lock_timeout = '2s';\nCREATE INDEX orders_amount_idx ON orders (amount)"},
				{name: "create index in BEGIN with SET LOCAL", pre: "BEGIN; SET LOCAL lock_timeout = '2s';", sql: "CREATE INDEX orders_amount_idx ON orders (amount)", post: "COMMIT;"},
				{name: "create index concurrently", sql: "CREATE INDEX CONCURRENTLY orders_amount_idx ON orders (amount)"},
				{name: "drop index", pre: "SET lock_timeout = '2s';", sql: "DROP INDEX orders_status_idx",
					want: "DROP INDEX CONCURRENTLY orders_status_idx"},
				{name: "drop two indexes", pre: "SET lock_timeout = '2s'; CREATE INDEX CONCURRENTLY orders_amount_idx ON orders (amount);",
					sql: "DROP INDEX orders_status_idx, orders_amount_idx"},
				{name: "drop index concurrently", sql: "DROP INDEX CONCURRENTLY orders_status_idx"},
				{name: "unnamed foreign key", sql: "ALTER TABLE orders ADD FOREIGN KEY (user_id) REFERENCES users (id)",
					want: "SET lock_timeout = '2s';\nALTER TABLE orders ADD CONSTRAINT orders_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) NOT VALID;\nALTER TABLE orders VALIDATE CONSTRAINT orders_user_id_fkey"},
				{name: "named foreign key", pre: "SET lock_timeout = '2s';", sql: "ALTER TABLE public.orders ADD CONSTRAINT \"Orders User\" FOREIGN KEY (user_id) REFERENCES users",
					want: "ALTER TABLE public.orders ADD CONSTRAINT \"Orders User\" FOREIGN KEY (user_id) REFERENCES users NOT VALID;\nALTER TABLE public.orders VALIDATE CONSTRAINT \"Orders User\""},
				{name: "unnamed one-column check", pre: "SET lock_timeout = '2s';", sql: "ALTER TABLE orders ADD CHECK (amount > 0 AND amount < 1000000)",
					want: "ALTER TABLE orders ADD CONSTRAINT orders_amount_check CHECK (amount > 0 AND amount < 1000000) NOT VALID;\nALTER TABLE orders VALIDATE CONSTRAINT orders_amount_check"},
				{name: "unnamed two-column check", pre: "SET lock_timeout = '2s';", sql: "ALTER TABLE orders ADD CHECK (amount > 0 OR user_id IS NULL)",
					want: "ALTER TABLE orders ADD CONSTRAINT orders_check CHECK (amount > 0 OR user_id IS NULL) NOT VALID;\nALTER TABLE orders VALIDATE CONSTRAINT orders_check"},
				{name: "default check name taken", pre: "SET lock_timeout = '2s'; ALTER TABLE orders ADD CONSTRAINT orders_amount_check CHECK (amount > -1) NOT VALID;",
					sql: "ALTER TABLE orders ADD CHECK (amount > 0)"},
				{name: "check already not valid", pre: "SET lock_timeout = '2s';", sql: "ALTER TABLE orders ADD CONSTRAINT c CHECK (amount > 0) NOT VALID"},
				{name: "foreign key in BEGIN", pre: "BEGIN; SET LOCAL lock_timeout = '2s';", sql: "ALTER TABLE orders ADD FOREIGN KEY (user_id) REFERENCES users (id)", post: "COMMIT;"},
				{name: "add column without timeout", sql: "ALTER TABLE orders ADD COLUMN note text",
					want: "SET lock_timeout = '2s';\nALTER TABLE orders ADD COLUMN note text"},
				{name: "add column with timeout", pre: "SET lock_timeout = '2s';", sql: "ALTER TABLE orders ADD COLUMN note text"},
			} {
				t.Run(c.name, func(t *testing.T) {
					file := func(stmt string) string { return c.pre + "\n" + stmt + ";\n" + c.post }
					analyze := func(d *pgtest.DB, sql string) *Report {
						t.Helper()
						base, err := LiveBase(t.Context(), d.Admin, []string{"public"}, 0)
						if err != nil {
							t.Fatal(err)
						}
						rep, err := Analyze(t.Context(), base, Request{Files: []ingest.Input{{Name: "m.sql", SQL: sql}}, TxMode: c.mode})
						if err != nil {
							t.Fatal(err)
						}
						return rep
					}
					orig := srv.NewDB(t)
					orig.Exec(t, rewriteSetup)
					rep := analyze(orig, file(c.sql))
					first := slices.IndexFunc(rep.Statements, func(s Statement) bool { return s.SQL == c.sql })
					target := rep.Statements[first]
					got := ""
					if target.Rewrite != nil {
						got = target.Rewrite.SQL
						if target.Rewrite.Note == "" {
							t.Error("rewrite has no note")
						}
					}
					if got != c.want {
						t.Fatalf("rewrite\n got %q\nwant %q\nfindings %v", got, c.want, ids(target))
					}
					if c.want == "" {
						return
					}

					safe := srv.NewDB(t)
					safe.Exec(t, rewriteSetup)
					rep2 := analyze(safe, file(got))
					after := rep2.Statements[first : len(rep2.Statements)-(len(rep.Statements)-first-1)]
					var afterIDs []string
					for _, s := range after {
						if s.Rating.Rank() > target.Rating.Rank() {
							t.Errorf("%q rated %s, original %s", s.SQL, s.Rating, target.Rating)
						}
						afterIDs = append(afterIDs, ids(s)...)
					}
					removed := 0
					for _, id := range ids(target) {
						if !slices.Contains(afterIDs, id) {
							removed++
						}
					}
					for _, id := range afterIDs {
						if !slices.Contains(ids(target), id) {
							t.Errorf("rewrite adds finding %s", id)
						}
					}
					if removed == 0 {
						t.Errorf("rewrite removes no finding: before %v, after %v", ids(target), afterIDs)
					}
					t.Logf("%s %v -> %v", target.Rating, ids(target), afterIDs)

					run(t, orig, file(c.sql), c.mode)
					run(t, safe, file(got), c.mode)
					if a, b := objects(t, orig), objects(t, safe); a != b {
						t.Errorf("objects differ\noriginal: %s\nrewrite:  %s", a, b)
					}
				})
			}
		})
	}
}

// run executes sql as a migration tool would: one query per statement, or
// the whole file as one query in ModeFile (golang-migrate).
func run(t *testing.T, d *pgtest.DB, sql string, mode ingest.Mode) {
	t.Helper()
	if mode == ingest.ModeFile {
		d.Exec(t, sql)
		return
	}
	set := ingest.Load([]ingest.Input{{Name: "m.sql", SQL: sql}}, ingest.Options{})
	for _, s := range set.Files[0].Statements {
		d.Exec(t, s.SQL)
	}
}

// objects lists the constraints and indexes of public with their validity.
func objects(t *testing.T, d *pgtest.DB) string {
	t.Helper()
	var out string
	err := d.Admin.QueryRow(context.Background(), `SELECT string_agg(x, ', ' ORDER BY x) FROM (
		SELECT 'constraint ' || conname || ' ' || pg_get_constraintdef(oid) || ' validated=' || convalidated AS x
		  FROM pg_constraint WHERE connamespace = 'public'::regnamespace
		UNION ALL
		SELECT 'index ' || c.relname || ' valid=' || i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		 WHERE c.relnamespace = 'public'::regnamespace
		UNION ALL
		SELECT 'column ' || attrelid::regclass || '.' || attname FROM pg_attribute
		 WHERE attrelid IN ('orders'::regclass, 'users'::regclass) AND attnum > 0 AND NOT attisdropped) s`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

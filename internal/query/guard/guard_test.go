package guard

import (
	"errors"
	"slices"
	"strings"
	"testing"

	pgquery "github.com/wasilibs/go-pgquery"
)

type checkCase struct {
	name   string
	sql    string
	reason Reason   // "" means accepted
	fns    []string // expected functions when accepted
}

var checkCases = []checkCase{
	// Accepted shapes.
	{name: "select", sql: "SELECT 1"},
	{name: "trailing semicolon and comment", sql: "select 1 -- trailing\n;"},
	{name: "block comment hiding a statement", sql: "SELECT 1 /* ; DROP TABLE t */"},
	{name: "leading comment", sql: "/* c */ SELECT 1;"},
	{name: "semicolon in string", sql: "SELECT 'DROP TABLE x; --'"},
	{name: "dollar quoted", sql: "SELECT $q$;DELETE FROM t$q$"},
	{name: "values", sql: "VALUES (1), (2)"},
	{name: "table", sql: "TABLE app.orders"},
	{name: "cte", sql: "WITH x AS (SELECT 1 AS a) SELECT a FROM x"},
	{name: "recursive cte", sql: "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r WHERE n < 5) SELECT * FROM r"},
	{name: "union branches", sql: "SELECT lower('A') UNION SELECT upper('b') INTERSECT SELECT 'B'", fns: []string{"lower", "upper"}},
	{name: "subqueries", sql: "SELECT * FROM a WHERE id IN (SELECT a_id FROM b WHERE EXISTS (SELECT md5(x) FROM c))", fns: []string{"md5"}},
	{name: "lateral", sql: "SELECT * FROM a, LATERAL (SELECT * FROM b WHERE b.a_id = a.id LIMIT 1) l, LATERAL generate_series(1, a.n) g", fns: []string{"generate_series"}},
	{name: "rows from", sql: "SELECT * FROM ROWS FROM (generate_series(1, 3), unnest(ARRAY[1, 2])) AS t(a, b)", fns: []string{"generate_series", "unnest"}},
	{name: "window and filter", sql: "SELECT count(*) FILTER (WHERE x > 0) OVER (PARTITION BY y), COUNT(z) FROM t", fns: []string{"count"}},
	{name: "qualified and quoted", sql: `SELECT pg_catalog.lower('A'), "Upper"('b'), public.f()`, fns: []string{"Upper", "pg_catalog.lower", "public.f"}},
	{name: "function in from", sql: "SELECT * FROM pg_read_file('/etc/passwd')", fns: []string{"pg_read_file"}},
	{name: "function in rows from", sql: "SELECT * FROM ROWS FROM (pg_ls_dir('.'))", fns: []string{"pg_ls_dir"}},
	{name: "function in order by and join", sql: "SELECT 1 FROM a JOIN b ON a.id = abs(b.id) ORDER BY pg_sleep(1)", fns: []string{"abs", "pg_sleep"}},
	{name: "function in cte", sql: "WITH x AS (SELECT pg_notify('c', 'p')) SELECT * FROM x", fns: []string{"pg_notify"}},
	{name: "special syntax", sql: "SELECT extract(year FROM now()), substring('abc' FROM 2), trim(' a ')", fns: []string{"now", "pg_catalog.btrim", "pg_catalog.extract", "pg_catalog.substring"}},
	{name: "for update inside a string", sql: "SELECT 'FOR UPDATE'"},

	// Incidents and statement types.
	{name: "commit then drop", sql: "COMMIT; DROP TABLE users", reason: MultipleStatements},
	{name: "two selects", sql: "SELECT 1; SELECT 2", reason: MultipleStatements},
	{name: "select then delete", sql: "SELECT 1; DELETE FROM t", reason: MultipleStatements},
	{name: "statement after line comment", sql: "SELECT 1 --\n; DROP TABLE t", reason: MultipleStatements},
	{name: "copy to program", sql: "COPY (SELECT 1) TO PROGRAM 'id'", reason: NotSelect},
	{name: "copy to file", sql: "COPY t TO '/tmp/x'", reason: NotSelect},
	{name: "delete cte", sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", reason: WritesData},
	{name: "insert cte", sql: "WITH i AS (INSERT INTO t VALUES (1) RETURNING *) SELECT * FROM i", reason: WritesData},
	{name: "update cte", sql: "WITH u AS (UPDATE t SET x = 1 RETURNING *) SELECT * FROM u", reason: WritesData},
	{name: "nested cte write", sql: "SELECT (WITH d AS (DELETE FROM t RETURNING 1) SELECT 1)", reason: WritesData},
	{name: "cte write in union branch", sql: "SELECT 1 UNION ALL (WITH d AS (DELETE FROM t RETURNING 1) SELECT 1)", reason: WritesData},
	{name: "insert", sql: "INSERT INTO t VALUES (1)", reason: WritesData},
	{name: "update", sql: "UPDATE t SET x = 1", reason: WritesData},
	{name: "delete", sql: "DELETE FROM t", reason: WritesData},
	{name: "merge", sql: "MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE", reason: WritesData},
	{name: "select into", sql: "SELECT * INTO new_t FROM t", reason: SelectInto},
	{name: "select into in cte", sql: "WITH x AS (SELECT 1) SELECT * INTO y FROM x", reason: SelectInto},
	{name: "for update", sql: "SELECT * FROM t FOR UPDATE", reason: LockingClause},
	{name: "for share skip locked", sql: "SELECT * FROM t FOR SHARE SKIP LOCKED", reason: LockingClause},
	{name: "for no key update", sql: "SELECT * FROM t FOR NO KEY UPDATE OF t NOWAIT", reason: LockingClause},
	{name: "for key share", sql: "SELECT * FROM t FOR KEY SHARE", reason: LockingClause},
	{name: "locking in subquery", sql: "SELECT * FROM (SELECT * FROM t FOR UPDATE) s", reason: LockingClause},
	{name: "locking in union branch", sql: "SELECT 1 UNION ALL (SELECT 1 FROM t FOR UPDATE)", reason: LockingClause},
	{name: "locking in cte", sql: "WITH x AS (SELECT * FROM t FOR UPDATE) SELECT * FROM x", reason: LockingClause},
	{name: "locking in sublink", sql: "SELECT EXISTS (SELECT 1 FROM t FOR UPDATE)", reason: LockingClause},
	{name: "do", sql: "DO $$ BEGIN DELETE FROM t; END $$", reason: NotSelect},
	{name: "set", sql: "SET statement_timeout = 0", reason: NotSelect},
	{name: "set local", sql: "SET LOCAL default_transaction_read_only = off", reason: NotSelect},
	{name: "set role", sql: "SET ROLE postgres", reason: NotSelect},
	{name: "reset", sql: "RESET ALL", reason: NotSelect},
	{name: "show", sql: "SHOW search_path", reason: NotSelect},
	{name: "call", sql: "CALL p()", reason: NotSelect},
	{name: "explain analyze delete", sql: "EXPLAIN ANALYZE DELETE FROM t", reason: NotSelect},
	{name: "create table as", sql: "CREATE TABLE x AS SELECT 1", reason: NotSelect},
	{name: "declare cursor", sql: "DECLARE c CURSOR FOR SELECT 1", reason: NotSelect},
	{name: "listen", sql: "LISTEN x", reason: NotSelect},
	{name: "notify", sql: "NOTIFY x", reason: NotSelect},
	{name: "lock", sql: "LOCK TABLE t", reason: NotSelect},
	{name: "begin", sql: "BEGIN", reason: NotSelect},
	{name: "drop", sql: "DROP TABLE t", reason: NotSelect},
	{name: "prepare", sql: "PREPARE p AS DELETE FROM t", reason: NotSelect},
	{name: "empty", sql: "", reason: NotSelect},
	{name: "only comment", sql: "-- nothing\n", reason: NotSelect},
	{name: "only semicolons", sql: ";;", reason: NotSelect},
	{name: "typo", sql: "SELEC 1", reason: Syntax},
	{name: "incomplete", sql: "SELECT * FROM", reason: Syntax},
	{name: "unterminated string", sql: "SELECT 'abc", reason: Syntax},
}

func TestCheck(t *testing.T) {
	for _, c := range checkCases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Check(c.sql)
			if c.reason == "" {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				var got []string
				for _, f := range p.Functions {
					got = append(got, f.String())
				}
				if !slices.Equal(got, c.fns) {
					t.Fatalf("functions %q, want %q", got, c.fns)
				}
				if strings.Contains(p.Deparsed, ";") && !strings.Contains(c.sql, "'") && !strings.Contains(c.sql, "$q$") {
					t.Fatalf("deparsed keeps a semicolon: %q", p.Deparsed)
				}
				assertSafe(t, p)
				return
			}
			var ge *Error
			if !errors.As(err, &ge) {
				t.Fatalf("got %v (%T), want a %s rejection", err, err, c.reason)
			}
			if ge.Reason != c.reason || ge.Message == "" {
				t.Fatalf("reason %q (%s), want %q", ge.Reason, ge.Message, c.reason)
			}
		})
	}
}

func TestDeparse(t *testing.T) {
	p, err := Check("select id from app.orders  -- newest first\n order by created_at desc;")
	if err != nil {
		t.Fatal(err)
	}
	if p.Deparsed != "SELECT id FROM app.orders ORDER BY created_at DESC" {
		t.Fatalf("deparsed %q", p.Deparsed)
	}
}

func TestMessages(t *testing.T) {
	for sql, want := range map[string]string{
		"COPY t TO PROGRAM 'x'":              "can run, not COPY",
		"DO $$ BEGIN END $$":                 "can run, not DO",
		"SET x = 1":                          "can run, not SET/RESET",
		"ALTER TABLE t ADD c int":            "can run, not ALTER TABLE",
		"WITH d AS (DELETE FROM t) SELECT 1": "DELETE modifies data",
		"SELECT * FROM t FOR SHARE":          "FOR SHARE locks rows",
	} {
		_, err := Check(sql)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", sql, err, want)
		}
	}
}

// TestSyntaxPosition checks that positions are 1-based characters, not
// bytes: the é takes two bytes.
func TestSyntaxPosition(t *testing.T) {
	for sql, want := range map[string]int{
		"select from where": 13,
		"select 'é' frm x":  16,
		"SELEC 1":           1,
	} {
		_, err := Check(sql)
		var ge *Error
		if !errors.As(err, &ge) || ge.Reason != Syntax || ge.Position != want {
			t.Errorf("%q: %+v, want syntax at %d", sql, ge, want)
		}
	}
}

// assertSafe re-parses the deparsed text independently of Check and walks
// it for anything forbidden.
func assertSafe(t testing.TB, p *Parsed) {
	t.Helper()
	tree, err := pgquery.Parse(p.Deparsed)
	if err != nil {
		t.Fatalf("deparsed %q does not parse: %v", p.Deparsed, err)
	}
	if len(tree.Stmts) != 1 || tree.Stmts[0].GetStmt().GetSelectStmt() == nil {
		t.Fatalf("deparsed %q is not exactly one SELECT", p.Deparsed)
	}
	if bad := forbidden(p.Deparsed); bad != "" {
		t.Fatalf("accepted %q contains %s (deparsed %q)", p.SQL, bad, p.Deparsed)
	}
}

// forbidden finds a forbidden node using the JSON form of the tree, a
// different traversal from the guard's protobuf walk.
func forbidden(sql string) string {
	js, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return "unparseable"
	}
	for _, key := range []string{`"InsertStmt"`, `"UpdateStmt"`, `"DeleteStmt"`, `"MergeStmt"`, `"intoClause"`, `"lockingClause"`} {
		if strings.Contains(js, key) {
			return key
		}
	}
	// Any statement node other than the SELECTs.
	for i := 0; ; {
		j := strings.Index(js[i:], `Stmt":`)
		if j < 0 {
			break
		}
		k := strings.LastIndex(js[:i+j], `"`)
		if name := js[k+1 : i+j+4]; name != "SelectStmt" {
			return name
		}
		i += j + 6
	}
	return ""
}

package age

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
)

func TestParse(t *testing.T) {
	text := `[{"id": 844424930131969, "label": "Person", "properties": {"note": "a::edge in text", "big": 9007199254740993}}::vertex, ` +
		`{"id": 1125899906842625, "label": "KNOWS", "end_id": 844424930131970, "start_id": 844424930131969, "properties": {}}::edge, ` +
		`{"id": 844424930131970, "label": "Person", "properties": {}}::vertex]::path`
	v, err := parse(text)
	if err != nil {
		t.Fatal(err)
	}
	b := newBuilder()
	b.add(v)
	if len(b.g.Nodes) != 2 || len(b.g.Relationships) != 1 || b.g.Relationships[0].Start != "844424930131969" ||
		string(b.g.Nodes[0].Props["note"]) != `"a::edge in text"` || string(b.g.Nodes[0].Props["big"]) != `"9007199254740993"` {
		t.Fatalf("graph %+v", b.g)
	}
	if graphName(`SELECT * FROM ag_catalog.cypher('it''s', 'MATCH (n) RETURN n') AS (n agtype)`) != "it's" ||
		graphName(`SELECT * FROM cypher('a', 'x') AS (n agtype), cypher('b', 'y') AS (m agtype)`) != "" {
		t.Error("graph names")
	}
}

func TestAGE(t *testing.T) {
	p := mytest.AGE(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, "postgres://postgres:"+mytest.AGEPassword+"@"+p.Host+":"+strconv.Itoa(p.Port)+"/postgres?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for _, q := range []string{
		"CREATE EXTENSION IF NOT EXISTS age",
		`SET search_path = ag_catalog, "$user", public`,
		"SELECT ag_catalog.create_graph('social')",
		`SELECT * FROM ag_catalog.cypher('social', $$ UNWIND range(1, 6) AS i CREATE (:Person {name: 'P' + toString(i), n: i}) $$) AS (x ag_catalog.agtype)`,
		`SELECT * FROM ag_catalog.cypher('social', $$ MATCH (a:Person), (b:Person) WHERE b.n = a.n + 1 CREATE (a)-[:KNOWS {since: 2020}]->(b) $$) AS (x ag_catalog.agtype)`,
		// A user function of the same name must not slip through.
		"CREATE FUNCTION public.cypher(text, text) RETURNS SETOF record LANGUAGE sql VOLATILE AS 'SELECT 1'",
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	pool, err := conn.NewPool(ctx, p, mytest.AGEPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	read := `SELECT * FROM ag_catalog.cypher('social', $$ MATCH path = (:Person {n: 1})-[:KNOWS*3]->(:Person) RETURN path $$) AS (path ag_catalog.agtype)`
	parsed, err := guard.Check(read)
	if err != nil {
		t.Fatalf("read refused: %v", err)
	}
	res, err := exec.Run(ctx, pool, exec.Request{Parsed: parsed, StatementTimeout: 10 * time.Second, MaxRows: 100})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	g := Graph(res)
	if g == nil || len(g.Nodes) != 4 || len(g.Relationships) != 3 || g.Source != "social" || g.Nodes[0].Labels[0] != "Person" {
		t.Fatalf("graph %+v", g)
	}

	// Writing Cypher, a non-constant query and the user's own cypher() are refused before running.
	var ge *guard.Error
	if _, err := guard.Check(`SELECT * FROM ag_catalog.cypher('social', $$ MATCH (n) DETACH DELETE n $$) AS (x ag_catalog.agtype)`); !errors.As(err, &ge) || ge.Reason != guard.WritesData {
		t.Errorf("write: %v", err)
	}
	if _, err := guard.Check(`SELECT * FROM ag_catalog.cypher('social', (SELECT 'MATCH (n) RETURN n')) AS (x ag_catalog.agtype)`); !errors.As(err, &ge) || ge.Reason != guard.FunctionNotAllowed {
		t.Errorf("non-constant: %v", err)
	}
	own, err := guard.Check(`SELECT * FROM public.cypher('social', 'MATCH (n) RETURN n') AS (x int)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Run(ctx, pool, exec.Request{Parsed: own, StatementTimeout: 10 * time.Second, MaxRows: 10}); !errors.As(err, &ge) || ge.Reason != guard.FunctionNotAllowed {
		t.Errorf("user cypher(): %v", err)
	}

	// The READ ONLY transaction refuses AGE's writes even without the guard.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `SELECT * FROM ag_catalog.cypher('social', $$ CREATE (:Person {name: 'x'}) $$) AS (x ag_catalog.agtype)`)
	tx.Rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "read-only transaction") {
		t.Errorf("read-only write: %v", err)
	}

	second := g.Nodes[1].ID
	x, err := Expand(ctx, pool, "social", second, 1, 10*time.Second)
	if err != nil || x.Degree != 2 || x.Shown != 1 || len(x.Graph.Nodes) != 2 || x.Graph.Source != "social" {
		t.Fatalf("expand %+v %v", x, err)
	}
	if x, err = Expand(ctx, pool, "social", second, 10, 10*time.Second); err != nil || x.Shown != 2 || len(x.Graph.Nodes) != 3 {
		t.Errorf("expand all %+v %v", x, err)
	}
	if _, err := Expand(ctx, pool, "nope", second, 10, 10*time.Second); !errors.Is(err, ErrNoGraph) {
		t.Errorf("graph: %v", err)
	}
	if _, err := Expand(ctx, pool, "social", "1", 10, 10*time.Second); !errors.Is(err, ErrNoVertex) {
		t.Errorf("vertex: %v", err)
	}
	if _, err := Expand(ctx, pool, "social", "1; DROP", 10, 10*time.Second); !errors.Is(err, ErrNoVertex) {
		t.Errorf("bad id: %v", err)
	}
}

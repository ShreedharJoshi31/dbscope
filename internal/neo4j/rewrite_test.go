package neo4j

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestBatchRewriteLive runs each batched rewrite as an auto-commit statement
// on 5.26 and 2026.10: it must change exactly what the original changes and
// lose DBS-NEO4J-UNBATCHED without a higher rating.
func TestBatchRewriteLive(t *testing.T) {
	cases := []struct{ stmt, want string }{
		{"MATCH (a:Account) SET a.flag = true",
			"MATCH (a:Account)\nCALL (a) {\n  SET a.flag = true\n} IN TRANSACTIONS OF 10000 ROWS"},
		{"MATCH (a:Account) WHERE a.n % 2 = 0 SET a:Even REMOVE a:Account",
			"MATCH (a:Account) WHERE a.n % 2 = 0\nCALL (a) {\n  SET a:Even REMOVE a:Account\n} IN TRANSACTIONS OF 10000 ROWS"},
		{"MATCH (a:Account)-[r:LINK]->(h:Hub) WHERE a.n > 5000 DELETE r",
			"MATCH (a:Account)-[r:LINK]->(h:Hub) WHERE a.n > 5000\nCALL (r) {\n  DELETE r\n} IN TRANSACTIONS OF 10000 ROWS"},
		{"MATCH (a:Account) WHERE a.n <= 15000 DETACH DELETE a",
			"MATCH (a:Account) WHERE a.n <= 15000\nCALL (a) {\n  DETACH DELETE a\n} IN TRANSACTIONS OF 10000 ROWS"},
		{"MATCH (a:Account), (h:Hub) SET a.hub = h.name", "MATCH (a:Account), (h:Hub)\nCALL (a, h) {\n  SET a.hub = h.name\n} IN TRANSACTIONS OF 10000 ROWS"},
		{"MATCH (a:Account) WITH a LIMIT 5 SET a.x = 1", ""},
		{"MATCH (a:Account) SET a.flag = true RETURN count(a)", ""},
		{"MATCH (a:Account) CALL (a) { SET a.flag = true } IN TRANSACTIONS OF 5000 ROWS", ""},
		{"MATCH (h:Hub) SET h.x = 1", ""},
	}
	for _, name := range []string{"5", "2026"} {
		t.Run(name, func(t *testing.T) {
			p := mytest.Neo4j(t, name)
			ctx := context.Background()
			c, err := Connect(p, mytest.Neo4jPassword)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			run := func(q string) {
				t.Helper()
				s := c.Driver.NewSession(ctx, sessionConfig(c.Database))
				defer s.Close(ctx)
				r, err := s.Run(ctx, q, nil)
				if err == nil {
					_, err = r.Consume(ctx)
				}
				if err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			state := func() string {
				t.Helper()
				s := c.Driver.NewSession(ctx, sessionConfig(c.Database))
				defer s.Close(ctx)
				r, err := s.Run(ctx, `MATCH (n) WITH reduce(x = '', l IN labels(n) | x + l + ',') AS l, count(*) AS c,
					sum(coalesce(n.n, 0)) AS s, count(n.flag) AS f, count(n.hub) AS h ORDER BY l
					WITH collect(l + ':' + c + ':' + s + ':' + f + ':' + h) AS nodes
					OPTIONAL MATCH ()-[r]->() RETURN nodes, count(r) AS rels`, nil)
				if err != nil {
					t.Fatal(err)
				}
				rec, err := r.Single(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return fmt.Sprint(rec.Values...)
			}
			seed := func() {
				t.Helper()
				run("MATCH (n) CALL (n) { DETACH DELETE n } IN TRANSACTIONS OF 10000 ROWS")
				run("CREATE (:Hub {name: 'h'})")
				for i := 0; i < 4; i++ {
					run(fmt.Sprintf("MATCH (h:Hub) UNWIND range(%d, %d) AS i CREATE (:Account {n: i})-[:LINK]->(h)", i*5000+1, (i+1)*5000))
				}
			}
			for _, tc := range cases {
				t.Run(tc.stmt, func(t *testing.T) {
					seed()
					base, err := ReadBase(ctx, c, 30*time.Second)
					if err != nil {
						t.Fatal(err)
					}
					analyze := func(q string) migrate.Statement {
						return Analyze(ctx, base, []ingest.Input{{Name: "V1__x.cypher", SQL: q}}).Statements[0]
					}
					st := analyze(tc.stmt)
					got := ""
					if st.Rewrite != nil {
						got = st.Rewrite.SQL
					}
					if got != tc.want {
						t.Fatalf("rewrite\n got %q\nwant %q\nfindings %+v", got, tc.want, st.Findings)
					}
					if got == "" {
						return
					}
					after := analyze(got)
					if after.Rating.Rank() > st.Rating.Rank() || slices.ContainsFunc(after.Findings, func(f migrate.Finding) bool { return f.RuleID == "DBS-NEO4J-UNBATCHED" }) {
						t.Errorf("rewrite rated %s %+v; original %s %+v", after.Rating, after.Findings, st.Rating, st.Findings)
					}
					t.Logf("%s %d findings -> %s %d findings", st.Rating, len(st.Findings), after.Rating, len(after.Findings))
					run(tc.stmt)
					want := state()
					seed()
					run(got)
					if g := state(); g != want {
						t.Errorf("state after rewrite %s, after original %s", g, want)
					}
				})
			}
		})
	}
}

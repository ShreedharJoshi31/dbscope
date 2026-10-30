package neo4j

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/workload"
)

// TestWorkload reads the collector on 5.26 and 2026, which both collect
// queries from startup, and after an admin has stopped it.
func TestWorkload(t *testing.T) {
	for _, name := range []string{"5", "2026"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testWorkload(t, name)
		})
	}
}

func testWorkload(t *testing.T, name string) {
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
	run("UNWIND range(1, 20) AS i CREATE (:Person {n: i})-[:WORKS_AT]->(:Company {n: i})")
	m, err := Introspect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	recs, _, err := c.read(ctx, 10*time.Second, "CALL db.stats.status() YIELD section, status WHERE section = 'QUERIES' RETURN status", nil)
	if err != nil || len(recs) == 0 {
		t.Fatal(err)
	}
	// Both versions collect from startup (seen on 5.26 and 2026.10); an admin may stop it.
	if recs[0].Values[0] != "collecting" {
		t.Errorf("collector status at start: %v", recs[0].Values[0])
	}
	run("CALL db.stats.stop('QUERIES')")
	run("CALL db.stats.clear('QUERIES')")
	snap, err := Workload(ctx, c, m)
	if err != nil || snap.Statements.Status != workload.StatusUnavailable || !strings.Contains(snap.Statements.Message, "CALL db.stats.collect") {
		t.Fatalf("stopped collector %+v %v", snap.Statements, err)
	}
	run("CALL db.stats.collect('QUERIES', {durationSeconds: 300})")

	const app = "MATCH (p:Person)-[:WORKS_AT]->(c:Company) WHERE p.n > 0 RETURN count(*)"
	for range 4 {
		run(app)
	}
	snap, err = Workload(ctx, c, m)
	if err != nil || snap.Statements.Status != workload.StatusOK {
		t.Fatalf("collected %+v %v", snap.Statements, err)
	}
	for _, label := range []string{"neo4j.Person", "neo4j.Company"} {
		tb := snap.Tables[label]
		found := false
		for _, q := range tb.TopQueries {
			found = found || q.Text == app && q.Calls == 4 && q.MeanMs > 0
			if strings.HasPrefix(q.Text, "CALL db.") {
				t.Errorf("catalog call attributed: %s", q.Text)
			}
		}
		if tb == nil || tb.QueryCalls < 4 || !found {
			t.Errorf("%s %+v", label, tb)
		}
	}
}

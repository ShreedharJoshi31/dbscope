package neo4j

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/guard"
)

func TestMemgraphPlanText(t *testing.T) {
	if sizeBytes("1.5MiB") != 1572864 || sizeBytes("2GiB") != 2<<30 || sizeBytes("12B") != 12 || sizeBytes("x") != 0 {
		t.Error("sizes")
	}
}

func TestMemgraph(t *testing.T) {
	p := mytest.Memgraph(t)
	ctx := context.Background()
	c, err := Connect(p, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, q := range []string{
		"CREATE INDEX ON :Person(email)",
		"CREATE CONSTRAINT ON (p:Person) ASSERT p.email IS UNIQUE",
		"UNWIND range(1, 50) AS i CREATE (:Person {email: 'p' + toString(i) + '@x.io', name: 'P' + toString(i), age: i})",
		"UNWIND range(1, 5) AS i CREATE (:Company {name: 'C' + toString(i)})",
		"MATCH (p:Person), (c:Company) WHERE p.age % 5 = toInteger(substring(c.name, 1, 1)) % 5 CREATE (p)-[:WORKS_AT {since: 2020}]->(c)",
		"MATCH (a:Person), (b:Person) WHERE b.age = a.age + 1 CREATE (a)-[:KNOWS]->(b)",
		"CREATE (:Gadget {size: 1}), (:Gadget {size: 'L'})",
		"CREATE (h:Celebrity {name: 'hub'}) WITH h UNWIND range(1, 1200) AS i CREATE (:Fan {n: i})-[:FOLLOWS]->(h)",
	} {
		s := c.Driver.NewSession(ctx, drv.SessionConfig{})
		r, err := s.Run(ctx, q, nil)
		if err == nil {
			_, err = r.Consume(ctx)
		}
		s.Close(ctx)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	res := Test(ctx, p, "")
	if !res.OK || res.Engine != conn.EngineMemgraph || !strings.HasPrefix(res.ServerVersion, "3.13") || len(res.Warnings) != 1 {
		t.Fatalf("test %+v %+v", res, res.Error)
	}

	m, err := Introspect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, r := range m.Relations {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "Celebrity,Company,Fan,Gadget,Person" || m.Schemas[0] != "memgraph" || m.ServerVersion != 31302 {
		t.Fatalf("labels %v %v %d", names, m.Schemas, m.ServerVersion)
	}
	person := m.Relations[4]
	if *person.RowEstimate != 50 || person.Column("age").Type != "integer" || len(person.Uniques) != 1 || len(person.Indexes) != 1 ||
		len(person.ForeignKeys) != 2 || person.ForeignKeys[0].Ref.Name != "Person" && person.ForeignKeys[1].Ref.Name != "Person" {
		t.Errorf("person %+v", person)
	}
	if f := m.Relations[0].Findings; len(f) != 1 || f[0].RuleID != "DBS-NEO4J-SUPERNODE" {
		t.Errorf("celebrity %+v", f)
	}
	if f := m.Relations[3].Findings; len(f) != 1 || f[0].RuleID != "DBS-NEO4J-MIXED-TYPES" || f[0].Column != "size" {
		t.Errorf("gadget %+v", f)
	}

	codes := func(stmt string) []string {
		t.Helper()
		r, err := Run(ctx, c, stmt, 10, true, 10*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		var out []string
		nodes := r.Plan.Children
		for len(nodes) > 0 {
			n := nodes[0]
			nodes = append(nodes[1:], n.Children...)
			for _, f := range n.Flags {
				out = append(out, f.Code)
			}
		}
		return out
	}
	if got := codes("MATCH (n) RETURN n"); !contains(got, "all_nodes_scan") {
		t.Errorf("scan all %v", got)
	}
	if got := codes("MATCH (a:Person), (b:Company) RETURN a, b"); !contains(got, "cartesian_product") {
		t.Errorf("cartesian %v", got)
	}
	if got := codes("MATCH (a:Person)-[:KNOWS*]->(b) RETURN b"); !contains(got, "unbounded_path") {
		t.Errorf("unbounded %v", got)
	}
	if got := codes("MATCH (a:Person)-[:KNOWS*1..3]->(b) RETURN b"); contains(got, "unbounded_path") {
		t.Errorf("bounded %v", got)
	}

	r, err := Run(ctx, c, "MATCH p = (:Person {age: 1})-[:KNOWS*3]->(:Person) RETURN p", 10, false, 10*time.Second)
	if err != nil || r.Graph == nil || len(r.Graph.Nodes) != 4 || len(r.Graph.Relationships) != 3 {
		t.Fatalf("graph %+v %v", r, err)
	}
	// Procedures Memgraph marks as writing, and mg.* ones that manage the server, are refused before they run.
	for _, call := range []string{"CALL example_c.write_procedure('x')", "CALL mg.load_all()", "CALL mg.create_module_file('x.py', '')"} {
		var ge *guard.Error
		if _, err := Run(ctx, c, call, 10, false, 10*time.Second); !errors.As(err, &ge) {
			t.Errorf("%s: %v", call, err)
		}
	}
	if r, err := Run(ctx, c, "CALL mg.procedures() YIELD name RETURN count(name) AS n", 10, false, 10*time.Second); err != nil || r.RowCount != 1 {
		t.Errorf("read-only mg procedure: %+v %v", r, err)
	}

	x, err := Expand(ctx, c, r.Graph.Nodes[1].ID, 10, 10*time.Second)
	if err != nil || x.Degree != 3 || x.Shown != 3 {
		t.Errorf("expand %+v %v", x, err)
	}

	rep, err := Health(ctx, c)
	if err != nil || rep.Engine != "memgraph" || len(rep.Checks) != 2 || rep.Checks[0].ID != "memgraph.memory" || rep.Checks[0].Status != health.OK ||
		rep.Checks[1].Value != "IN_MEMORY_TRANSACTIONAL" {
		t.Errorf("health %+v %v", rep, err)
	}
	if _, err := ReadBase(ctx, c, time.Second); !errors.Is(err, ErrMemgraphMigrations) {
		t.Errorf("migrations: %v", err)
	}
}

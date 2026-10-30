package neo4j

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/guard"
)

func TestCheck(t *testing.T) {
	for stmt, ok := range map[string]bool{
		"MATCH (n:Person) RETURN n.name;":                        true,
		"MATCH (n) WHERE n.note = 'create; set' RETURN n":        true,
		"MATCH (n:`Create`) RETURN n.set // DELETE in a comment": true,
		"CALL db.labels()":                                       true,
		"MATCH (n) DETACH DELETE n":                              false,
		"MERGE (n:Person {id: 1})":                               false,
		"MATCH (n) SET n.x = 1":                                  false,
		"LOAD CSV FROM 'file:///x.csv' AS row RETURN row":        false,
		"CALL { MATCH (n) RETURN n } IN TRANSACTIONS RETURN 1":   false,
		"USE system SHOW USERS":                                  false,
		"PROFILE MATCH (n) RETURN n":                             false,
		"MATCH (n) RETURN n; MATCH (m) RETURN m":                 false,
		"CREATE INDEX x FOR (n:Person) ON (n.name)":              false,
	} {
		if err := Check(stmt); (err == nil) != ok {
			t.Errorf("Check(%q) = %v", stmt, err)
		}
	}
}

func TestUnbounded(t *testing.T) {
	for details, want := range map[string]bool{
		"(a)-[anon_0:KNOWS*]->(b)": true, "(a)-[r:KNOWS*2..]->(b)": true,
		"(a)-[r:KNOWS*..3]->(b)": false, "(a)-[r:KNOWS*1..5]->(b)": false, "(a)-[r:KNOWS*2]->(b)": false,
	} {
		if unbounded(details) != want {
			t.Errorf("unbounded(%q) != %v", details, want)
		}
	}
}

func TestNeo4j(t *testing.T) {
	for _, name := range []string{"5", "2026", "enterprise"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testServer(t, name)
		})
	}
}

func testServer(t *testing.T, name string) {
	p := mytest.Neo4j(t, name)
	ctx := context.Background()
	admin, err := Connect(p, mytest.Neo4jPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	write := func(cypher string) {
		t.Helper()
		s := admin.Driver.NewSession(ctx, sessionConfig(admin.Database))
		defer s.Close(ctx)
		r, err := s.Run(ctx, cypher, nil)
		if err == nil {
			_, err = r.Consume(ctx)
		}
		if err != nil {
			t.Fatalf("%s: %v", cypher, err)
		}
	}
	write("CREATE CONSTRAINT person_email FOR (p:Person) REQUIRE p.email IS UNIQUE")
	write("CREATE INDEX company_name FOR (c:Company) ON (c.name)")
	write("UNWIND range(1, 50) AS i CREATE (:Person {email: 'p' + i + '@x.io', name: 'P' + i, age: i})")
	write("UNWIND range(1, 5) AS i CREATE (:Company {name: 'C' + i, founded: date('2000-01-01')})")
	write("MATCH (p:Person), (c:Company) WHERE p.age % 5 = toInteger(substring(c.name, 1)) % 5 CREATE (p)-[:WORKS_AT {since: 2020}]->(c)")
	write("MATCH (a:Person), (b:Person) WHERE b.age = a.age + 1 CREATE (a)-[:KNOWS]->(b)")

	res := Test(ctx, p, mytest.Neo4jPassword)
	if !res.OK || !strings.Contains(res.ServerVersion, map[string]string{"5": "5.26", "2026": "2026", "enterprise": "enterprise"}[name]) {
		t.Fatalf("test %+v %+v", res, res.Error)
	}
	if name == "enterprise" {
		if len(res.WritePrivileges) != 1 || res.WritePrivileges[0] != "role admin" {
			t.Errorf("admin privileges %v", res.WritePrivileges)
		}
	} else if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "Community Edition") {
		t.Errorf("community warnings %v", res.Warnings)
	}
	if bad := Test(ctx, p, "wrong-password"); bad.OK || bad.Error.Kind != "auth" {
		t.Errorf("wrong password %+v %+v", bad, bad.Error)
	}

	c := admin
	if name == "enterprise" {
		s := admin.Driver.NewSession(ctx, sessionConfig("system"))
		for _, q := range []string{"CREATE USER ro SET PASSWORD 'ro-password' CHANGE NOT REQUIRED", "GRANT ROLE reader TO ro"} {
			r, err := s.Run(ctx, q, nil)
			if err == nil {
				_, err = r.Consume(ctx)
			}
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		s.Close(ctx)
		ro := p
		ro.User = "ro"
		if res := Test(ctx, ro, "ro-password"); !res.OK || len(res.WritePrivileges) != 0 || len(res.Warnings) != 0 {
			t.Errorf("reader test %+v %+v", res, res.Error)
		}
		if c, err = Connect(ro, "ro-password"); err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}

	t.Run("introspect", func(t *testing.T) {
		m, err := Introspect(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Relations) != 2 || m.Relations[0].Name != "Company" || m.Relations[1].Name != "Person" {
			t.Fatalf("labels %+v", m.Relations)
		}
		person := m.Relations[1]
		if *person.RowEstimate != 50 || person.Column("age").Type != "integer" || len(person.Uniques) != 1 || person.Uniques[0][0] != "email" {
			t.Errorf("person %+v", person)
		}
		if len(person.ForeignKeys) != 2 || person.ForeignKeys[0].Ref.Name != "Person" || person.ForeignKeys[1].Ref.Name != "Company" ||
			!strings.Contains(person.Column("-[:WORKS_AT]-> Company").Comment, "properties: since integer") {
			t.Errorf("relationships %+v %+v", person.ForeignKeys, person.Columns)
		}
		if m.Relations[0].Column("founded").Type != "date" || len(m.Relations[0].Indexes) != 1 {
			t.Errorf("company %+v", m.Relations[0])
		}
	})

	t.Run("run", func(t *testing.T) {
		flags := func(stmt string) (string, []string) {
			t.Helper()
			r, err := Run(ctx, c, stmt, 10, true, 10*time.Second)
			if err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
			var codes []string
			nodes := r.Plan.Children
			for len(nodes) > 0 {
				n := nodes[0]
				nodes = nodes[1:]
				for _, f := range n.Flags {
					codes = append(codes, f.Code)
				}
				nodes = append(nodes, n.Children...)
			}
			return r.Plan.Children[0].NodeType, codes
		}
		if _, codes := flags("MATCH (n) RETURN n"); !contains(codes, "all_nodes_scan") {
			t.Errorf("all nodes %v", codes)
		}
		if _, codes := flags("MATCH (a:Person), (c:Company) RETURN a, c"); !contains(codes, "cartesian_product") || !contains(codes, "gql_03N90") {
			t.Errorf("cartesian %v", codes)
		}
		if _, codes := flags("MATCH (a:Person)-[:KNOWS*]->(b) RETURN b"); !contains(codes, "unbounded_path") {
			t.Errorf("unbounded %v", codes)
		}
		if _, codes := flags("MATCH (p:Person {email: 'p1@x.io'}) RETURN p"); len(codes) != 0 {
			t.Errorf("index seek %v", codes)
		}
		r, err := Run(ctx, c, "MATCH (p:Person)-[w:WORKS_AT]->(co:Company) WHERE p.age = 5 RETURN p.name, p.age, co, co.founded", 10, false, 10*time.Second)
		if err != nil || r.RowCount != 1 || string(r.Rows[0][0]) != `"P5"` || string(r.Rows[0][1]) != "5" ||
			!strings.Contains(string(r.Rows[0][2]), `\"labels\":[\"Company\"]`) || string(r.Rows[0][3]) != `"2000-01-01"` {
			t.Errorf("run %+v %v", r, err)
		}
		// A WRITE-mode procedure has no write clause for the token check; its mode refuses it.
		_, err = Run(ctx, c, "CALL db.createLabel('Sneaky')", 10, false, 10*time.Second)
		var ge *guard.Error
		if !errors.As(err, &ge) {
			t.Errorf("write procedure ran: %v", err)
		}
		if _, err := Run(ctx, c, "MATCH (n:Nope RETURN n", 10, false, 10*time.Second); err == nil {
			t.Error("syntax error ran")
		}
	})
	testGraph(t, ctx, c)
}

func testGraph(t *testing.T, ctx context.Context, c *Client) {
	t.Run("graph result", func(t *testing.T) {
		r, err := Run(ctx, c, "MATCH p = (:Person {age: 1})-[:KNOWS*3]->(:Person) RETURN p", 10, false, 10*time.Second)
		if err != nil || r.Graph == nil || len(r.Graph.Nodes) != 4 || len(r.Graph.Relationships) != 3 || r.Graph.Truncated {
			t.Fatalf("path graph %+v %v", r.Graph, err)
		}
		ids := map[string]bool{}
		for _, n := range r.Graph.Nodes {
			ids[n.ID] = true
			if n.Stub || n.Labels[0] != "Person" || n.Props["email"] == nil {
				t.Errorf("node %+v", n)
			}
		}
		for _, rel := range r.Graph.Relationships {
			if rel.Type != "KNOWS" || !ids[rel.Start] || !ids[rel.End] {
				t.Errorf("relationship %+v", rel)
			}
		}
		// Relationships alone bring their ends as stubs.
		r, err = Run(ctx, c, "MATCH (:Person {age: 5})-[w:WORKS_AT]->() RETURN w", 10, false, 10*time.Second)
		if err != nil || r.Graph == nil || len(r.Graph.Relationships) != 1 || len(r.Graph.Nodes) != 2 || !r.Graph.Nodes[0].Stub ||
			string(r.Graph.Relationships[0].Props["since"]) != "2020" {
			t.Errorf("relationship graph %+v %v", r.Graph, err)
		}
		if r, err := Run(ctx, c, "MATCH (p:Person) RETURN p.name LIMIT 3", 10, false, 10*time.Second); err != nil || r.Graph != nil {
			t.Errorf("scalar result drew a graph: %+v %v", r, err)
		}
		// Only the rows returned are drawn.
		if r, err := Run(ctx, c, "MATCH (p:Person) RETURN p", 7, false, 10*time.Second); err != nil || len(r.Graph.Nodes) != 7 {
			t.Errorf("row limit %+v %v", r, err)
		}
	})

	t.Run("expand", func(t *testing.T) {
		recs, _, err := c.read(ctx, 10*time.Second, "MATCH (p:Person {age: 2}) RETURN elementId(p)", nil)
		if err != nil || len(recs) != 1 {
			t.Fatal(err)
		}
		id := recs[0].Values[0].(string)
		// age 2 KNOWS 3, is known by 1, and works at C2.
		x, err := Expand(ctx, c, id, 1, 10*time.Second)
		if err != nil || x.Degree != 3 || x.Shown != 1 || len(x.Graph.Nodes) != 2 {
			t.Fatalf("expand 1 %+v %v", x, err)
		}
		x, err = Expand(ctx, c, id, 10, 10*time.Second)
		if err != nil || x.Degree != 3 || x.Shown != 3 || len(x.Graph.Nodes) != 4 || x.Graph.Nodes[0].ID != id {
			t.Fatalf("expand 10 %+v %v", x, err)
		}
		for _, n := range x.Graph.Nodes {
			if n.Stub {
				t.Errorf("stub in expansion %+v", n)
			}
		}
		missing := id[:strings.LastIndexByte(id, ':')+1] + "99999999"
		if _, err := Expand(ctx, c, missing, 10, 10*time.Second); !errors.Is(err, ErrNoNode) {
			t.Errorf("missing node: %v", err)
		}
	})
}

func sessionConfig(db string) drv.SessionConfig { return drv.SessionConfig{DatabaseName: db} }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

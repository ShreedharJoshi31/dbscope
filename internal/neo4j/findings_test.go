package neo4j

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestFindings(t *testing.T) {
	p := mytest.Neo4j(t, "2026")
	ctx := context.Background()
	c, err := Connect(p, mytest.Neo4jPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := c.Driver.NewSession(ctx, sessionConfig(c.Database))
	for _, q := range []string{
		"CREATE (:Celebrity {name: 'hub'})",
		"MATCH (h:Celebrity) UNWIND range(1, 1200) AS i CREATE (:Fan {n: i})-[:FOLLOWS]->(h)",
		"CREATE (:Gadget {size: 1}), (:Gadget {size: 'L'}), (:Gadget {size: 2})",
		"UNWIND range(1, 12000) AS i CREATE (:Event {n: i})",
		"CREATE INDEX fan_n FOR (f:Fan) ON (f.n)",
		"UNWIND range(1, 20) AS i CREATE (:Team {n: i})",
	} {
		r, err := s.Run(ctx, q, nil)
		if err == nil {
			_, err = r.Consume(ctx)
		}
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close(ctx)

	m, err := Introspect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	rules := func(name string) []schema.Finding {
		for _, r := range m.Relations {
			if r.Name == name {
				return r.Findings
			}
		}
		t.Fatalf("no label %s", name)
		return nil
	}
	if f := rules("Celebrity"); len(f) != 1 || f[0].RuleID != "DBS-NEO4J-SUPERNODE" || f[0].Level != "warn" ||
		!strings.HasPrefix(f[0].Message, "The only Celebrity node has 1200 relationships") {
		t.Errorf("celebrity %+v", f)
	}
	// Each fan has one relationship: no supernode, and it is indexed.
	if f := rules("Fan"); len(f) != 0 {
		t.Errorf("fan %+v", f)
	}
	if f := rules("Gadget"); len(f) != 1 || f[0].RuleID != "DBS-NEO4J-MIXED-TYPES" || f[0].Column != "size" ||
		!strings.Contains(f[0].Message, "integer | string") && !strings.Contains(f[0].Message, "string | integer") {
		t.Errorf("gadget %+v", f)
	}
	if f := rules("Event"); len(f) != 1 || f[0].RuleID != "DBS-NEO4J-NO-INDEX" || !strings.Contains(f[0].Message, "12000 nodes") {
		t.Errorf("event %+v", f)
	}
	if f := rules("Team"); len(f) != 0 {
		t.Errorf("team %+v", f)
	}
}

package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/dbscope/dbscope/internal/query/exec"
)

// Drawing limits: a browser draws a few hundred nodes comfortably.
const (
	MaxGraphNodes         = 500
	MaxGraphRelationships = 1000
)

// graphBuilder collects the nodes and relationships in result values,
// each once, up to the drawing limits.
type graphBuilder struct {
	g     exec.Graph
	nodes map[string]int // id -> index in g.Nodes
	rels  map[string]bool
}

func newGraphBuilder() *graphBuilder {
	return &graphBuilder{g: exec.Graph{Nodes: []exec.GraphNode{}, Relationships: []exec.GraphRelationship{}},
		nodes: map[string]int{}, rels: map[string]bool{}}
}

func props(m map[string]any) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = cell(v)
	}
	return out
}

func (b *graphBuilder) node(n dbtype.Node) {
	if i, ok := b.nodes[n.ElementId]; ok {
		if b.g.Nodes[i].Stub { // a relationship's end, now returned in full
			b.g.Nodes[i] = exec.GraphNode{ID: n.ElementId, Labels: labels(n.Labels), Props: props(n.Props)}
		}
		return
	}
	if len(b.g.Nodes) >= MaxGraphNodes {
		b.g.Truncated = true
		return
	}
	b.nodes[n.ElementId] = len(b.g.Nodes)
	b.g.Nodes = append(b.g.Nodes, exec.GraphNode{ID: n.ElementId, Labels: labels(n.Labels), Props: props(n.Props)})
}

func labels(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// stub adds a relationship end that the result did not return, if there is room.
func (b *graphBuilder) stub(id string) bool {
	if _, ok := b.nodes[id]; ok {
		return true
	}
	if len(b.g.Nodes) >= MaxGraphNodes {
		b.g.Truncated = true
		return false
	}
	b.nodes[id] = len(b.g.Nodes)
	b.g.Nodes = append(b.g.Nodes, exec.GraphNode{ID: id, Labels: []string{}, Props: map[string]json.RawMessage{}, Stub: true})
	return true
}

func (b *graphBuilder) relationship(r dbtype.Relationship) {
	if b.rels[r.ElementId] {
		return
	}
	if len(b.g.Relationships) >= MaxGraphRelationships || !b.stub(r.StartElementId) || !b.stub(r.EndElementId) {
		b.g.Truncated = true
		return
	}
	b.rels[r.ElementId] = true
	b.g.Relationships = append(b.g.Relationships, exec.GraphRelationship{ID: r.ElementId, Type: r.Type,
		Start: r.StartElementId, End: r.EndElementId, Props: props(r.Props)})
}

// add walks a value for graph elements, including inside lists, maps and paths.
func (b *graphBuilder) add(v any) {
	switch x := v.(type) {
	case dbtype.Node:
		b.node(x)
	case dbtype.Relationship:
		b.relationship(x)
	case dbtype.Path:
		for _, n := range x.Nodes {
			b.node(n)
		}
		for _, r := range x.Relationships {
			b.relationship(r)
		}
	case []any:
		for _, e := range x {
			b.add(e)
		}
	case map[string]any:
		for _, e := range x {
			b.add(e)
		}
	}
}

// graph is the collected graph, or nil when the result held no nodes.
func (b *graphBuilder) graph() *exec.Graph {
	if len(b.g.Nodes) == 0 {
		return nil
	}
	return &b.g
}

// Expansion is a node with some of its neighbours.
type Expansion struct {
	Graph *exec.Graph `json:"graph"`
	// Degree is how many relationships the node has; Shown how many are drawn.
	Degree int64 `json:"degree"`
	Shown  int   `json:"shown"`
}

// MaxExpand caps the neighbours one expansion returns: a supernode can have millions.
const MaxExpand = 200

// Expand reads a node, its degree, and up to limit of its relationships
// with the nodes at their other end. The Cypher is fixed and takes the id
// as a parameter, so no user text runs.
func Expand(ctx context.Context, c *Client, id string, limit int, timeout time.Duration) (*Expansion, error) {
	limit = max(1, min(limit, MaxExpand))
	recs, _, err := c.read(ctx, timeout, `MATCH (n) WHERE elementId(n) = $id RETURN n, COUNT { (n)--() } AS degree`, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrNoNode
	}
	b := newGraphBuilder()
	b.add(recs[0].Values[0])
	out := &Expansion{}
	out.Degree, _ = recs[0].Values[1].(int64)
	recs, _, err = c.read(ctx, timeout, `MATCH (n)-[r]-(m) WHERE elementId(n) = $id RETURN r, m LIMIT $limit`,
		map[string]any{"id": id, "limit": int64(limit)})
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		b.add(rec.Values[1])
		b.add(rec.Values[0])
	}
	out.Graph, out.Shown = &b.g, len(b.g.Relationships)
	return out, nil
}

// ErrNoNode means the node to expand no longer exists.
var ErrNoNode = errors.New("no node has this id; it may have been deleted")

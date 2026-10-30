// Package age reads Apache AGE graphs inside PostgreSQL: it turns the
// vertices, edges and paths that cypher() returns into a drawable graph,
// and expands a vertex by its neighbours. dbscope's guard admits only
// read-only cypher() calls (internal/query/guard), and every read runs in a
// READ ONLY transaction, which AGE's writes cannot pass (verified on AGE 1.8
// with PostgreSQL 18).
package age

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/query/exec"
)

// Drawing limits, as for Neo4j.
const (
	maxNodes         = 500
	maxRelationships = 1000
	// MaxExpand caps the neighbours one expansion returns.
	MaxExpand = 200
)

// ErrNoVertex means the vertex to expand does not exist in the graph.
var ErrNoVertex = errors.New("no vertex has this id in the graph; it may have been deleted")

// ErrNoGraph means the graph is not one of the database's AGE graphs.
var ErrNoGraph = errors.New("no Apache AGE graph has this name in the database")

// isAgtype reports whether a result column holds AGE values.
func isAgtype(c exec.Column) bool {
	return c.Type == "agtype" || c.Type == "ag_catalog.agtype"
}

// graphName is the graph a statement's cypher() calls read, when they all name the same one.
var graphArg = regexp.MustCompile(`cypher\(\s*'((?:[^']|'')*)'`)

func graphName(sql string) string {
	name := ""
	for _, m := range graphArg.FindAllStringSubmatch(sql, -1) {
		g := strings.ReplaceAll(m[1], "''", "'")
		if name != "" && g != name {
			return ""
		}
		name = g
	}
	return name
}

// Graph collects the vertices and edges in a result's agtype columns, or
// returns nil when it has none. Source is the graph its cypher() read.
func Graph(res *exec.Result) *exec.Graph {
	cols := []int{}
	for i, c := range res.Columns {
		if isAgtype(c) {
			cols = append(cols, i)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	b := newBuilder()
	for _, row := range res.Rows {
		for _, i := range cols {
			var text string
			if json.Unmarshal(row[i], &text) != nil {
				continue
			}
			v, err := parse(text)
			if err == nil {
				b.add(v)
			}
		}
	}
	if len(b.g.Nodes) == 0 {
		return nil
	}
	b.g.Source = graphName(res.ExecutedSQL)
	return &b.g
}

// parse reads agtype text: JSON with ::vertex, ::edge and ::path after values.
// Numbers stay exact, since ids pass 2^53 once a graph has 32 labels.
func parse(text string) (any, error) {
	d := json.NewDecoder(strings.NewReader(stripAnnotations(text)))
	d.UseNumber()
	var v any
	return v, d.Decode(&v)
}

// stripAnnotations removes "::name" outside JSON strings.
func stripAnnotations(s string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			b.WriteByte(c)
		case c == ':' && i+1 < len(s) && s[i+1] == ':':
			i += 2
			for i < len(s) && (s[i] == '_' || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
				i++
			}
			i--
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

type builder struct {
	g     exec.Graph
	nodes map[string]int
	rels  map[string]bool
}

func newBuilder() *builder {
	return &builder{g: exec.Graph{Nodes: []exec.GraphNode{}, Relationships: []exec.GraphRelationship{}}, nodes: map[string]int{}, rels: map[string]bool{}}
}

func props(v any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	m, _ := v.(map[string]any)
	for k, p := range m {
		if n, ok := p.(json.Number); ok {
			if i, err := n.Int64(); err == nil && (i > 1<<53-1 || i < -(1<<53-1)) {
				out[k], _ = json.Marshal(n.String()) // beyond JavaScript's exact range, as for Neo4j
				continue
			}
		}
		out[k], _ = json.Marshal(p)
	}
	return out
}

func idText(v any) string {
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return ""
}

func (b *builder) node(id, label string, p any) {
	if i, ok := b.nodes[id]; ok {
		if b.g.Nodes[i].Stub {
			b.g.Nodes[i] = exec.GraphNode{ID: id, Labels: []string{label}, Props: props(p)}
		}
		return
	}
	if len(b.g.Nodes) >= maxNodes {
		b.g.Truncated = true
		return
	}
	b.nodes[id] = len(b.g.Nodes)
	b.g.Nodes = append(b.g.Nodes, exec.GraphNode{ID: id, Labels: []string{label}, Props: props(p)})
}

func (b *builder) stub(id string) bool {
	if _, ok := b.nodes[id]; ok {
		return true
	}
	if len(b.g.Nodes) >= maxNodes {
		b.g.Truncated = true
		return false
	}
	b.nodes[id] = len(b.g.Nodes)
	b.g.Nodes = append(b.g.Nodes, exec.GraphNode{ID: id, Labels: []string{}, Props: map[string]json.RawMessage{}, Stub: true})
	return true
}

func (b *builder) add(v any) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			b.add(e)
		}
	case map[string]any:
		id := idText(x["id"])
		label, _ := x["label"].(string)
		_, hasProps := x["properties"]
		if id == "" || !hasProps {
			for _, e := range x {
				b.add(e)
			}
			return
		}
		start, end := idText(x["start_id"]), idText(x["end_id"])
		if start == "" || end == "" {
			b.node(id, label, x["properties"])
			return
		}
		if b.rels[id] {
			return
		}
		if len(b.g.Relationships) >= maxRelationships || !b.stub(start) || !b.stub(end) {
			b.g.Truncated = true
			return
		}
		b.rels[id] = true
		b.g.Relationships = append(b.g.Relationships, exec.GraphRelationship{ID: id, Type: label, Start: start, End: end, Props: props(x["properties"])})
	}
}

// Expansion is a vertex with some of its neighbours.
type Expansion = exec.Expansion

// Expand reads a vertex, its degree and up to limit of its edges with their
// other ends, in a READ ONLY transaction under timeout. The graph name is
// checked against ag_graph and the id must be an integer, so the Cypher
// dbscope writes holds no user text.
func Expand(ctx context.Context, db exec.DB, graph, id string, limit int, timeout time.Duration) (*Expansion, error) {
	limit = max(1, min(limit, MaxExpand))
	vid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, ErrNoVertex
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true), set_config('search_path', 'ag_catalog, pg_catalog', true)",
		strconv.FormatInt(timeout.Milliseconds(), 10)); err != nil {
		return nil, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ag_catalog.ag_graph WHERE name = $1)", graph).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNoGraph
	}
	lit := "'" + strings.ReplaceAll(graph, "'", "''") + "'"
	run := func(cypher, cols string) ([]string, error) {
		rows, err := tx.Query(ctx, "SELECT * FROM ag_catalog.cypher("+lit+", $$ "+cypher+" $$) AS ("+cols+")")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			vals := rows.RawValues()
			for _, v := range vals {
				out = append(out, string(v))
			}
		}
		return out, rows.Err()
	}
	b := newBuilder()
	got, err := run(fmt.Sprintf("MATCH (n) WHERE id(n) = %d OPTIONAL MATCH (n)-[r]-() RETURN n, count(r)", vid), "n ag_catalog.agtype, d ag_catalog.agtype")
	if err != nil {
		return nil, err
	}
	if len(got) < 2 {
		return nil, ErrNoVertex
	}
	x := &Expansion{}
	if v, err := parse(got[0]); err == nil {
		b.add(v)
	}
	x.Degree, _ = strconv.ParseInt(strings.TrimSpace(got[1]), 10, 64)
	got, err = run(fmt.Sprintf("MATCH (n)-[r]-(m) WHERE id(n) = %d RETURN r, m LIMIT %d", vid, limit), "r ag_catalog.agtype, m ag_catalog.agtype")
	if err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(got); i += 2 {
		for _, t := range []string{got[i+1], got[i]} {
			if v, err := parse(t); err == nil {
				b.add(v)
			}
		}
	}
	b.g.Source = graph
	x.Graph, x.Shown = &b.g, len(b.g.Relationships)
	return x, nil
}

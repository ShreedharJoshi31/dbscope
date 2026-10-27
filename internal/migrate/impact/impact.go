// Package impact lists what depends on the relations and columns a
// migration changes: foreign-key children, views and materialized views
// (recursively), SQL-standard function bodies, policies, generated columns,
// triggers, indexes and, when readable, queries in pg_stat_statements.
//
// Against a live database the catalog queries (docs/research-migrations.md
// section 6) give "catalog" certainty. Function bodies that are opaque text
// (PL/pgSQL, string SQL bodies) and pg_stat_statements texts are matched by
// name and labelled "heuristic". Without a database (schema dump, stats
// file, sample) only the model's foreign keys, view dependencies and
// indexes are used.
package impact

import (
	"cmp"
	"slices"

	"github.com/dbscope/dbscope/internal/schema"
)

// Item is one dependent object.
type Item struct {
	// Kind: fk_child, view, matview, function, trigger, policy, index,
	// generated_column or query.
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Certainty string `json:"certainty"` // catalog | heuristic
}

const (
	Catalog   = "catalog"
	Heuristic = "heuristic"
)

// Target is a changed relation, or a column of it when Column is set.
type Target struct {
	Relation schema.QualifiedName
	Column   string
}

// Key is how the report indexes the target: "schema.table" or "schema.table.column".
func (t Target) Key() string {
	if t.Column == "" {
		return t.Relation.String()
	}
	return t.Relation.String() + "." + t.Column
}

// Result maps Target.Key to its dependents.
type Result map[string][]Item

func (r Result) add(key string, it Item) {
	for _, x := range r[key] {
		if x.Kind == it.Kind && x.Name == it.Name {
			return
		}
	}
	r[key] = append(r[key], it)
}

func (r Result) sort() {
	for k := range r {
		slices.SortFunc(r[k], func(a, b Item) int {
			return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
		})
	}
}

// FromModel derives dependents from the model alone.
func FromModel(m *schema.Model, targets []Target) Result {
	out := Result{}
	for _, t := range targets {
		key := t.Key()
		for _, r := range m.Relations {
			for _, fk := range r.ForeignKeys {
				if fk.Ref != t.Relation || r.QName() == t.Relation || fk.Inferred {
					continue
				}
				if t.Column == "" || slices.Contains(fk.RefCols, t.Column) {
					out.add(key, Item{Kind: "fk_child", Name: r.QName().String() + " (" + fk.Name + ")", Certainty: Catalog})
				}
			}
		}
		certainty := Catalog
		if t.Column != "" {
			certainty = Heuristic // the model records view dependencies per relation only
		}
		for _, v := range dependents(m, t.Relation) {
			kind := "view"
			if r := m.Relation(v); r != nil && r.Kind == schema.KindMatView {
				kind = "matview"
			}
			out.add(key, Item{Kind: kind, Name: v.String(), Certainty: certainty})
		}
		if t.Column != "" {
			if r := m.Relation(t.Relation); r != nil {
				for _, ix := range r.Indexes {
					if slices.Contains(ix.Columns, t.Column) || slices.Contains(ix.Include, t.Column) {
						out.add(key, Item{Kind: "index", Name: t.Relation.Schema + "." + ix.Name, Certainty: Catalog})
					}
				}
				for _, c := range r.Columns {
					if c.Generated != "" && c.Name != t.Column && mentions(c.Generated, t.Column) {
						out.add(key, Item{Kind: "generated_column", Name: t.Relation.String() + "." + c.Name, Certainty: Heuristic})
					}
				}
			}
		}
	}
	out.sort()
	return out
}

func dependents(m *schema.Model, q schema.QualifiedName) []schema.QualifiedName {
	seen := map[schema.QualifiedName]bool{}
	var order []schema.QualifiedName
	var walk func(schema.QualifiedName)
	walk = func(t schema.QualifiedName) {
		for _, r := range m.Relations {
			if slices.Contains(r.DependsOn, t) && !seen[r.QName()] {
				seen[r.QName()] = true
				order = append(order, r.QName())
				walk(r.QName())
			}
		}
	}
	walk(q)
	return order
}

func mentions(expr, name string) bool {
	for i := 0; i+len(name) <= len(expr); i++ {
		if expr[i:i+len(name)] != name {
			continue
		}
		before := i == 0 || !identByte(expr[i-1])
		after := i+len(name) == len(expr) || !identByte(expr[i+len(name)])
		if before && after {
			return true
		}
	}
	return false
}

func identByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

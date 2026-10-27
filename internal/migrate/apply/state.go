// Package apply replays migration statements on a copy of a schema.Model,
// so each statement is classified against the schema as it is at that
// point, and the final model can be diffed against the base. The same code
// builds a base model from `pg_dump --schema-only` output.
package apply

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/dbscope/dbscope/internal/schema"
)

type QName = schema.QualifiedName

// Catalog holds facts the classifier needs that schema.Model does not carry.
type Catalog struct {
	// ConstrainedDomains are domains with CHECK or NOT NULL constraints,
	// keyed "schema.name"; adding a column of such a type rewrites the table.
	ConstrainedDomains map[string]bool `json:"constrainedDomains,omitempty"`
	// DefaultPartitions maps a partitioned table to its DEFAULT partition.
	DefaultPartitions map[string]QName `json:"defaultPartitions,omitempty"`
	// Clustered are tables with a clustered index (ALTER TABLE ... CLUSTER ON),
	// keyed "schema.name"; a bare CLUSTER rewrites each of them.
	Clustered map[string]bool `json:"clustered,omitempty"`
}

func (c *Catalog) clone() *Catalog {
	out := &Catalog{ConstrainedDomains: map[string]bool{}, DefaultPartitions: map[string]QName{}, Clustered: map[string]bool{}}
	if c != nil {
		for k, v := range c.Clustered {
			out.Clustered[k] = v
		}
		for k, v := range c.ConstrainedDomains {
			out.ConstrainedDomains[k] = v
		}
		for k, v := range c.DefaultPartitions {
			out.DefaultPartitions[k] = v
		}
	}
	return out
}

// State is the schema at one point of the migration.
type State struct {
	Model   *schema.Model
	Catalog *Catalog
	// SearchPath resolves unqualified names, as SET search_path changes it.
	SearchPath []string

	base *schema.Model
	// created holds relations created by an applied statement (not in the base).
	created map[QName]bool
	// renamed maps relation -> current column name -> name in the base.
	renamed map[QName]map[string]string
	// Skipped counts statements with no modeled effect, by kind (for dumps).
	Skipped map[string]int
	// Warnings are problems found while applying (unknown relations, ...).
	Warnings []string
}

// DefaultSearchPath is PostgreSQL's default: "$user" (rarely a schema), public.
var DefaultSearchPath = []string{"public"}

// New starts from a deep copy of base.
func New(base *schema.Model, cat *Catalog) *State {
	if base == nil {
		base = &schema.Model{Schemas: []string{}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	}
	return &State{
		Model: base.Clone(), Catalog: cat.clone(), SearchPath: slices.Clone(DefaultSearchPath),
		base: base.Clone(), created: map[QName]bool{}, renamed: map[QName]map[string]string{},
		Skipped: map[string]int{},
	}
}

// Base is the model the state started from.
func (s *State) Base() *schema.Model { return s.base }

// Created reports whether q was created by a statement applied so far.
func (s *State) Created(q QName) bool { return s.created[q] }

func (s *State) warnf(format string, args ...any) {
	s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
}

// Lookup finds a relation by qualified name.
func (s *State) Lookup(q QName) *schema.Relation { return s.Model.Relation(q) }

// Resolve qualifies a RangeVar against the search path. For an unqualified
// name it returns the first search-path schema holding such a relation (or
// a partition of one), else the first search-path schema.
func (s *State) Resolve(rv *pg_query.RangeVar) QName {
	return s.resolveName(rv.GetSchemaname(), rv.GetRelname())
}

func (s *State) resolveName(nsp, name string) QName {
	if nsp != "" {
		return QName{Schema: nsp, Name: name}
	}
	for _, sp := range s.SearchPath {
		q := QName{Schema: sp, Name: name}
		if s.Lookup(q) != nil || s.PartitionParent(q) != nil {
			return q
		}
	}
	first := "public"
	if len(s.SearchPath) > 0 {
		first = s.SearchPath[0]
	}
	return QName{Schema: first, Name: name}
}

// PartitionParent returns the partitioned table that lists q as a partition.
func (s *State) PartitionParent(q QName) *schema.Relation {
	for i := range s.Model.Relations {
		if slices.Contains(s.Model.Relations[i].Partitions, q) {
			return &s.Model.Relations[i]
		}
	}
	return nil
}

// FindIndex resolves an index name to its table.
func (s *State) FindIndex(nsp, name string) (*schema.Relation, *schema.Index) {
	schemas := s.SearchPath
	if nsp != "" {
		schemas = []string{nsp}
	}
	for _, sp := range schemas {
		for i := range s.Model.Relations {
			r := &s.Model.Relations[i]
			if r.Schema != sp {
				continue
			}
			for j := range r.Indexes {
				if r.Indexes[j].Name == name {
					return r, &r.Indexes[j]
				}
			}
		}
	}
	return nil, nil
}

// Referencing lists tables with a foreign key to q, sorted.
func (s *State) Referencing(q QName) []QName {
	var out []QName
	for _, r := range s.Model.Relations {
		for _, fk := range r.ForeignKeys {
			if fk.Ref == q && r.QName() != q && !fk.Inferred && !slices.Contains(out, r.QName()) {
				out = append(out, r.QName())
			}
		}
	}
	slices.SortFunc(out, cmpQName)
	return out
}

// Dependents lists views and matviews that read q, directly or through
// other views, sorted.
func (s *State) Dependents(q QName) []QName {
	seen := map[QName]bool{}
	var walk func(QName)
	walk = func(t QName) {
		for _, r := range s.Model.Relations {
			if slices.Contains(r.DependsOn, t) && !seen[r.QName()] {
				seen[r.QName()] = true
				walk(r.QName())
			}
		}
	}
	walk(q)
	out := make([]QName, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.SortFunc(out, cmpQName)
	return out
}

func cmpQName(a, b QName) int {
	return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
}

// Empty reports whether q is known to have no rows: it was created during
// the migration, or its row estimate is zero. Unknown counts are not empty.
func (s *State) Empty(q QName) bool {
	if s.created[q] {
		return true
	}
	r := s.Lookup(q)
	return r != nil && r.RowEstimate != nil && *r.RowEstimate == 0 && r.Pages == 0
}

// Enum finds an enum type.
func (s *State) Enum(nsp, name string) *schema.Enum {
	schemas := s.SearchPath
	if nsp != "" {
		schemas = []string{nsp}
	}
	for _, sp := range schemas {
		for i := range s.Model.Enums {
			if s.Model.Enums[i].Schema == sp && s.Model.Enums[i].Name == name {
				return &s.Model.Enums[i]
			}
		}
	}
	return nil
}

// ConstrainedDomain reports whether the type is a domain with constraints.
func (s *State) ConstrainedDomain(t Type) bool {
	if t.Schema != "" {
		return s.Catalog.ConstrainedDomains[t.Schema+"."+t.Base]
	}
	for _, sp := range append(slices.Clone(s.SearchPath), "public") {
		if s.Catalog.ConstrainedDomains[sp+"."+t.Base] {
			return true
		}
	}
	return false
}

// RangeVars returns the relations a query reads (every RangeVar in the
// tree), qualified against the search path, sorted and deduplicated.
// Unqualified names that match a CTE of the query are left out.
func (s *State) RangeVars(n *pg_query.Node) []QName {
	if n == nil {
		return nil
	}
	ctes := map[string]bool{}
	var rvs []*pg_query.RangeVar
	walk(n.ProtoReflect(), func(m protoreflect.Message) {
		switch v := m.Interface().(type) {
		case *pg_query.CommonTableExpr:
			ctes[v.GetCtename()] = true
		case *pg_query.RangeVar:
			rvs = append(rvs, v)
		}
	})
	var out []QName
	for _, rv := range rvs {
		if rv.GetSchemaname() == "" && ctes[rv.GetRelname()] {
			continue
		}
		q := s.Resolve(rv)
		if p := s.PartitionParent(q); p != nil {
			q = p.QName()
		}
		if !slices.Contains(out, q) {
			out = append(out, q)
		}
	}
	slices.SortFunc(out, cmpQName)
	return out
}

// walk visits every message in the tree through protobuf reflection, like
// internal/query/guard's walker.
func walk(m protoreflect.Message, visit func(protoreflect.Message)) {
	visit(m)
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true
		}
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				walk(l.Get(i).Message(), visit)
			}
		case fd.IsMap():
		default:
			walk(v.Message(), visit)
		}
		return true
	})
}

// Walk exposes the reflection walker to the classifier.
func Walk(n *pg_query.Node, visit func(protoreflect.Message)) {
	if n != nil {
		walk(n.ProtoReflect(), visit)
	}
}

// Deparse renders an expression node as SQL text, or "" when libpg_query
// cannot deparse it.
func Deparse(expr *pg_query.Node) (out string) {
	if expr == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	tree := &pg_query.ParseResult{Stmts: []*pg_query.RawStmt{{Stmt: &pg_query.Node{Node: &pg_query.Node_SelectStmt{
		SelectStmt: &pg_query.SelectStmt{TargetList: []*pg_query.Node{{Node: &pg_query.Node_ResTarget{
			ResTarget: &pg_query.ResTarget{Val: expr}}}}},
	}}}}}
	s, err := pgquery.Deparse(tree)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(s, "SELECT ")
}

// DeparseStmt renders a whole statement.
func DeparseStmt(n *pg_query.Node) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	s, err := pgquery.Deparse(&pg_query.ParseResult{Stmts: []*pg_query.RawStmt{{Stmt: n}}})
	if err != nil {
		return ""
	}
	return s
}

func strs(nodes []*pg_query.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.GetString_().GetSval())
	}
	return out
}

// QualifiedFromList converts a name list (["schema","name"] or ["name"]).
func QualifiedFromList(nodes []*pg_query.Node) (string, string) {
	names := strs(nodes)
	switch len(names) {
	case 0:
		return "", ""
	case 1:
		return "", names[0]
	default:
		return names[len(names)-2], names[len(names)-1]
	}
}

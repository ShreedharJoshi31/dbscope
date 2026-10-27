package apply

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate/parse"
	"github.com/dbscope/dbscope/internal/schema"
)

// Diff is the change from the base model to the current one, in the API shape.
type Diff struct {
	Added   []string       `json:"added"`
	Dropped []string       `json:"dropped"`
	Altered []string       `json:"altered"`
	Columns []ColumnChange `json:"columns"`
}

// ColumnChange is one column-level change of an altered relation. For a
// rename, Column is the name in the base and To the new name.
type ColumnChange struct {
	Relation string `json:"relation"`
	Column   string `json:"column"`
	Change   string `json:"change"` // added | dropped | altered | renamed
	To       string `json:"to,omitempty"`
}

// structure is the part of a relation a migration can change; estimates
// and sizes are left out.
type structure struct {
	Kind         schema.RelKind
	Comment      string
	Columns      []schema.Column
	PrimaryKey   []string
	ForeignKeys  []schema.ForeignKey
	Uniques      [][]string
	Checks       []schema.Check
	Indexes      []schema.Index
	PartitionKey string
	Partitions   []QName
	DependsOn    []QName
}

func structOf(r *schema.Relation) structure {
	norm := func(x []string) []string {
		if len(x) == 0 {
			return nil
		}
		return x
	}
	st := structure{Kind: r.Kind, Comment: r.Comment, Columns: r.Columns, PrimaryKey: norm(r.PrimaryKey),
		PartitionKey: r.PartitionKey}
	if len(r.ForeignKeys) > 0 {
		st.ForeignKeys = r.ForeignKeys
	}
	if len(r.Uniques) > 0 {
		st.Uniques = r.Uniques
	}
	if len(r.Checks) > 0 {
		st.Checks = r.Checks
	}
	if len(r.Indexes) > 0 {
		st.Indexes = r.Indexes
	}
	if len(r.Partitions) > 0 {
		st.Partitions = r.Partitions
	}
	if len(r.DependsOn) > 0 {
		st.DependsOn = r.DependsOn
	}
	return st
}

// Diff compares the current model with the base.
func (s *State) Diff() Diff {
	d := Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []ColumnChange{}}
	for i := range s.Model.Relations {
		r := &s.Model.Relations[i]
		b := s.base.Relation(r.QName())
		if b == nil {
			d.Added = append(d.Added, r.QName().String())
			continue
		}
		if !reflect.DeepEqual(structOf(b), structOf(r)) {
			d.Altered = append(d.Altered, r.QName().String())
			d.Columns = append(d.Columns, s.columnChanges(b, r)...)
		}
	}
	for i := range s.base.Relations {
		b := &s.base.Relations[i]
		if s.Model.Relation(b.QName()) == nil {
			d.Dropped = append(d.Dropped, b.QName().String())
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Dropped)
	sort.Strings(d.Altered)
	return d
}

func (s *State) columnChanges(b, r *schema.Relation) []ColumnChange {
	key := r.QName().String()
	renamed := s.renamed[r.QName()]
	var out []ColumnChange
	used := map[string]bool{}
	for _, c := range r.Columns {
		orig := c.Name
		if o, ok := renamed[c.Name]; ok {
			orig = o
		}
		bc := b.Column(orig)
		if bc == nil {
			out = append(out, ColumnChange{Relation: key, Column: c.Name, Change: "added"})
			continue
		}
		used[orig] = true
		if orig != c.Name {
			out = append(out, ColumnChange{Relation: key, Column: orig, Change: "renamed", To: c.Name})
		}
		x, y := *bc, c
		x.Name, y.Name = "", ""
		if x != y {
			out = append(out, ColumnChange{Relation: key, Column: c.Name, Change: "altered"})
		}
	}
	for _, c := range b.Columns {
		if !used[c.Name] {
			out = append(out, ColumnChange{Relation: key, Column: c.Name, Change: "dropped"})
		}
	}
	slices.SortStableFunc(out, func(p, q ColumnChange) int { return strings.Compare(p.Column, q.Column) })
	return out
}

// OriginalColumn maps a column's current name to its name in the base.
func (s *State) OriginalColumn(q QName, name string) string {
	if o, ok := s.renamed[q][name]; ok {
		return o
	}
	return name
}

var dumpedFrom = regexp.MustCompile(`(?m)^-- Dumped from database version (\d+)(?:\.(\d+))?`)

// Dump is a base schema built from `pg_dump --schema-only` output.
type Dump struct {
	Model   *schema.Model
	Catalog *Catalog
	// Warnings report statements dbscope could not model, counted by kind,
	// and statements that failed to parse.
	Warnings []string
}

// LoadDump applies a schema-only dump to an empty model. The server
// version comes from the dump header when present (0 otherwise).
func LoadDump(text string) *Dump {
	s := New(nil, nil)
	f := parse.Split(text)
	var parseErrors int
	for _, st := range f.Statements {
		if st.Err != nil {
			parseErrors++
			continue
		}
		s.Apply(st.Node)
	}
	m := s.Model
	if v := dumpedFrom.FindStringSubmatch(text); v != nil {
		major, _ := strconv.Atoi(v[1])
		minor, _ := strconv.Atoi(v[2])
		m.ServerVersion = major*10000 + minor
	}
	slices.SortFunc(m.Relations, func(a, b schema.Relation) int { return cmpQName(a.QName(), b.QName()) })
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Kind != schema.KindView {
			r.Pages = 0
		}
	}
	if !slices.Contains(m.Schemas, "public") && hasSchema(m, "public") {
		m.Schemas = append(m.Schemas, "public")
	}
	sort.Strings(m.Schemas)
	d := &Dump{Model: m, Catalog: s.Catalog}
	if n := sum(s.Skipped); n > 0 {
		d.Warnings = append(d.Warnings, fmt.Sprintf("schema dump: %d statements were not modeled (%s)", n, counts(s.Skipped)))
	}
	if parseErrors > 0 {
		d.Warnings = append(d.Warnings, fmt.Sprintf("schema dump: %d statements could not be parsed with the PostgreSQL 17 grammar and were skipped", parseErrors))
	}
	d.Warnings = append(d.Warnings, s.Warnings...)
	return d
}

func hasSchema(m *schema.Model, nsp string) bool {
	return slices.ContainsFunc(m.Relations, func(r schema.Relation) bool { return r.Schema == nsp })
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s x%d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

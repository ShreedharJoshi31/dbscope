// Package schema is the in-memory database model shared by every mode:
// introspection produces it, migration analysis mutates a copy of it, and the
// NL layer and canvas read it. It holds structure only, never row data.
package schema

import (
	"fmt"
	"slices"
)

type RelKind string

const (
	KindTable            RelKind = "table"
	KindPartitionedTable RelKind = "partitioned_table"
	KindView             RelKind = "view"
	KindMatView          RelKind = "materialized_view"
	KindForeignTable     RelKind = "foreign_table"
)

type Model struct {
	ServerVersion int        `json:"serverVersion"` // server_version_num, e.g. 170002
	Schemas       []string   `json:"schemas"`
	Relations     []Relation `json:"relations"`
	Enums         []Enum     `json:"enums"`
}

type Relation struct {
	Schema  string  `json:"schema"`
	Name    string  `json:"name"`
	Kind    RelKind `json:"kind"`
	Comment string  `json:"comment,omitempty"`
	// RowEstimate is pg_class.reltuples; nil when the table was never analyzed.
	RowEstimate *int64       `json:"rowEstimate"`
	Pages       int64        `json:"pages"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primaryKey,omitempty"`
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
	Uniques     [][]string   `json:"uniques,omitempty"`
	Checks      []Check      `json:"checks,omitempty"`
	Indexes     []Index      `json:"indexes,omitempty"`
	// Partitions lists child relations collapsed under a partitioned parent.
	Partitions []QualifiedName `json:"partitions,omitempty"`
	// DependsOn holds the relations a view or matview reads from.
	DependsOn []QualifiedName `json:"dependsOn,omitempty"`
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // format_type() output, e.g. "character varying(255)"
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
	Identity bool   `json:"identity,omitempty"`
	// Generated is the expression of a stored generated column.
	Generated string `json:"generated,omitempty"`
	Comment   string `json:"comment,omitempty"`
}

type ForeignKey struct {
	Name      string        `json:"name"`
	Columns   []string      `json:"columns"`
	Ref       QualifiedName `json:"ref"`
	RefCols   []string      `json:"refColumns"`
	OnDelete  string        `json:"onDelete,omitempty"`
	Validated bool          `json:"validated"`
	// Inferred marks a relationship guessed from naming, not declared in the catalog.
	Inferred   bool    `json:"inferred,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

type Check struct {
	Name      string `json:"name"`
	Expr      string `json:"expr"`
	Validated bool   `json:"validated"`
}

type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"` // expression indexes appear as their expression text
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Method  string   `json:"method"` // btree, gin, ...
	Partial string   `json:"partial,omitempty"`
	Valid   bool     `json:"valid"`
}

type Enum struct {
	Schema string   `json:"schema"`
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type QualifiedName struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

func (q QualifiedName) String() string { return fmt.Sprintf("%s.%s", q.Schema, q.Name) }

func (r *Relation) QName() QualifiedName { return QualifiedName{r.Schema, r.Name} }

func (r *Relation) Column(name string) *Column {
	for i := range r.Columns {
		if r.Columns[i].Name == name {
			return &r.Columns[i]
		}
	}
	return nil
}

func (m *Model) Relation(q QualifiedName) *Relation {
	for i := range m.Relations {
		if m.Relations[i].Schema == q.Schema && m.Relations[i].Name == q.Name {
			return &m.Relations[i]
		}
	}
	return nil
}

// Clone returns a deep copy so migration analysis can mutate it freely.
func (m *Model) Clone() *Model {
	c := *m
	c.Schemas = slices.Clone(m.Schemas)
	c.Enums = slices.Clone(m.Enums)
	for i := range c.Enums {
		c.Enums[i].Values = slices.Clone(c.Enums[i].Values)
	}
	c.Relations = slices.Clone(m.Relations)
	for i := range c.Relations {
		r := &c.Relations[i]
		if r.RowEstimate != nil {
			v := *r.RowEstimate
			r.RowEstimate = &v
		}
		r.Columns = slices.Clone(r.Columns)
		r.PrimaryKey = slices.Clone(r.PrimaryKey)
		r.ForeignKeys = slices.Clone(r.ForeignKeys)
		for j := range r.ForeignKeys {
			r.ForeignKeys[j].Columns = slices.Clone(r.ForeignKeys[j].Columns)
			r.ForeignKeys[j].RefCols = slices.Clone(r.ForeignKeys[j].RefCols)
		}
		r.Uniques = slices.Clone(r.Uniques)
		for j := range r.Uniques {
			r.Uniques[j] = slices.Clone(r.Uniques[j])
		}
		r.Checks = slices.Clone(r.Checks)
		r.Indexes = slices.Clone(r.Indexes)
		for j := range r.Indexes {
			r.Indexes[j].Columns = slices.Clone(r.Indexes[j].Columns)
		}
		r.Partitions = slices.Clone(r.Partitions)
		r.DependsOn = slices.Clone(r.DependsOn)
	}
	return &c
}

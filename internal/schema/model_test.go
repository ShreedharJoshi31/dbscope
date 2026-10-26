package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func sample() *Model {
	n := int64(42)
	return &Model{
		ServerVersion: 170002,
		Schemas:       []string{"public"},
		Relations: []Relation{
			{Schema: "public", Name: "users", Kind: KindTable, RowEstimate: &n,
				Columns:    []Column{{Name: "id", Type: "bigint"}},
				PrimaryKey: []string{"id"},
				Uniques:    [][]string{{"id"}},
				Indexes:    []Index{{Name: "users_pkey", Columns: []string{"id"}, Include: []string{"x"}, Primary: true}}},
			{Schema: "public", Name: "orders", Kind: KindTable,
				Columns:     []Column{{Name: "user_id", Type: "bigint"}},
				ForeignKeys: []ForeignKey{{Name: "fk", Columns: []string{"user_id"}, Ref: QualifiedName{"public", "users"}, RefCols: []string{"id"}}}},
		},
		Enums: []Enum{{Schema: "public", Name: "status", Values: []string{"a"}}},
	}
}

func TestCloneIsDeep(t *testing.T) {
	m := sample()
	before, _ := json.Marshal(m)
	c := m.Clone()

	*c.Relations[0].RowEstimate = 1
	c.Relations[0].Columns[0].Name = "x"
	c.Relations[0].PrimaryKey[0] = "x"
	c.Relations[0].Uniques[0][0] = "x"
	c.Relations[0].Indexes[0].Columns[0] = "x"
	c.Relations[0].Indexes[0].Include[0] = "y"
	c.Relations[1].ForeignKeys[0].Columns[0] = "x"
	c.Relations[1].ForeignKeys[0].RefCols[0] = "x"
	c.Enums[0].Values[0] = "x"
	c.Schemas[0] = "x"

	after, _ := json.Marshal(m)
	if string(before) != string(after) {
		t.Fatalf("clone mutation leaked into original:\n%s\n%s", before, after)
	}
}

func TestCloneEqual(t *testing.T) {
	m := sample()
	if !reflect.DeepEqual(m, m.Clone()) {
		t.Fatal("clone differs from original")
	}
}

func TestLookup(t *testing.T) {
	m := sample()
	r := m.Relation(QualifiedName{"public", "users"})
	if r == nil || r.Column("id") == nil || r.Column("nope") != nil {
		t.Fatal("lookup failed")
	}
	if m.Relation(QualifiedName{"other", "users"}) != nil {
		t.Fatal("schema must be part of the key")
	}
}

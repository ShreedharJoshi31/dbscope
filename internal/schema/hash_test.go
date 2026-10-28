package schema

import "testing"

func TestStructureHash(t *testing.T) {
	n := int64(10)
	base := func() *Model {
		return &Model{Relations: []Relation{{Schema: "s", Name: "t", Kind: KindTable, RowEstimate: &n, Comment: "1 MiB",
			Columns: []Column{{Name: "id", Type: "int"}}, Indexes: []Index{{Name: "b"}, {Name: "a"}}}}}
	}
	h := StructureHash(base())
	m := base()
	m.Relations[0].Comment, m.Relations[0].RowEstimate, m.DurationMs = "2 MiB", nil, 99
	m.Relations[0].Indexes = []Index{{Name: "a"}, {Name: "b"}}
	m.Relations[0].ForeignKeys = []ForeignKey{{Name: "guess", Inferred: true}}
	if StructureHash(m) != h {
		t.Error("counts, comments, index order or inferred keys changed the hash")
	}
	m.Relations[0].Columns[0].Type = "bigint"
	if StructureHash(m) == h {
		t.Error("a column type change kept the hash")
	}
}

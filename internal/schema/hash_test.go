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

// Time travel flags describe what the reading user may do, not the design, so
// turning detection on (or a grant changing) must not save a new snapshot.
func TestStructureHashIgnoresTimeTravel(t *testing.T) {
	base := func() *Model {
		return &Model{Relations: []Relation{
			{Schema: "dbo", Name: "acct", Kind: KindTable, Columns: []Column{{Name: "id", Type: "int"}}},
			{Schema: "dbo", Name: "acct_history", Kind: KindTable, Columns: []Column{{Name: "id", Type: "int"}}},
		}}
	}
	h := StructureHash(base())
	m := base()
	m.TimeTravel = &ModelTimeTravel{Supported: true}
	m.Relations[0].TimeTravel = &TimeTravel{Kind: TimeTravelTemporal, Available: false, Retention: "3 months",
		Note: "Needs SELECT on the history table dbo.acct_history"}
	m.Relations[1].TimeTravel = &TimeTravel{Kind: TimeTravelHistory, Available: true, HistoryOf: "dbo.acct"}
	if StructureHash(m) != h {
		t.Error("time travel flags changed the hash")
	}
	m.Relations[0].TimeTravel.Available = true
	if StructureHash(m) != h {
		t.Error("a time travel privilege change changed the hash")
	}
}

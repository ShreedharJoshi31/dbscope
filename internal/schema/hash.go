package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// StructureHash identifies a model's structure: relations, columns, keys,
// indexes and findings. Counts, sizes, comments (which carry sizes and part
// counts) and timings are left out, so it changes only when the design does.
// Inferred foreign keys are left out too: they follow the user's decisions.
func StructureHash(m *Model) string {
	type col struct {
		Name, Type, Default, Generated string
		Nullable                       bool
	}
	type fk struct {
		Name    string
		Columns []string
		Ref     QualifiedName
		RefCols []string
	}
	type idx struct {
		Name, Method string
		Columns      []string
		Unique       bool
	}
	type finding struct{ RuleID, Column string }
	type rel struct {
		Schema, Name, Kind, PartitionKey string
		Columns                          []col
		PrimaryKey                       []string
		ForeignKeys                      []fk
		Uniques                          [][]string
		Indexes                          []idx
		Findings                         []finding
	}
	var rels []rel
	for _, r := range m.Relations {
		if r.Stub {
			continue
		}
		x := rel{Schema: r.Schema, Name: r.Name, Kind: string(r.Kind), PartitionKey: r.PartitionKey, PrimaryKey: r.PrimaryKey, Uniques: r.Uniques}
		for _, c := range r.Columns {
			x.Columns = append(x.Columns, col{c.Name, c.Type, c.Default, c.Generated, c.Nullable})
		}
		for _, f := range r.ForeignKeys {
			if !f.Inferred {
				x.ForeignKeys = append(x.ForeignKeys, fk{f.Name, f.Columns, f.Ref, f.RefCols})
			}
		}
		for _, i := range r.Indexes {
			x.Indexes = append(x.Indexes, idx{i.Name, i.Method, i.Columns, i.Unique})
		}
		for _, f := range r.Findings {
			x.Findings = append(x.Findings, finding{f.RuleID, f.Column})
		}
		sort.Slice(x.Indexes, func(a, b int) bool { return x.Indexes[a].Name < x.Indexes[b].Name })
		sort.Slice(x.Findings, func(a, b int) bool {
			return x.Findings[a].RuleID+x.Findings[a].Column < x.Findings[b].RuleID+x.Findings[b].Column
		})
		rels = append(rels, x)
	}
	sort.Slice(rels, func(a, b int) bool { return rels[a].Schema+"."+rels[a].Name < rels[b].Schema+"."+rels[b].Name })
	b, _ := json.Marshal(rels)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

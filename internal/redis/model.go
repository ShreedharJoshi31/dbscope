package redis

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/schema"
)

// Model shows the key patterns in the shared model, so the canvas and the
// explorer map the keyspace: each pattern is a node whose rows are its type,
// memory and expiry. Redis has no relationships, so there are no edges.
func (r *Report) Model(db int) *schema.Model {
	s := "db" + strconv.Itoa(db)
	m := &schema.Model{Engine: r.Server.Flavor, ServerVersion: r.Server.Num, Schemas: []string{s}, Relations: []schema.Relation{}, Enums: []schema.Enum{},
		IntrospectedAt: r.At, DurationMs: r.TookMs}
	for _, p := range r.Patterns {
		n := int64(p.Keys)
		var types []string
		for t, c := range p.Types {
			types = append(types, fmt.Sprintf("%s %d", t, c))
		}
		sort.Strings(types)
		expiry := "all expire"
		switch {
		case p.NoTTL == p.Keys:
			expiry = "none expire"
		case p.NoTTL > 0:
			expiry = fmt.Sprintf("%d of %d never expire", p.NoTTL, p.Keys)
		}
		rel := schema.Relation{Schema: s, Name: p.Pattern, Kind: schema.KindKeyPattern, RowEstimate: &n,
			Columns: []schema.Column{
				{Name: "type", Type: strings.Join(types, ", ")},
				{Name: "memory", Type: bytesText(p.Bytes)},
				{Name: "expiry", Type: expiry},
			}}
		if p.Largest != nil {
			rel.Comment = fmt.Sprintf("largest: %s (%s)", p.Largest.Name, bytesText(p.Largest.Bytes))
		}
		m.Relations = append(m.Relations, rel)
	}
	return m
}

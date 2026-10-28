package mongo

import (
	"fmt"
	"strings"

	"github.com/dbscope/dbscope/internal/schema"
)

// IndexInfo is one listIndexes entry.
type IndexInfo struct {
	Name    string
	Keys    []string // "field" or "field:-1", "field:text", ...
	Unique  bool
	Partial string // the partialFilterExpression as JSON
	// TTL is expireAfterSeconds, when set.
	TTL *int64
}

// Collection is what dbscope knows about one collection.
type Collection struct {
	Name      string
	Kind      string // collection, view, timeseries
	Count     int64  // the estimated document count
	Shape     Shape
	Indexes   []IndexInfo
	Validator string // the validator as JSON, if any
	ViewOn    string
}

// Relation shows a collection in the shared model: each field path is a
// column whose type is the mix of BSON types seen and whose comment gives
// its coverage of the sample; _id is the primary key.
func (c Collection) Relation(db string) schema.Relation {
	r := schema.Relation{Schema: db, Name: c.Name, Kind: schema.KindCollection}
	if c.Kind == "view" {
		r.Kind = schema.KindView
		if c.ViewOn != "" {
			r.DependsOn = []schema.QualifiedName{{Schema: db, Name: c.ViewOn}}
		}
	} else {
		n := c.Count
		r.RowEstimate = &n
	}
	if c.Kind == "timeseries" {
		r.Comment = "time series collection"
	}
	for _, f := range c.Shape.Fields {
		cov := c.Shape.Coverage(f)
		col := schema.Column{Name: f.Path, Type: f.TypeText(), Nullable: cov < 100 || f.Types["null"] > 0}
		if cov < 100 {
			col.Comment = fmt.Sprintf("in %d%% of %d sampled documents", cov, c.Shape.Sampled)
		}
		r.Columns = append(r.Columns, col)
	}
	if c.Kind != "view" {
		r.PrimaryKey = []string{"_id"}
	}
	for _, i := range c.Indexes {
		idx := schema.Index{Name: i.Name, Columns: i.Keys, Unique: i.Unique || i.Name == "_id_", Primary: i.Name == "_id_",
			Method: "btree", Partial: i.Partial, Valid: true}
		for _, k := range i.Keys {
			if _, kind, ok := strings.Cut(k, ":"); ok && kind != "1" && kind != "-1" {
				idx.Method = kind // text, 2dsphere, hashed, ...
			}
		}
		r.Indexes = append(r.Indexes, idx)
		if idx.Unique && !idx.Primary {
			var cols []string
			for _, k := range i.Keys {
				name, _, _ := strings.Cut(k, ":")
				cols = append(cols, name)
			}
			r.Uniques = append(r.Uniques, cols)
		}
	}
	if c.Validator != "" {
		r.Checks = []schema.Check{{Name: "validator", Expr: c.Validator, Validated: true}}
	}
	return r
}

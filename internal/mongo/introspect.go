package mongo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/schema"
)

// SampleSize is how many documents per collection the schema is inferred from.
const SampleSize = 200

// maxCollections bounds how many collections one read samples.
const maxCollections = 300

// Namespaces lists the server's databases other than its own.
func Namespaces(ctx context.Context, c *mongo.Client) ([]introspect.Namespace, error) {
	res, err := c.ListDatabases(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		return nil, err
	}
	out := []introspect.Namespace{}
	for _, d := range res.Databases {
		if d.Name == "admin" || d.Name == "local" || d.Name == "config" {
			continue
		}
		names, err := c.Database(d.Name).ListCollectionNames(ctx, bson.D{}, options.ListCollections().SetAuthorizedCollections(true))
		if err != nil {
			names = nil
		}
		out = append(out, introspect.Namespace{Name: d.Name, RelationCount: len(names)})
	}
	return out, nil
}

// Introspect reads a database's collections: the catalog, document counts,
// indexes, and a schema inferred from a sample of each collection, plus
// references guessed from ObjectId fields whose values exist in another
// collection.
func Introspect(ctx context.Context, c *mongo.Client, db string) (*schema.Model, error) {
	start := time.Now()
	srv, err := describe(ctx, c, db)
	if err != nil {
		return nil, err
	}
	d := c.Database(db)
	cur, err := d.ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var specs []struct {
		Name    string `bson:"name"`
		Type    string `bson:"type"`
		Options struct {
			Validator bson.Raw `bson:"validator"`
			ViewOn    string   `bson:"viewOn"`
		} `bson:"options"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		return nil, err
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	m := &schema.Model{Engine: "mongodb", ServerVersion: srv.Num, Schemas: []string{db}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	var cols []Collection
	for _, s := range specs {
		if strings.HasPrefix(s.Name, "system.") || len(cols) == maxCollections {
			continue
		}
		col := Collection{Name: s.Name, Kind: s.Type, ViewOn: s.Options.ViewOn}
		if len(s.Options.Validator) > 0 {
			if b, err := bson.MarshalExtJSON(s.Options.Validator, false, false); err == nil {
				col.Validator = string(b)
			}
		}
		coll := d.Collection(s.Name)
		if s.Type != "view" {
			if n, err := coll.EstimatedDocumentCount(ctx); err == nil {
				col.Count = n
			}
			if col.Indexes, err = indexes(ctx, coll); err != nil {
				return nil, fmt.Errorf("indexes of %s: %w", s.Name, err)
			}
		}
		docs, err := sample(ctx, coll, col.Count, s.Type == "view")
		if err != nil {
			return nil, fmt.Errorf("sampling %s: %w", s.Name, err)
		}
		col.Shape = Infer(docs)
		cols = append(cols, col)
	}
	for _, col := range cols {
		m.Relations = append(m.Relations, col.Relation(db))
	}
	inferReferences(ctx, d, cols, m)
	m.IntrospectedAt = time.Now().UTC()
	m.DurationMs = time.Since(start).Milliseconds()
	return m, nil
}

// sample returns up to SampleSize documents. $sample is a cheap random
// cursor only when it is first and asks for under 5% of more than 100
// documents; otherwise it sorts randomly, so disk use is refused. A small
// collection or a view (where $sample always scans) is read with a limit.
func sample(ctx context.Context, coll *mongo.Collection, count int64, view bool) ([]bson.Raw, error) {
	var cur *mongo.Cursor
	var err error
	if view || count <= SampleSize {
		cur, err = coll.Find(ctx, bson.D{}, options.Find().SetLimit(SampleSize).SetComment("dbscope schema sample"))
	} else {
		cur, err = coll.Aggregate(ctx, mongo.Pipeline{{{Key: "$sample", Value: bson.D{{Key: "size", Value: SampleSize}}}}},
			options.Aggregate().SetAllowDiskUse(false).SetComment("dbscope schema sample"))
	}
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []bson.Raw
	for cur.Next(ctx) {
		out = append(out, append(bson.Raw(nil), cur.Current...))
	}
	return out, cur.Err()
}

func indexes(ctx context.Context, coll *mongo.Collection) ([]IndexInfo, error) {
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []IndexInfo
	for cur.Next(ctx) {
		var spec struct {
			Name    string   `bson:"name"`
			Key     bson.Raw `bson:"key"`
			Unique  bool     `bson:"unique"`
			Partial bson.Raw `bson:"partialFilterExpression"`
			TTL     *int64   `bson:"expireAfterSeconds"`
		}
		if err := cur.Decode(&spec); err != nil {
			return nil, err
		}
		i := IndexInfo{Name: spec.Name, Unique: spec.Unique, TTL: spec.TTL}
		elems, _ := spec.Key.Elements()
		for _, e := range elems {
			v := e.Value()
			switch v.Type {
			case bson.TypeString:
				i.Keys = append(i.Keys, e.Key()+":"+v.StringValue())
			default:
				if n, ok := v.AsInt64OK(); ok && n < 0 {
					i.Keys = append(i.Keys, e.Key()+":-1")
				} else {
					i.Keys = append(i.Keys, e.Key())
				}
			}
		}
		if len(spec.Partial) > 0 {
			if b, err := bson.MarshalExtJSON(spec.Partial, false, false); err == nil {
				i.Partial = string(b)
			}
		}
		out = append(out, i)
	}
	return out, cur.Err()
}

// inferReferences guesses foreign keys: a top-level field holding only
// ObjectIds (or nulls), named after another collection (customer, customerId,
// customer_id for customers), whose sampled values mostly exist as that
// collection's _id. The share found becomes the confidence.
func inferReferences(ctx context.Context, d *mongo.Database, cols []Collection, m *schema.Model) {
	byStem := map[string]string{}
	for _, c := range cols {
		if c.Kind != "view" {
			byStem[stem(c.Name)] = c.Name
		}
	}
	for i, c := range cols {
		for _, f := range c.Shape.Fields {
			if f.Path == "_id" || !topLevel(f.Path) || f.Types["objectId"] == 0 || !onlyTypes(f, []string{"objectId"}) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(f.Path, "_id"), "Id"), "ID")
			target, ok := byStem[stem(name)]
			if !ok || name == f.Path {
				continue
			}
			share := matchShare(ctx, d, c.Name, f.Path, target)
			if share < 0.5 {
				continue
			}
			m.Relations[i].ForeignKeys = append(m.Relations[i].ForeignKeys, schema.ForeignKey{
				Name: c.Name + "_" + f.Path, Columns: []string{f.Path}, Ref: schema.QualifiedName{Schema: d.Name(), Name: target},
				RefCols: []string{"_id"}, Inferred: true, Confidence: share,
			})
		}
	}
}

// stem lower-cases a name and drops separators and a plural s.
func stem(s string) string {
	s = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s))
	if strings.HasSuffix(s, "ies") {
		return strings.TrimSuffix(s, "ies") + "y"
	}
	return strings.TrimSuffix(s, "s")
}

// matchShare is the share of the distinct ids among up to 50 values of field that exist as _id in target.
func matchShare(ctx context.Context, d *mongo.Database, from, field, target string) float64 {
	cur, err := d.Collection(from).Find(ctx, bson.D{{Key: field, Value: bson.D{{Key: "$type", Value: "objectId"}}}},
		options.Find().SetProjection(bson.D{{Key: field, Value: 1}}).SetLimit(50).SetComment("dbscope reference check"))
	if err != nil {
		return 0
	}
	var vals bson.A
	seen := map[bson.ObjectID]bool{}
	for cur.Next(ctx) {
		// Distinct ids: many documents referencing one parent must not lower the share.
		if id, ok := cur.Current.Lookup(field).ObjectIDOK(); ok && !seen[id] {
			seen[id] = true
			vals = append(vals, id)
		}
	}
	cur.Close(ctx)
	if len(vals) == 0 {
		return 0
	}
	n, err := d.Collection(target).CountDocuments(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: vals}}}})
	if err != nil {
		return 0
	}
	return float64(n) / float64(len(vals))
}

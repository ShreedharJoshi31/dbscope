package mongo

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/dbscope/dbscope/internal/schema"
)

const (
	// rareShare and rareSample are dbscope's heuristics, not documented limits:
	// a field in under 5% of a sample of at least 100 documents is more likely
	// a typo or leftover than a design.
	rareShare  = 5
	rareSample = 100
	// longArray is dbscope's own bound, not a documented limit. A document is
	// at most 16 MB (https://www.mongodb.com/docs/manual/reference/limits/),
	// and a multikey index holds a key per array element
	// (https://www.mongodb.com/docs/manual/core/indexes/index-types/index-multikey/).
	longArray = 1_000
	// indexStatsTimeout bounds the $indexStats read of one collection.
	indexStatsTimeout = 5 * time.Second
)

// unusedIndexAfter is how long an index's counters must have run with no
// access before it counts as unused; a day gives daily jobs a chance to use
// it. Counters start at index creation or mongod restart (accesses.since,
// https://www.mongodb.com/docs/manual/reference/operator/aggregation/indexStats/).
var unusedIndexAfter = 24 * time.Hour

// numeric types compare as one type in queries and sorts
// (https://www.mongodb.com/docs/manual/reference/bson-type-comparison-order/),
// so a mix of only these is not reported.
var numeric = []string{"int", "long", "double", "decimal"}

// addFindings records design risks a collection's sample shows. Views are
// skipped: their documents are their source's.
func addFindings(c Collection, r *schema.Relation) {
	if c.Kind == "view" {
		return
	}
	byPath := map[string]Field{}
	for _, f := range c.Shape.Fields {
		byPath[f.Path] = f
	}
	rare := func(f Field) bool {
		return c.Shape.Sampled >= rareSample && f.Present*100 < rareShare*c.Shape.Sampled
	}
	for _, f := range c.Shape.Fields {
		if m := mixedTypes(f); m != "" {
			r.AddFinding("DBS-MONGO-MIXED-TYPES", "warn", fmt.Sprintf("%s holds %s of the sampled values (nulls aside): "+
				"queries and sorts compare each type separately, so a filter on one type misses the others", f.Path, m), f.Path)
		}
		// An element path ("tags[]") is rare when arrays are empty, not misspelled;
		// under a rare parent, the parent is the one reported.
		if f.Path != "_id" && !strings.HasSuffix(f.Path, "[]") && rare(f) {
			if p, ok := byPath[parent(f.Path)]; !ok || !rare(p) {
				r.AddFinding("DBS-MONGO-RARE-FIELD", "info", fmt.Sprintf("%s is in %d of %d sampled documents (%.1f%%): "+
					"a field this rare is often a misspelling or a leftover of an old shape", f.Path, f.Present, c.Shape.Sampled,
					float64(f.Present)*100/float64(c.Shape.Sampled)), f.Path)
			}
		}
		if len(f.ArrayLens) > 0 {
			lens := slices.Sorted(slices.Values(f.ArrayLens))
			if top := lens[len(lens)-1]; top > longArray {
				r.AddFinding("DBS-MONGO-UNBOUNDED-ARRAY", "warn", fmt.Sprintf("%s has up to %d elements in the sample (median %d over %d documents): "+
					"an array that keeps growing heads for the 16 MB document limit, and an index on it holds a key per element; "+
					"bound it or move its elements to their own collection", f.Path, top, lens[len(lens)/2], len(lens)), f.Path)
			}
		}
	}
}

// mixedTypes describes a field's types when it holds more than one besides
// null, "int 75% | string 25%", and is empty otherwise.
func mixedTypes(f Field) string {
	types := map[string]int{}
	allNumeric := true
	for t, n := range f.Types {
		if t != "null" {
			types[t] = n
			allNumeric = allNumeric && slices.Contains(numeric, t)
		}
	}
	if len(types) < 2 || allNumeric {
		return ""
	}
	return Field{Types: types}.TypeText()
}

// parent is the path a field path sits in: "a" for "a.b" and "a[]" for "a[].b".
func parent(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	return ""
}

// unusedIndexes finds indexes with no access since their counters started
// at least unusedIndexAfter ago. $indexStats needs the indexStats action,
// which the read role lacks (TestFindings checks this); then, or on any
// other error or a timeout, the check is skipped without a finding.
// Unique, TTL and hidden indexes are left out: they work without being read.
// refused reports that the server refused $indexStats for lack of privilege.
func unusedIndexes(ctx context.Context, coll *mongo.Collection) (_ []schema.Finding, refused bool) {
	ctx, cancel := context.WithTimeout(ctx, indexStatsTimeout)
	defer cancel()
	// Counters are per member; the primary is where most applications read,
	// so it is asked first rather than a secondary.
	pc := coll.Database().Collection(coll.Name(), options.Collection().SetReadPreference(readpref.PrimaryPreferred()))
	cur, err := pc.Aggregate(ctx, mongo.Pipeline{{{Key: "$indexStats", Value: bson.D{}}}}, options.Aggregate().SetComment("dbscope index use"))
	if err != nil {
		return nil, isUnauthorized(err)
	}
	defer cur.Close(ctx)
	type stat struct {
		ops   int64
		since time.Time
		hosts []string
	}
	byName := map[string]*stat{}
	for cur.Next(ctx) {
		var ix struct {
			Name     string `bson:"name"`
			Host     string `bson:"host"`
			Accesses struct {
				Ops   int64     `bson:"ops"`
				Since time.Time `bson:"since"`
			} `bson:"accesses"`
			Spec struct {
				Unique bool `bson:"unique"`
				TTL    any  `bson:"expireAfterSeconds"`
				Hidden bool `bson:"hidden"`
			} `bson:"spec"`
			Building bool `bson:"building"`
		}
		if cur.Decode(&ix) != nil || ix.Name == "_id_" || ix.Spec.Unique || ix.Spec.TTL != nil || ix.Spec.Hidden || ix.Building {
			continue
		}
		// A sharded collection has one entry per shard: used on any, used; the latest start counts.
		s := byName[ix.Name]
		if s == nil {
			s = &stat{}
			byName[ix.Name] = s
		}
		s.ops += ix.Accesses.Ops
		if ix.Accesses.Since.After(s.since) {
			s.since = ix.Accesses.Since
		}
		s.hosts = append(s.hosts, ix.Host)
	}
	if cur.Err() != nil {
		return nil, false
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []schema.Finding
	for _, n := range names {
		s := byName[n]
		if s.ops != 0 || s.since.IsZero() || time.Since(s.since) < unusedIndexAfter {
			continue
		}
		out = append(out, schema.Finding{RuleID: "DBS-MONGO-UNUSED-INDEX", Level: "warn", Message: fmt.Sprintf("The index %s has had no accesses since %s on %s: "+
			"it still costs every write and its memory. Counters are per member and restart with mongod, "+
			"so check the other members, then hide it (hideIndex) before dropping it", n,
			s.since.UTC().Format(time.RFC3339), strings.Join(s.hosts, ", "))})
	}
	return out, false
}

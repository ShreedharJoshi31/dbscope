package mongo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestEstimateIndexBuild builds indexes on collections of three sizes and
// checks each real duration falls inside the estimate made from a live base.
// Its log lines are the measurements behind indexRate.
func TestEstimateIndexBuild(t *testing.T) {
	p := mytest.Mongo(t)
	c, err := Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	app := c.Database("app")
	for _, docs := range []int{100_000, 300_000, 1_000_000} {
		coll := app.Collection("c")
		if err := coll.Drop(ctx); err != nil {
			t.Fatal(err)
		}
		const batch = 10_000
		for i := 0; i < docs; i += batch {
			b := make([]any, batch)
			for j := range b {
				n := i + j
				b[j] = bson.D{{Key: "n", Value: n}, {Key: "s", Value: fmt.Sprintf("%032x", uint64(n)*0x9e3779b97f4a7c15)}, {Key: "k", Value: n % 1000}}
			}
			if _, err := coll.InsertMany(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			script string
			keys   bson.D
		}{
			{`db.c.createIndex({s: 1})`, bson.D{{Key: "s", Value: 1}}},
			{`db.c.createIndex({k: 1, n: 1})`, bson.D{{Key: "k", Value: 1}, {Key: "n", Value: 1}}},
		} {
			base, err := ReadBase(ctx, c, "app")
			if err != nil {
				t.Fatal(err)
			}
			s := Analyze(base, []ingest.Input{{Name: "m.js", SQL: tc.script}}).Statements[0]
			if s.Estimate == nil {
				t.Fatalf("%s: no estimate (%+v)", tc.script, s.Findings)
			}
			cmd := bson.D{{Key: "createIndexes", Value: "c"}, {Key: "indexes", Value: bson.A{bson.D{{Key: "key", Value: tc.keys}, {Key: "name", Value: fmt.Sprint(len(tc.keys), tc.keys[0].Key)}}}}}
			start := time.Now()
			if err := app.RunCommand(ctx, cmd).Err(); err != nil {
				t.Fatal(err)
			}
			checkEstimate(t, fmt.Sprintf("%d documents: %s", docs, tc.script), base.Collections["c"].Bytes, time.Since(start), s.Estimate)
		}
	}
}

// TestEstimateNone checks which calls get no estimate: an index on a
// collection of unknown size, a call that fails, and work not measured.
func TestEstimateNone(t *testing.T) {
	base := &Base{Collections: map[string]*CollectionInfo{
		"c":       {Count: 1e6, Bytes: 80e6, Indexes: []IndexInfo{{Name: "_id_", Keys: []string{"_id"}}, {Name: "n_1", Keys: []string{"n"}}}},
		"unsized": {Count: 1e6},
	}}
	for _, c := range []struct {
		script string
		want   bool
	}{
		{`db.c.createIndex({s: 1})`, true},
		{`db.c.createIndexes([{key: {s: 1}}, {key: {k: 1}}])`, true},
		{`db.unsized.createIndex({s: 1})`, false},
		{`db.missing.createIndex({s: 1})`, false},
		{`db.c.createIndex({s: 1}, {name: "n_1"})`, false}, // fails: the name is taken
		{`db.c.updateMany({}, {$set: {a: 1}})`, false},
	} {
		s := Analyze(base, []ingest.Input{{Name: "m.js", SQL: c.script}}).Statements[0]
		if (s.Estimate != nil) != c.want {
			t.Errorf("%s: estimate %+v, want one: %v", c.script, s.Estimate, c.want)
		}
	}
}

// checkEstimate fails when took is outside e and logs the measured rate and
// how far took is from each end of the range.
func checkEstimate(t *testing.T, what string, size int64, took time.Duration, e *migrate.Estimate) {
	t.Helper()
	sec := took.Seconds()
	t.Logf("%s: %.1f MB in %.3fs = %.0f MB/s; estimate %.3f-%.3fs (low end %.1fx below, high end %.1fx above)",
		what, float64(size)/1e6, sec, float64(size)/1e6/sec, e.LowSeconds, e.HighSeconds, sec/e.LowSeconds, e.HighSeconds/sec)
	if sec < e.LowSeconds || sec > e.HighSeconds {
		t.Errorf("%s took %.3fs, outside the estimate %.3f-%.3fs (%s)", what, sec, e.LowSeconds, e.HighSeconds, e.Basis)
	}
}

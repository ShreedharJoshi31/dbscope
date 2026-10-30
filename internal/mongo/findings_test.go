package mongo

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestFindings(t *testing.T) {
	root := mytest.Mongo(t)
	ctx := context.Background()
	c, err := Connect(root, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect(ctx)
	db := c.Database("app")

	// 200 products: all of them are sampled, so the shares are exact.
	var products []any
	for i := range 200 {
		var price any = int32(i)
		if i%4 == 0 {
			price = "9.99"
		}
		var weight any = int32(i)
		if i%2 == 1 {
			weight = float64(i) + 0.5
		}
		var note any = "n"
		if i%5 == 0 {
			note = nil
		}
		p := doc("sku", fmt.Sprintf("s%d", i), "price", price, "weight", weight, "note", note)
		if i < 3 {
			p = append(p, bson.E{Key: "emial", Value: "x@y"})
		} else {
			p = append(p, bson.E{Key: "email", Value: "x@y"})
		}
		if i < 10 { // exactly 5%: not rare
			p = append(p, bson.E{Key: "discount", Value: int32(5)})
		}
		// A polymorphic subdocument: color or size, and a rare legacy flag.
		attrs := doc("color", "red")
		if i%2 == 1 {
			attrs = doc("size", int32(i))
		}
		if i < 2 {
			attrs = append(attrs, bson.E{Key: "legacy", Value: true})
		}
		p = append(p, bson.E{Key: "attrs", Value: attrs})
		if i < 4 { // rare, and its child is not reported on its own
			p = append(p, bson.E{Key: "extra", Value: doc("x", int32(1))})
		}
		tags := bson.A{"a", "b", "c"}
		if i == 0 {
			tags = make(bson.A, 1001)
			for j := range tags {
				tags[j] = "t"
			}
		}
		history := bson.A{int32(1)}
		if i == 1 { // exactly 1,000: not flagged
			history = make(bson.A, 1000)
			for j := range history {
				history[j] = int32(j)
			}
		}
		items := bson.A{}
		if i == 5 { // items[] and items[].sku are rare because arrays are empty, not misspelled
			items = bson.A{doc("sku", "a")}
		}
		p = append(p, bson.E{Key: "tags", Value: tags}, bson.E{Key: "history", Value: history}, bson.E{Key: "items", Value: items})
		products = append(products, p)
	}
	if _, err := db.Collection("products").InsertMany(ctx, products); err != nil {
		t.Fatal(err)
	}
	for _, ix := range []mongo.IndexModel{
		{Keys: bson.D{{Key: "sku", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "price", Value: 1}}},
		{Keys: bson.D{{Key: "weight", Value: 1}}},
		{Keys: bson.D{{Key: "createdAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(3600)},
		{Keys: bson.D{{Key: "note", Value: 1}}, Options: options.Index().SetHidden(true)},
	} {
		if _, err := db.Collection("products").Indexes().CreateOne(ctx, ix); err != nil {
			t.Fatal(err)
		}
	}
	// Fewer than 100 documents: a field in one of them is not rare enough to say.
	var small []any
	for i := range 50 {
		s := doc("name", "n")
		if i == 0 {
			s = append(s, bson.E{Key: "nmae", Value: "n"})
		}
		small = append(small, s)
	}
	if _, err := db.Collection("small").InsertMany(ctx, small); err != nil {
		t.Fatal(err)
	}
	// The view filters on an unindexed field: sampling a view that used weight_1 would count as its use.
	if err := db.CreateView(ctx, "cheap", "products", bson.A{doc("$match", doc("discount", doc("$gt", 1)))}); err != nil {
		t.Fatal(err)
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "createUser", Value: "reader"}, {Key: "pwd", Value: "reader-test"},
		{Key: "roles", Value: bson.A{doc("role", "read", "db", "app")}}}).Err(); err != nil {
		t.Fatal(err)
	}

	introspect := func(t *testing.T, cl *mongo.Client) map[string][]schema.Finding {
		t.Helper()
		m, err := Introspect(ctx, cl, "app")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]schema.Finding{}
		for _, r := range m.Relations {
			out[r.Name] = r.Findings
		}
		return out
	}
	sampleRules := func(t *testing.T, got map[string][]schema.Finding) {
		t.Helper()
		var keys []string
		for _, f := range got["products"] {
			t.Logf("%s %s %s: %s", f.RuleID, f.Level, f.Column, f.Message)
			if f.RuleID != "DBS-MONGO-UNUSED-INDEX" {
				keys = append(keys, f.RuleID+" "+f.Level+" "+f.Column)
			}
		}
		slices.Sort(keys)
		want := []string{
			"DBS-MONGO-MIXED-TYPES warn price",
			"DBS-MONGO-RARE-FIELD info attrs.legacy",
			"DBS-MONGO-RARE-FIELD info emial",
			"DBS-MONGO-RARE-FIELD info extra",
			"DBS-MONGO-UNBOUNDED-ARRAY warn tags",
		}
		if !slices.Equal(keys, want) {
			t.Errorf("products findings %v", keys)
		}
		msg := func(rule, col string) string {
			for _, f := range got["products"] {
				if f.RuleID == rule && f.Column == col {
					return f.Message
				}
			}
			return ""
		}
		if m := msg("DBS-MONGO-MIXED-TYPES", "price"); !strings.HasPrefix(m, "price holds int 75% | string 25% ") {
			t.Errorf("mixed %q", m)
		}
		if m := msg("DBS-MONGO-RARE-FIELD", "emial"); !strings.HasPrefix(m, "emial is in 3 of 200 sampled documents (1.5%)") {
			t.Errorf("rare %q", m)
		}
		if m := msg("DBS-MONGO-RARE-FIELD", "attrs.legacy"); !strings.HasPrefix(m, "attrs.legacy is in 2 of 200 sampled documents (1.0%)") {
			t.Errorf("rare nested %q", m)
		}
		if m := msg("DBS-MONGO-UNBOUNDED-ARRAY", "tags"); !strings.HasPrefix(m, "tags has up to 1001 elements in the sample (median 3 over 200 documents)") {
			t.Errorf("array %q", m)
		}
		if len(got["small"]) != 0 || len(got["cheap"]) != 0 {
			t.Errorf("small %+v, view %+v", got["small"], got["cheap"])
		}
	}
	unused := func(got map[string][]schema.Finding) []schema.Finding {
		var out []schema.Finding
		for _, f := range got["products"] {
			if f.RuleID == "DBS-MONGO-UNUSED-INDEX" {
				out = append(out, f)
			}
		}
		return out
	}

	t.Run("indexes younger than a day", func(t *testing.T) {
		got := introspect(t, c)
		sampleRules(t, got)
		if u := unused(got); len(u) != 0 {
			t.Errorf("unused %+v", u)
		}
	})

	t.Run("unused index", func(t *testing.T) {
		defer func(d time.Duration) { unusedIndexAfter = d }(unusedIndexAfter)
		unusedIndexAfter = time.Second
		time.Sleep(2 * time.Second)
		// price_1 is used once; weight_1 never.
		if err := db.Collection("products").FindOne(ctx, doc("price", int32(5))).Err(); err != nil {
			t.Fatal(err)
		}
		got := introspect(t, c)
		sampleRules(t, got)
		u := unused(got)
		if len(u) != 1 || u[0].Level != "warn" || !strings.HasPrefix(u[0].Message, "The index weight_1 has had no accesses since 20") {
			t.Errorf("unused %+v", u)
		}

		// The read role may not run $indexStats: the check is skipped, the rest stays.
		rp := root
		rp.User, rp.Options = "reader", "authSource=app&directConnection=true"
		rc, err := Connect(rp, "reader-test")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Disconnect(ctx)
		if _, err := rc.Database("app").Collection("products").Aggregate(ctx, mongo.Pipeline{{{Key: "$indexStats", Value: bson.D{}}}}); !isUnauthorized(err) {
			t.Fatalf("reader ran $indexStats: %v", err)
		}
		got = introspect(t, rc)
		sampleRules(t, got)
		if u := unused(got); len(u) != 0 {
			t.Errorf("reader unused %+v", u)
		}
	})
}

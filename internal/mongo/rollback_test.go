package mongo

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

func downRules(rep *migrate.Report) []string {
	var out []string
	for _, s := range rep.Statements {
		for _, f := range s.Findings {
			if strings.HasPrefix(f.RuleID, "DBS-DOWN-") {
				out = append(out, f.RuleID+"/"+string(f.Rating))
			}
		}
	}
	return out
}

// mongoShape lists collections and their index names.
func mongoShape(t *testing.T, d *mongo.Database) []string {
	t.Helper()
	ctx := context.Background()
	names, err := d.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range names {
		out = append(out, "collection "+n)
		ixs, err := indexes(ctx, d.Collection(n))
		if err != nil {
			t.Fatal(err)
		}
		for _, ix := range ixs {
			out = append(out, "index "+n+"."+ix.Name)
		}
	}
	slices.Sort(out)
	return out
}

// Each case is a migrate-mongo file and the same calls as server commands;
// running them confirms DBS-DOWN-INCOMPLETE matches the collections and
// indexes the server ends up with.
func TestDownRoundTrip(t *testing.T) {
	idx := func(coll string, keys bson.D, name string) bson.D {
		return bson.D{{Key: "createIndexes", Value: coll}, {Key: "indexes", Value: bson.A{bson.D{{Key: "key", Value: keys}, {Key: "name", Value: name}}}}}
	}
	dropIdx := func(coll, name string) bson.D {
		return bson.D{{Key: "dropIndexes", Value: coll}, {Key: "index", Value: name}}
	}
	drop := func(coll string) bson.D { return bson.D{{Key: "drop", Value: coll}} }
	create := func(coll string) bson.D { return bson.D{{Key: "create", Value: coll}} }
	file := func(up, down string) string {
		s := "module.exports = {\n  async up(db, client) {\n" + up + "\n  },\n"
		if down != "" {
			s += "  async down(db, client) {\n" + down + "\n  }\n"
		}
		return s + "};\n"
	}
	cases := []struct {
		name, script string
		up, down     []bson.D
		want         []string
		inMessage    string
	}{
		{name: "index undone",
			script: file(`await db.collection('users').createIndex({email: 1});`, `await db.collection('users').dropIndex('email_1');`),
			up:     []bson.D{idx("users", bson.D{{Key: "email", Value: 1}}, "email_1")}, down: []bson.D{dropIdx("users", "email_1")}},
		{name: "index left behind",
			script: file(`await db.collection('users').createIndex({email: 1}, {name: 'by_email'});
    await db.createCollection('audit');`, `await db.collection('audit').drop();`),
			up:   []bson.D{idx("users", bson.D{{Key: "email", Value: 1}}, "by_email"), create("audit")},
			down: []bson.D{drop("audit")}, want: []string{"DBS-DOWN-INCOMPLETE/high"}, inMessage: "index by_email on users is still there"},
		{name: "drop recreated with its index",
			script: file(`await db.collection('orders').drop();`, `await db.createCollection('orders');
    await db.collection('orders').createIndex({user: 1}, {name: 'user_1'});`),
			up: []bson.D{drop("orders")}, down: []bson.D{create("orders"), idx("orders", bson.D{{Key: "user", Value: 1}}, "user_1")},
			want: []string{"DBS-DOWN-LOSES-DATA/medium"}, inMessage: "drops collection orders"},
		{name: "drop recreated without its index",
			script: file(`await db.collection('orders').drop();`, `await db.createCollection('orders');`),
			up:     []bson.D{drop("orders")}, down: []bson.D{create("orders")},
			want: []string{"DBS-DOWN-INCOMPLETE/high", "DBS-DOWN-LOSES-DATA/medium"}, inMessage: "index user_1 on orders is not recreated"},
		{name: "unset field",
			script: file(`await db.collection('users').updateMany({}, {$unset: {legacy: ''}});`, `await db.collection('users').updateMany({}, {$set: {legacy: null}});`),
			want:   []string{"DBS-DOWN-LOSES-DATA/medium"}, inMessage: "field users.legacy"},
	}
	p := mytest.Mongo(t)
	c, err := Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	app := c.Database("app")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := app.Drop(ctx); err != nil {
				t.Fatal(err)
			}
			app.Collection("users").InsertOne(ctx, bson.M{"email": "a", "legacy": 1})
			app.Collection("orders").InsertOne(ctx, bson.M{"user": 1})
			if err := app.RunCommand(ctx, idx("orders", bson.D{{Key: "user", Value: 1}}, "user_1")).Err(); err != nil {
				t.Fatal(err)
			}
			base, err := ReadBase(ctx, c, "app")
			if err != nil {
				t.Fatal(err)
			}
			rep := Analyze(base, []ingest.Input{{Name: "20240101-x.js", SQL: tc.script}})
			if got := downRules(rep); !slices.Equal(got, tc.want) {
				t.Fatalf("rules %v, want %v; %+v", got, tc.want, rep.Statements)
			}
			var msgs []string
			for _, s := range rep.Statements {
				for _, f := range s.Findings {
					msgs = append(msgs, f.Message)
				}
			}
			if tc.inMessage != "" && !strings.Contains(strings.Join(msgs, "\n"), tc.inMessage) {
				t.Errorf("no %q in %q", tc.inMessage, msgs)
			}
			upCalls := len(findCalls(tc.script)) - len(tc.down)
			if tc.up == nil {
				upCalls = 1
			}
			if len(rep.Statements) != upCalls {
				t.Errorf("%d statements, want %d: the down function must not be analyzed", len(rep.Statements), upCalls)
			}

			before := mongoShape(t, app)
			for _, cmd := range append(slices.Clone(tc.up), tc.down...) {
				if err := app.RunCommand(ctx, cmd).Err(); err != nil {
					t.Fatalf("%v: %v", cmd, err)
				}
			}
			after := mongoShape(t, app)
			if changed := !slices.Equal(before, after); changed != slices.Contains(tc.want, "DBS-DOWN-INCOMPLETE/high") {
				t.Errorf("server changed %v (%v -> %v)", changed, before, after)
			}
		})
	}
}

// migrate-mongo files without a down function get DBS-DOWN-MISSING;
// mongosh scripts, which have no up function, never do.
func TestDownMissing(t *testing.T) {
	base := &Base{Server: Server{Num: 80000}, Collections: map[string]*CollectionInfo{}}
	rep := Analyze(base, []ingest.Input{
		{Name: "1-a.js", SQL: "module.exports = { async up(db) { await db.collection('u').createIndex({a: 1}); } };"},
		{Name: "2-b.js", SQL: "exports.up = async (db) => db.collection('u').createIndex({b: 1});\nexports.down = async (db) => db.collection('u').dropIndex('b_1');\n"},
		{Name: "plain.js", SQL: "db.u.createIndex({c: 1})"},
	})
	got := map[string][]string{}
	for _, s := range rep.Statements {
		for _, f := range s.Findings {
			if strings.HasPrefix(f.RuleID, "DBS-DOWN-") {
				got[s.File] = append(got[s.File], f.RuleID)
			}
		}
	}
	if len(got) != 1 || !slices.Equal(got["1-a.js"], []string{"DBS-DOWN-MISSING"}) {
		t.Fatalf("%v", got)
	}
	if len(rep.Statements) != 3 {
		t.Fatalf("%d statements, want 3 (2-b.js's down must not be analyzed)", len(rep.Statements))
	}
}

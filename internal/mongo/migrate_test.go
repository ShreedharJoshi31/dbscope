package mongo

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

func hasRule(st migrate.Statement, id string) bool {
	for _, f := range st.Findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func TestFindCalls(t *testing.T) {
	calls := findCalls(`module.exports = {
  async up(db, client) {
    // db.ignored.drop()
    await db.collection('users').createIndex({ email: 1, "org.id": -1 }, { unique: true, name: "email_org" });
    await db.collection("events").updateMany({}, { $set: { v: 2 } });
  },
};
db.orders.createIndex({created: 1}, {expireAfterSeconds: 3600})
db.runCommand({ collMod: "users", validator: { $jsonSchema: { required: ["email"] } } })
db.getCollection("logs").drop()`)
	if len(calls) != 5 {
		t.Fatalf("calls %+v", calls)
	}
	c := calls[0]
	keys := keyList(c.Args[0])
	if c.Collection != "users" || c.Method != "createIndex" || c.Line != 4 || len(keys) != 2 || keys[1] != "org.id:-1" ||
		c.Args[1].(jsObject).get("unique") != true || c.Args[1].(jsObject).get("name") != "email_org" {
		t.Errorf("createIndex %+v", c)
	}
	if calls[2].Collection != "orders" || calls[3].Method != "runCommand" || calls[3].Args[0].(jsObject)[0].Key != "collMod" ||
		calls[4].Collection != "logs" || calls[4].Method != "drop" {
		t.Errorf("calls %+v", calls[2:])
	}
	if defaultIndexName([]string{"a", "b:-1", "c:text"}) != "a_1_b_-1_c_text" {
		t.Error("default name")
	}
}

func TestAnalyzeMongo(t *testing.T) {
	base := &Base{Server: Server{Version: "8.0.4", Num: 80004}, Collections: map[string]*CollectionInfo{
		"users":  {Count: 1000, Bytes: 1 << 20, Indexes: []IndexInfo{{Name: "_id_", Keys: []string{"_id"}}, {Name: "email_1", Keys: []string{"email"}}}},
		"orders": {Count: 5_000_000, Bytes: 2 << 30, Indexes: []IndexInfo{{Name: "_id_", Keys: []string{"_id"}}}},
	}}
	rep := Analyze(base, []ingest.Input{{Name: "001.js", SQL: `
db.users.createIndex({email: 1}, {name: "by_email"})
db.users.createIndex({email: 1})
db.orders.createIndex({created: 1}, {expireAfterSeconds: 86400})
db.orders.updateMany({}, {$set: {v: 2}})
db.users.dropIndex("_id_")
db.users.renameCollection("orders")`}})
	want := []struct {
		rating score.Rating
		rule   string
	}{{score.High, "DBS-MONGO-FAILS"}, {score.Low, "DBS-MONGO-INDEX-EXISTS"}, {score.High, "DBS-MONGO-TTL"}, {score.Medium, "DBS-MONGO-BULK-UPDATE"},
		{score.High, "DBS-MONGO-FAILS"}, {score.High, "DBS-MONGO-FAILS"}}
	if len(rep.Statements) != len(want) || rep.Engine != "mongodb" || rep.PGVersion != 80004 {
		t.Fatalf("report %+v", rep)
	}
	for i, w := range want {
		if st := rep.Statements[i]; st.Rating != w.rating || !hasRule(st, w.rule) || st.Score != map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8}[w.rating] {
			t.Errorf("%s: %s %+v", st.SQL, st.Rating, st.Findings)
		}
	}
	if msg := rep.Statements[2].Findings[1].Message; !strings.Contains(msg, "86400 seconds (1 day)") {
		t.Errorf("ttl message %q", msg)
	}
	if rep := Analyze(base, []ingest.Input{{Name: "x.js", SQL: "console.log('nothing here')"}}); len(rep.Unanalyzable) != 1 {
		t.Errorf("unanalyzable %+v", rep.Unanalyzable)
	}
}

// TestMigratePredictions runs each script's command on a real server and
// checks dbscope's "this fails" against the server's answer.
func TestMigratePredictions(t *testing.T) {
	idx := func(keys bson.D, opts ...bson.E) bson.D {
		return bson.D{{Key: "createIndexes", Value: "u"}, {Key: "indexes", Value: bson.A{append(bson.D{{Key: "key", Value: keys}}, opts...)}}}
	}
	cases := []struct {
		script string
		admin  bool
		cmd    bson.D
	}{
		{`db.u.createIndex({n: 1}, {name: "other"})`, false, idx(bson.D{{Key: "n", Value: 1}}, bson.E{Key: "name", Value: "other"})},
		{`db.u.createIndex({email: 1}, {name: "n_1"})`, false, idx(bson.D{{Key: "email", Value: 1}}, bson.E{Key: "name", Value: "n_1"})},
		{`db.u.createIndex({n: 1})`, false, idx(bson.D{{Key: "n", Value: 1}}, bson.E{Key: "name", Value: "n_1"})},
		{`db.u.createIndex({n: 1}, {unique: true})`, false, idx(bson.D{{Key: "n", Value: 1}}, bson.E{Key: "name", Value: "n_1"}, bson.E{Key: "unique", Value: true})},
		{`db.u.createIndex({n: 1}, {sparse: true})`, false, idx(bson.D{{Key: "n", Value: 1}}, bson.E{Key: "name", Value: "n_1"}, bson.E{Key: "sparse", Value: true})},
		{`db.u.createIndex({x: 1, n: -1})`, false, idx(bson.D{{Key: "x", Value: 1}, {Key: "n", Value: -1}}, bson.E{Key: "name", Value: "x_1_n_-1"})},
		{`db.u.dropIndex("_id_")`, false, bson.D{{Key: "dropIndexes", Value: "u"}, {Key: "index", Value: "_id_"}}},
		{`db.u.dropIndex("nope")`, false, bson.D{{Key: "dropIndexes", Value: "u"}, {Key: "index", Value: "nope"}}},
		{`db.u.dropIndex("n_1")`, false, bson.D{{Key: "dropIndexes", Value: "u"}, {Key: "index", Value: "n_1"}}},
		{`db.u.renameCollection("v")`, true, bson.D{{Key: "renameCollection", Value: "app.u"}, {Key: "to", Value: "app.v"}}},
		{`db.u.renameCollection("v", true)`, true, bson.D{{Key: "renameCollection", Value: "app.u"}, {Key: "to", Value: "app.v"}, {Key: "dropTarget", Value: true}}},
		{`db.u.renameCollection("w")`, true, bson.D{{Key: "renameCollection", Value: "app.u"}, {Key: "to", Value: "app.w"}}},
		{`db.createCollection("u")`, false, bson.D{{Key: "create", Value: "u"}}},
		{`db.runCommand({collMod: "u", validator: {email: {$type: "string"}}})`, false,
			bson.D{{Key: "collMod", Value: "u"}, {Key: "validator", Value: bson.D{{Key: "email", Value: bson.D{{Key: "$type", Value: "string"}}}}}}},
	}
	p := mytest.Mongo(t)
	c, err := Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	app, admin := c.Database("app"), c.Database("admin")
	for _, tc := range cases {
		for _, n := range []string{"u", "v", "w"} {
			app.Collection(n).Drop(ctx)
		}
		app.Collection("u").InsertMany(ctx, []any{bson.M{"email": "a", "n": 1, "x": 1}, bson.M{"email": "b", "n": 2, "x": 2}})
		app.Collection("v").InsertOne(ctx, bson.M{"x": 1})
		if err := app.RunCommand(ctx, idx(bson.D{{Key: "n", Value: 1}}, bson.E{Key: "name", Value: "n_1"})).Err(); err != nil {
			t.Fatal(err)
		}
		base, err := ReadBase(ctx, c, "app")
		if err != nil {
			t.Fatal(err)
		}
		st := Analyze(base, []ingest.Input{{Name: "m.js", SQL: tc.script}}).Statements[0]
		d := app
		if tc.admin {
			d = admin
		}
		err = d.RunCommand(ctx, tc.cmd).Err()
		if predicted := hasRule(st, "DBS-MONGO-FAILS"); predicted != (err != nil) {
			t.Errorf("%s: dbscope predicted fails=%v; the server said %v (%+v)", tc.script, predicted, err, st.Findings)
		}
	}
}

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

// TestBusyTable runs 1,200 single-document inserts on one collection and 5
// on another on a real server, then checks DBS-HOT-TABLE against what
// latencyStats counted: convertToCapped on the busy collection is rated one
// step above DBS-MONGO-CAPPED, an index build or collMod there names the
// share at Medium, and the quiet collection or an updateMany (no exclusive
// lock) gets nothing. A user with the read role alone still gets the counts,
// without a start date.
func TestBusyTable(t *testing.T) {
	p := mytest.Mongo(t)
	ctx := context.Background()
	c, err := Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect(ctx)
	app := c.Database("app")
	for i := range 1200 {
		if _, err := app.Collection("hot").InsertOne(ctx, bson.M{"_id": i, "status": "paid"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		if _, err := app.Collection("quiet").InsertOne(ctx, bson.M{"_id": i, "status": "paid"}); err != nil {
			t.Fatal(err)
		}
	}

	base, err := ReadBase(ctx, c, "app")
	if err != nil {
		t.Fatal(err)
	}
	if base.Traffic == nil || base.Traffic.Writes["hot"] != 1200 || base.Traffic.Writes["quiet"] != 5 || base.Traffic.Since == nil {
		t.Fatalf("traffic: %+v, warnings %v", base.Traffic, base.Warnings)
	}
	find := func(st migrate.Statement, id string) *migrate.Finding {
		for i := range st.Findings {
			if st.Findings[i].RuleID == id {
				return &st.Findings[i]
			}
		}
		return nil
	}
	check := func(base *Base, label, js string, want score.Rating) {
		t.Helper()
		rep := Analyze(base, []ingest.Input{{Name: "m.js", SQL: js}})
		if len(rep.Statements) != 1 {
			t.Fatalf("%s: statements %+v", label, rep.Statements)
		}
		st := rep.Statements[0]
		f := find(st, "DBS-HOT-TABLE")
		switch {
		case want == "" && f != nil:
			t.Errorf("%s: unexpected %+v", label, *f)
		case want == "":
		case f == nil:
			t.Errorf("%s: no DBS-HOT-TABLE in %+v", label, st.Findings)
		case f.Rating != want || st.Rating != score.Max(st.Rating, want) || !strings.Contains(f.Message, "hot has 100% of the write operations in the database"):
			t.Errorf("%s: %s %q, statement %s; want %s", label, f.Rating, f.Message, st.Rating, want)
		case want == score.Critical:
			if b := find(st, "DBS-MONGO-CAPPED"); b == nil || b.Rating.Up() != f.Rating {
				t.Errorf("%s: not one step above DBS-MONGO-CAPPED: %+v", label, st.Findings)
			}
		}
	}
	check(base, "convertToCapped on the busy collection", `db.runCommand({convertToCapped: "hot", size: 100000})`, score.Critical)
	check(base, "index build on the busy collection", `db.hot.createIndex({status: 1})`, score.Medium)
	check(base, "collMod on the busy collection", `db.runCommand({collMod: "hot", validator: {status: {$exists: true}}})`, score.Medium)
	check(base, "index build on the quiet collection", `db.quiet.createIndex({status: 1})`, "")
	check(base, "updateMany takes no exclusive lock", `db.hot.updateMany({}, {$set: {seen: true}})`, "")

	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "createUser", Value: "reader"}, {Key: "pwd", Value: "reader-pass"},
		{Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: "app"}}}}}).Err(); err != nil {
		t.Fatal(err)
	}
	rp := p
	rp.User = "reader"
	rc, err := Connect(rp, "reader-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Disconnect(ctx)
	rb, err := ReadBase(ctx, rc, "app")
	if err != nil {
		t.Fatal(err)
	}
	if rb.Traffic == nil || rb.Traffic.Writes["hot"] != 1200 || rb.Traffic.Since != nil || len(rb.Warnings) != 0 {
		t.Fatalf("read role: traffic %+v, warnings %v", rb.Traffic, rb.Warnings)
	}
	check(rb, "index build, read role", `db.hot.createIndex({status: 1})`, score.Medium)
}

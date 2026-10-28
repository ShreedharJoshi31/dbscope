package mongo

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestMongo(t *testing.T) {
	root := mytest.Mongo(t)
	ctx := context.Background()
	c, err := Connect(root, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect(ctx)
	db := c.Database("app")
	if err := db.CreateCollection(ctx, "customers"); err != nil {
		t.Fatal(err)
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: "customers"}, {Key: "validator", Value: bson.D{{Key: "email", Value: bson.D{{Key: "$exists", Value: true}}}}}}).Err(); err != nil {
		t.Fatal(err)
	}
	var customers []any
	var ids []bson.ObjectID
	for i := 0; i < 40; i++ {
		id := bson.NewObjectID()
		ids = append(ids, id)
		customers = append(customers, doc("_id", id, "email", "c@x", "name", "c"))
	}
	if _, err := db.Collection("customers").InsertMany(ctx, customers); err != nil {
		t.Fatal(err)
	}
	var orders []any
	for i := 0; i < 300; i++ {
		var total any = int32(i)
		if i%3 == 0 {
			total = float64(i) + 0.5
		}
		o := doc("customer_id", ids[i%40], "total", total, "status", []string{"new", "paid"}[i%2], "items", bson.A{doc("sku", "a", "qty", int32(1))})
		if i%10 == 0 {
			o = append(o, bson.E{Key: "coupon", Value: "X"})
		}
		orders = append(orders, o)
	}
	if _, err := db.Collection("orders").InsertMany(ctx, orders); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("orders").Indexes().CreateOne(ctx, mongoIndex("status")); err != nil {
		t.Fatal(err)
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "createUser", Value: "reader"}, {Key: "pwd", Value: "reader-test"},
		{Key: "roles", Value: bson.A{doc("role", "read", "db", "app")}}}).Err(); err != nil {
		t.Fatal(err)
	}

	reader := root
	reader.User, reader.Options = "reader", "authSource=app&directConnection=true"
	rc, err := Connect(reader, "reader-test")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Disconnect(ctx)

	t.Run("introspect", func(t *testing.T) {
		m, err := Introspect(ctx, rc, "app")
		if err != nil {
			t.Fatal(err)
		}
		if m.Engine != "mongodb" || len(m.Relations) != 2 || m.ServerVersion < 80000 {
			t.Fatalf("model %+v", m)
		}
		o := m.Relation(schema.QualifiedName{Schema: "app", Name: "orders"})
		if o.Kind != schema.KindCollection || *o.RowEstimate != 300 || !slices.Equal(o.PrimaryKey, []string{"_id"}) {
			t.Errorf("orders %+v", o)
		}
		if c := o.Column("total"); !strings.HasPrefix(c.Type, "int ") || !strings.Contains(c.Type, "double") {
			t.Errorf("total %+v", c)
		}
		if c := o.Column("coupon"); !c.Nullable || !strings.HasPrefix(c.Comment, "in ") {
			t.Errorf("coupon %+v", c)
		}
		if o.Column("items[].sku") == nil {
			t.Errorf("nested array field missing: %+v", o.Columns)
		}
		if len(o.ForeignKeys) != 1 || o.ForeignKeys[0].Ref.Name != "customers" || o.ForeignKeys[0].Confidence != 1 || !o.ForeignKeys[0].Inferred {
			t.Errorf("references %+v", o.ForeignKeys)
		}
		if !slices.ContainsFunc(o.Indexes, func(i schema.Index) bool { return i.Name == "status_1" }) {
			t.Errorf("indexes %+v", o.Indexes)
		}
		cu := m.Relation(schema.QualifiedName{Schema: "app", Name: "customers"})
		if len(cu.Checks) != 1 || !strings.Contains(cu.Checks[0].Expr, "$exists") {
			t.Errorf("validator %+v", cu.Checks)
		}
	})

	t.Run("run", func(t *testing.T) {
		cmd, err := Parse(`{"find": "orders", "filter": {"status": "paid"}, "sort": {"total": -1}, "projection": {"_id": 0, "total": 1, "status": 1}}`)
		if err != nil || Check(cmd) != nil {
			t.Fatal(err)
		}
		res, err := Run(ctx, rc.Database("app"), cmd, 5, false, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if res.RowCount != 5 || !res.Truncated || len(res.Columns) != 2 {
			t.Errorf("find %+v", res)
		}
		if res.Plan == nil || !planHas(res.Plan, "IXSCAN") {
			t.Errorf("plan should use status_1: %+v", res.Plan)
		}
		agg, _ := Parse(`{"aggregate": "orders", "pipeline": [{"$match": {"total": {"$gt": 100}}}, {"$group": {"_id": "$status", "n": {"$sum": 1}}}]}`)
		res, err = Run(ctx, rc.Database("app"), agg, 10, false, 5*time.Second)
		if err != nil || res.RowCount != 2 {
			t.Fatalf("aggregate %+v %v", res, err)
		}
		if !planHas(res.Plan, "COLLSCAN") || !planFlagged(res.Plan) {
			t.Errorf("an unindexed match should be a flagged collection scan: %+v", res.Plan)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		slow, _ := Parse(`{"aggregate": "orders", "pipeline": [{"$project": {"x": {"$range": [0, 1000000]}}}, {"$unwind": "$x"}, {"$count": "n"}]}`)
		start := time.Now()
		_, err := Run(ctx, rc.Database("app"), slow, 10, false, time.Second)
		if err == nil || time.Since(start) > 10*time.Second || !IsTimeout(err) {
			t.Fatalf("err %v after %s", err, time.Since(start))
		}
		// Whichever side timed out, the server must not keep running it.
		running := func() int {
			cur, err := c.Database("admin").Aggregate(ctx, mongo.Pipeline{
				{{Key: "$currentOp", Value: doc("allUsers", true)}},
				{{Key: "$match", Value: doc("command.comment", "dbscope", "command.aggregate", "orders")}}})
			if err != nil {
				t.Fatal(err)
			}
			var ops []bson.Raw
			cur.All(ctx, &ops)
			return len(ops)
		}
		deadline := time.Now().Add(5 * time.Second)
		for running() > 0 && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if n := running(); n > 0 {
			t.Errorf("%d dbscope operations still running after the timeout", n)
		}
	})

	t.Run("read only", func(t *testing.T) {
		bad := map[string]guard.Reason{
			`{"insert": "orders", "documents": [{}]}`:                                                                             guard.NotSelect,
			`{"aggregate": "orders", "pipeline": [{"$out": "copy"}]}`:                                                             guard.WritesData,
			`{"aggregate": "orders", "pipeline": [{"$lookup": {"from": "customers", "pipeline": [{"$merge": "x"}], "as": "c"}}]}`: guard.WritesData,
			`{"find": "orders", "filter": {"$where": "sleep(100)"}}`:                                                              guard.FunctionNotAllowed,
			`{"find": 1}`: guard.Syntax,
		}
		for q, want := range bad {
			cmd, err := Parse(q)
			if err == nil {
				err = Check(cmd)
			}
			var ge *guard.Error
			if !errors.As(err, &ge) || ge.Reason != want {
				t.Errorf("%s: %v, want %s", q, err, want)
			}
		}
		// The read role refuses a write even when Check is bypassed.
		cmd, _ := Parse(`{"aggregate": "orders", "pipeline": [{"$out": "copy"}]}`)
		if _, err := Run(ctx, rc.Database("app"), cmd, 10, false, 5*time.Second); err == nil {
			t.Error("$out ran as a read-only user")
		}
	})

	t.Run("test", func(t *testing.T) {
		if res := Test(ctx, reader, "reader-test"); !res.OK || len(res.WritePrivileges) != 0 || res.ServerVersionNum < 80000 {
			t.Errorf("reader %+v", res)
		}
		if res := Test(ctx, root, mytest.Password); !res.OK || len(res.WritePrivileges) == 0 {
			t.Errorf("root %+v", res)
		}
		if res := Test(ctx, reader, "wrong"); res.OK || res.Error == nil || res.Error.Kind != "auth" {
			t.Errorf("wrong password %+v %+v", res, res.Error)
		}
	})
}

func mongoIndex(field string) mongo.IndexModel {
	return mongo.IndexModel{Keys: bson.D{{Key: field, Value: 1}}}
}

func planHas(n *plan.Node, stage string) bool {
	if n == nil {
		return false
	}
	if n.NodeType == stage {
		return true
	}
	for _, c := range n.Children {
		if planHas(c, stage) {
			return true
		}
	}
	return false
}

func planFlagged(n *plan.Node) bool {
	if len(n.Flags) > 0 {
		return true
	}
	for _, c := range n.Children {
		if planFlagged(c) {
			return true
		}
	}
	return false
}

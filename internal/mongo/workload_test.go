package mongo

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/workload"
)

// TestWorkload reads the counters and the profiler of MongoDB 8.0: with the
// profiler off, after an admin turned it on and an application ran, and as a
// user with only read on the database, before and after clusterMonitor.
func TestWorkload(t *testing.T) {
	p := mytest.Mongo(t)
	ctx := context.Background()
	c, err := Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect(ctx)
	db := c.Database("app")
	var orders []any
	for i := range 50 {
		orders = append(orders, doc("n", int32(i), "status", []string{"new", "paid"}[i%2]))
	}
	if _, err := db.Collection("orders").InsertMany(ctx, orders); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("orders").Indexes().CreateOne(ctx, mongoIndex("status")); err != nil {
		t.Fatal(err)
	}
	var customers []any
	for i := range 10 {
		customers = append(customers, doc("x", int32(i)))
	}
	if _, err := db.Collection("customers").InsertMany(ctx, customers); err != nil {
		t.Fatal(err)
	}
	m, err := Introspect(ctx, c, "app")
	if err != nil {
		t.Fatal(err)
	}

	// The profiler is off by default: counters only, and how to turn it on.
	off, err := Workload(ctx, c, m)
	if err != nil {
		t.Fatal(err)
	}
	if off.Statements.Status != workload.StatusNotLoaded ||
		!strings.Contains(off.Statements.Message, `db.getSiblingDB("app").setProfilingLevel(1, { slowms: 100 })`) {
		t.Fatalf("profiler off: %+v", off.Statements)
	}
	if off.InRecovery || off.TableStatsReset == nil || off.TableStatsReset.After(time.Now()) {
		t.Errorf("server: inRecovery %v, reset %v", off.InRecovery, off.TableStatsReset)
	}
	for key := range off.Tables {
		if key != "app.orders" && key != "app.customers" {
			t.Errorf("unexpected table %s", key)
		}
	}
	o, cu := off.Tables["app.orders"], off.Tables["app.customers"]
	if o == nil || cu == nil || o.LiveRows != 50 || cu.LiveRows != 10 || o.RowsWritten != 1 || o.SeqScans != 0 || o.QueryCalls < 2 {
		t.Fatalf("orders %+v customers %+v", o, cu)
	}

	// One reading adds exactly three reads to each collection: $collStats two, $indexStats one.
	again, err := Workload(ctx, c, m)
	if err != nil {
		t.Fatal(err)
	}
	if d := again.Tables["app.orders"].QueryCalls - o.QueryCalls; d != 3 {
		t.Errorf("dbscope's own reads per reading: %d", d)
	}
	if again.Tables["app.orders"].RowsWritten != o.RowsWritten || again.Tables["app.orders"].IdxScans != o.IdxScans {
		t.Errorf("a reading changed writes or index use: %+v", again.Tables["app.orders"])
	}

	// An admin turns the profiler on; the application, under its own name, runs.
	if err := db.RunCommand(ctx, bson.D{{Key: "profile", Value: 2}}).Err(); err != nil {
		t.Fatal(err)
	}
	app, err := mongo.Connect(options.Client().ApplyURI("mongodb://" + net.JoinHostPort(p.Host, strconv.Itoa(p.Port)) + "/?directConnection=true").
		SetAuth(options.Credential{Username: "root", Password: mytest.Password, AuthSource: "admin"}).SetAppName("shop"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Disconnect(ctx)
	adb := app.Database("app")
	ord := adb.Collection("orders")
	for range 4 {
		cur, err := ord.Find(ctx, doc("status", "paid", "n", doc("$gt", int32(3))))
		if err == nil {
			err = cur.All(ctx, &[]bson.Raw{})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	cur, err := ord.Aggregate(ctx, bson.A{doc("$match", doc("n", int32(1))),
		doc("$lookup", doc("from", "customers", "localField", "n", "foreignField", "x", "as", "c"))})
	if err == nil {
		err = cur.All(ctx, &[]bson.Raw{})
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ord.InsertOne(ctx, doc("n", int32(100), "status", "new")); err != nil {
		t.Fatal(err)
	}
	if _, err := ord.UpdateOne(ctx, doc("n", int32(2)), doc("$set", doc("status", "paid"))); err != nil {
		t.Fatal(err)
	}
	if _, err := ord.DeleteOne(ctx, doc("n", int32(4))); err != nil {
		t.Fatal(err)
	}
	// A collection created after the model was read: not tied to a table.
	if _, err := adb.Collection("audit").InsertOne(ctx, doc("at", "now")); err != nil {
		t.Fatal(err)
	}
	// Catalog traffic from the application, which statements leave out.
	if _, err := ord.Indexes().ListSpecifications(ctx); err != nil {
		t.Fatal(err)
	}
	cur, err = ord.Aggregate(ctx, bson.A{doc("$collStats", doc("latencyStats", bson.D{}))})
	if err == nil {
		err = cur.All(ctx, &[]bson.Raw{})
	}
	if err != nil {
		t.Fatal(err)
	}

	on, err := Workload(ctx, c, m)
	if err != nil {
		t.Fatal(err)
	}
	st := on.Statements
	// 4 finds, 1 aggregate, 3 writes on orders and 1 insert into audit.
	if st.Status != workload.StatusOK || st.Message != "" || st.TotalCalls != 9 || st.UnattributedCalls != 1 || st.Since == nil || st.Read < 9 {
		t.Fatalf("statements %+v", st)
	}
	o2 := on.Tables["app.orders"]
	// Since the second reading: 8 application operations, its $collStats, and this reading's 3 reads.
	if d := o2.QueryCalls - again.Tables["app.orders"].QueryCalls; d != 12 {
		t.Errorf("orders calls grew by %d", d)
	}
	if d := o2.RowsWritten - o.RowsWritten; d != 3 {
		t.Errorf("orders writes grew by %d", d)
	}
	if d := o2.IdxScans - o.IdxScans; d != 4 {
		t.Errorf("orders index uses grew by %d", d)
	}
	if o2.LiveRows != 50 || o2.QueryTimeMs <= again.Tables["app.orders"].QueryTimeMs {
		t.Errorf("orders %+v", o2)
	}
	want := map[string]int64{
		`db.orders.find({"status": ?, "n": {"$gt": ?}})`: 4,
		`db.orders.aggregate([{"$match": {"n": ?}}, {"$lookup": {"from": "customers", "localField": "n", "foreignField": "x", "as": "c"}}])`: 1,
		`db.orders.insert()`: 1,
		`db.orders.update({"n": ?}, {"$set": {"status": ?}})`: 1,
		`db.orders.remove({"n": ?})`:                          1,
	}
	got := map[string]int64{}
	for _, q := range o2.TopQueries {
		got[q.Text] = q.Calls
	}
	for text, calls := range want {
		if got[text] != calls {
			t.Errorf("orders top queries lack %s x%d: %+v", text, calls, o2.TopQueries)
		}
	}
	// Most time first; among equal times (millis is whole), most calls first.
	for i := 1; i < len(o2.TopQueries); i++ {
		a, b := o2.TopQueries[i-1], o2.TopQueries[i]
		if a.TotalMs < b.TotalMs || a.TotalMs == b.TotalMs && a.Calls < b.Calls {
			t.Errorf("top queries out of order: %+v", o2.TopQueries)
		}
	}
	// The $lookup ties the aggregate to customers too.
	if cq := on.Tables["app.customers"].TopQueries; len(cq) != 1 || !strings.Contains(cq[0].Text, "$lookup") || cq[0].Calls != 1 {
		t.Errorf("customers top queries %+v", cq)
	}
	for key, tb := range on.Tables {
		if strings.Contains(key, "system.") || key == "app.audit" {
			t.Errorf("table %s reported", key)
		}
		for _, q := range tb.TopQueries {
			if strings.Contains(q.Text, "$collStats") || strings.Contains(q.Text, "$indexStats") || strings.Contains(q.Text, "system.") {
				t.Errorf("catalog traffic attributed to %s: %s", key, q.Text)
			}
		}
	}

	// Level 1 records only slow operations, and says so.
	if err := db.RunCommand(ctx, bson.D{{Key: "profile", Value: 1}, {Key: "slowms", Value: 50}}).Err(); err != nil {
		t.Fatal(err)
	}
	slow, err := Workload(ctx, c, m)
	if err != nil || slow.Statements.Status != workload.StatusOK || !strings.Contains(slow.Statements.Message, "slower than 50 ms") {
		t.Fatalf("level 1 %+v %v", slow.Statements, err)
	}

	// A user with only read on the database: $collStats works, $indexStats and the profiler do not.
	if err := db.RunCommand(ctx, bson.D{{Key: "createUser", Value: "reader"}, {Key: "pwd", Value: "reader-pass"},
		{Key: "roles", Value: bson.A{doc("role", "read", "db", "app")}}}).Err(); err != nil {
		t.Fatal(err)
	}
	reader := p
	reader.User, reader.Options = "reader", "authSource=app&directConnection=true"
	readerWorkload := func() *workload.Snapshot {
		t.Helper()
		rc, err := Connect(reader, "reader-pass")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Disconnect(ctx)
		snap, err := Workload(ctx, rc, m)
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}
	ro := readerWorkload()
	const grant = `db.getSiblingDB("app").grantRolesToUser("reader", [{ role: "clusterMonitor", db: "admin" }])`
	if ro.Statements.Status != workload.StatusUnavailable || !strings.Contains(ro.Statements.Message, grant) ||
		!strings.Contains(ro.Statements.Message, "the indexStats action and find on app.system.profile") ||
		strings.Contains(ro.Statements.Message, "collStats action") {
		t.Fatalf("reader statements %+v", ro.Statements)
	}
	if tb := ro.Tables["app.orders"]; tb == nil || tb.QueryCalls <= o2.QueryCalls || tb.LiveRows != 50 || tb.IdxScans != 0 || len(tb.TopQueries) != 0 {
		t.Errorf("reader orders %+v", tb)
	}
	if ro.TableStatsReset != nil {
		t.Errorf("reader read serverStatus: %v", ro.TableStatsReset)
	}

	// The admin runs the grant the message names.
	if err := db.RunCommand(ctx, bson.D{{Key: "grantRolesToUser", Value: "reader"},
		{Key: "roles", Value: bson.A{doc("role", "clusterMonitor", "db", "admin")}}}).Err(); err != nil {
		t.Fatal(err)
	}
	granted := readerWorkload()
	if granted.Statements.Status != workload.StatusOK || strings.Contains(granted.Statements.Message, "lacks") || granted.TableStatsReset == nil {
		t.Fatalf("after grant %+v", granted.Statements)
	}
	if tb := granted.Tables["app.orders"]; tb == nil || tb.IdxScans != o2.IdxScans || !slices.ContainsFunc(tb.TopQueries, func(q workload.Query) bool { return q.Calls == 4 }) {
		t.Errorf("after grant orders %+v", tb)
	}
	// clusterMonitor alone reads no documents.
	mon := p
	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "createUser", Value: "monitor"}, {Key: "pwd", Value: "monitor-pass"},
		{Key: "roles", Value: bson.A{doc("role", "clusterMonitor", "db", "admin")}}}).Err(); err != nil {
		t.Fatal(err)
	}
	mon.User = "monitor"
	mc, err := Connect(mon, "monitor-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer mc.Disconnect(ctx)
	if err := mc.Database("app").Collection("orders").FindOne(ctx, bson.D{}).Err(); !isUnauthorized(err) {
		t.Errorf("clusterMonitor read a document: %v", err)
	}
}

func TestWorkloadText(t *testing.T) {
	cmd := raw(t, bson.D{{Key: "aggregate", Value: "orders"}, {Key: "pipeline", Value: bson.A{
		doc("$match", doc("status", doc("$in", bson.A{"a", "b"}), "note", "secret")),
		doc("$unionWith", doc("coll", "archive", "pipeline", bson.A{doc("$lookup", doc("from", "customers", "as", "c"))})),
		doc("$group", doc("_id", "$status", "n", doc("$sum", int32(1)), "label", "secret")),
		doc("$merge", doc("into", doc("db", "reports", "coll", "daily"))),
	}}})
	if got, want := commandText("orders", "aggregate", cmd),
		`db.orders.aggregate([{"$match": {"status": {"$in": [?]}, "note": ?}}, {"$unionWith": {"coll": "archive", "pipeline": [{"$lookup": {"from": "customers", "as": "c"}}]}}, {"$group": {"_id": "$status", "n": {"$sum": ?}, "label": ?}}, {"$merge": {"into": {"db": "reports", "coll": "daily"}}}])`; got != want {
		t.Errorf("text\n got %s\nwant %s", got, want)
	}
	if got, want := pipelineCollections("app", cmd), []string{"app.archive", "app.customers", "reports.daily"}; !slices.Equal(got, want) {
		t.Errorf("collections %v", got)
	}
	if got := commandText("orders", "find", raw(t, bson.D{{Key: "find", Value: "orders"}, {Key: "filter", Value: doc("a", int32(1))}, {Key: "sort", Value: doc("b", int32(-1))}})); got != `db.orders.find({"a": ?}).sort({"b": ?})` {
		t.Errorf("find %s", got)
	}
	long := strings.Repeat("é", 2*maxTextLen)
	if got := commandText("orders", "find", raw(t, bson.D{{Key: "filter", Value: doc(long, int32(1))}})); len(got) > maxTextLen+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("long text %d bytes", len(got))
	}
	for _, c := range []struct {
		op   string
		cmd  bson.D
		kind string
	}{
		{"command", bson.D{{Key: "aggregate", Value: "orders"}, {Key: "pipeline", Value: bson.A{doc("$indexStats", bson.D{})}}}, ""},
		{"command", bson.D{{Key: "listIndexes", Value: "orders"}}, ""},
		{"command", bson.D{{Key: "count", Value: "orders"}}, "count"},
		{"command", bson.D{{Key: "aggregate", Value: "orders"}, {Key: "pipeline", Value: bson.A{}}}, "aggregate"},
		{"query", bson.D{{Key: "find", Value: "orders"}}, "find"},
	} {
		if got := commandKind(c.op, raw(t, c.cmd)); got != c.kind {
			t.Errorf("%s %v: %q", c.op, c.cmd, got)
		}
	}
}

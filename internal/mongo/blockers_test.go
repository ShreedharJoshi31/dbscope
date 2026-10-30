package mongo

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// replicaSet starts a one-member replica set (transactions need one) without
// authentication and returns a profile for its "app" database.
func replicaSet(t *testing.T) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_MONGO") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: mytest.MongoImage, Cmd: []string{"--replSet", "rs0", "--bind_ip_all", "--wiredTigerCacheSizeGB", "0.25"},
			ExposedPorts: []string{"27017/tcp"},
			WaitingFor:   wait.ForLog("Waiting for connections").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no mongodb container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "27017/tcp")
	n, _ := strconv.Atoi(port.Port())
	p := conn.Profile{Engine: conn.EngineMongoDB, Host: host, Port: n, Database: "app",
		Options: "directConnection=true", SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
	cl := rawClient(t, p, "")
	cfg := bson.D{{Key: "_id", Value: "rs0"}, {Key: "members", Value: bson.A{bson.D{{Key: "_id", Value: 0}, {Key: "host", Value: "localhost:27017"}}}}}
	if err := cl.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetInitiate", Value: cfg}}).Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		var hello struct {
			IsWritablePrimary bool `bson:"isWritablePrimary"`
		}
		if err := cl.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err == nil && hello.IsWritablePrimary {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replica set never elected a primary")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return p
}

// rawClient connects as an application would, under another name than dbscope's.
func rawClient(t *testing.T, p conn.Profile, auth string) *mongo.Client {
	t.Helper()
	c, err := mongo.Connect(options.Client().ApplyURI("mongodb://" + auth + p.Host + ":" + strconv.Itoa(p.Port) + "/?directConnection=true").SetAppName("app"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Disconnect(context.Background()) })
	return c
}

func blockedFinding(s migrate.Statement) *migrate.Finding {
	for i, f := range s.Findings {
		if f.RuleID == migrate.BlockedNow {
			return &s.Findings[i]
		}
	}
	return nil
}

// TestBlockersMongo holds a transaction open on a replica set, analyzes an
// index build and checks the reported blockers against the server: the
// build really runs out of time exactly when dbscope says it would wait.
func TestBlockersMongo(t *testing.T) {
	p := replicaSet(t)
	ctx := context.Background()
	app := rawClient(t, p, "")
	appDB := app.Database("app")
	if _, err := appDB.Collection("t").InsertOne(ctx, bson.M{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := appDB.Collection("other").InsertOne(ctx, bson.M{"x": 1}); err != nil {
		t.Fatal(err)
	}
	dbscope, err := Connect(p, "")
	if err != nil {
		t.Fatal(err)
	}
	defer dbscope.Disconnect(ctx)
	build := bson.D{{Key: "createIndexes", Value: "t"}, {Key: "indexes", Value: bson.A{bson.D{{Key: "key", Value: bson.D{{Key: "x", Value: 1}}}, {Key: "name", Value: "x_1"}}}}}
	// waits reports whether the index build runs out of its 2 s, dropping the index if it was built.
	waits := func() bool {
		t.Helper()
		err := appDB.RunCommand(ctx, append(build, bson.E{Key: "maxTimeMS", Value: 2000})).Err()
		var ce mongo.CommandError
		if errors.As(err, &ce) && ce.Code == 50 { // MaxTimeMSExpired
			return true
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := appDB.Collection("t").Indexes().DropOne(ctx, "x_1"); err != nil {
			t.Fatal(err)
		}
		return false
	}
	analyze := func() *migrate.Report {
		t.Helper()
		base, err := ReadBase(ctx, dbscope, "app")
		if err != nil {
			t.Fatal(err)
		}
		rep := Analyze(base, []ingest.Input{{Name: "m.js", SQL: "db.t.createIndex({x: 1})\ndb.t.updateMany({}, {$set: {y: 1}})"}})
		ReadBlockers(ctx, dbscope, "app", rep)
		if rep.BlockersAt == nil {
			t.Fatalf("BlockersAt not set; warnings %v", rep.Warnings)
		}
		return rep
	}
	open := func(coll string) (string, func()) {
		t.Helper()
		s, err := app.StartSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.StartTransaction(); err != nil {
			t.Fatal(err)
		}
		if _, err := appDB.Collection(coll).InsertOne(mongo.NewSessionContext(ctx, s), bson.M{"x": 2}); err != nil {
			t.Fatal(err)
		}
		id := s.ID().Lookup("id")
		_, data := id.Binary()
		return uuidText(bson.Binary{Subtype: 4, Data: data}), func() { s.AbortTransaction(ctx); s.EndSession(ctx) }
	}

	t.Run("no open transaction", func(t *testing.T) {
		rep := analyze()
		if len(rep.Blockers) != 0 || blockedFinding(rep.Statements[0]) != nil || waits() {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
	})

	t.Run("transaction on another collection", func(t *testing.T) {
		_, end := open("other")
		defer end()
		rep := analyze()
		if len(rep.Blockers) != 0 || blockedFinding(rep.Statements[0]) != nil || waits() {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
	})

	t.Run("idle transaction that wrote the collection", func(t *testing.T) {
		lsid, end := open("t")
		defer end()
		time.Sleep(1100 * time.Millisecond)
		rep := analyze()
		if len(rep.Blockers) != 1 {
			t.Fatalf("blockers %+v, warnings %v", rep.Blockers, rep.Warnings)
		}
		b := rep.Blockers[0]
		if b.Relation != "t" || b.Session != "lsid "+lsid || b.State != "idle in transaction" || b.Lock != "IX collection lock" || b.AgeSeconds < 1 {
			t.Errorf("blocker %+v", b)
		}
		f := blockedFinding(rep.Statements[0])
		if f == nil || f.Rating != "high" || rep.Statements[0].Rating != "high" ||
			!strings.Contains(f.Message, "the open transaction of session "+lsid+", idle in transaction for ") || !strings.Contains(f.Message, "holding IX collection lock on t") {
			t.Errorf("finding %+v", f)
		}
		if blockedFinding(rep.Statements[1]) != nil {
			t.Error("updateMany takes no exclusive lock")
		}
		if !waits() {
			t.Error("the server builds the index past the transaction")
		}
		t.Logf("observed: %+v", b)
	})

	t.Run("index build already waiting", func(t *testing.T) {
		_, end := open("t")
		other := rawClient(t, p, "")
		done := make(chan error, 1)
		go func() {
			done <- other.Database("app").RunCommand(ctx, append(build, bson.E{Key: "maxTimeMS", Value: 20000})).Err()
		}()
		deadline := time.Now().Add(10 * time.Second)
		var rep *migrate.Report
		for {
			rep = analyze()
			if len(rep.Blockers) == 2 || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		end()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := appDB.Collection("t").Indexes().DropOne(ctx, "x_1"); err != nil {
			t.Fatal(err)
		}
		var waiting *migrate.Blocker
		for i, b := range rep.Blockers {
			if strings.HasSuffix(b.Lock, "(waiting)") {
				waiting = &rep.Blockers[i]
			}
		}
		if len(rep.Blockers) != 2 || waiting == nil || !strings.Contains(waiting.State, "waiting for a lock") || !strings.Contains(waiting.Query, "createIndexes") {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
		if f := blockedFinding(rep.Statements[0]); f == nil || !strings.Contains(f.Message, "already waiting for") {
			t.Errorf("finding %+v", f)
		}
		t.Logf("observed: %+v", *waiting)
	})
}

// TestBlockersMongoPrivileges checks the warnings for users who may not see
// other users' operations, or lockInfo, and that inprog plus serverStatus suffice.
func TestBlockersMongoPrivileges(t *testing.T) {
	p := mytest.Mongo(t)
	ctx := context.Background()
	root, err := Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Disconnect(ctx)
	admin := root.Database("admin")
	for _, cmd := range []bson.D{
		{{Key: "createRole", Value: "inprogOnly"}, {Key: "privileges", Value: bson.A{bson.D{{Key: "resource", Value: bson.D{{Key: "cluster", Value: true}}}, {Key: "actions", Value: bson.A{"inprog"}}}}}, {Key: "roles", Value: bson.A{}}},
		{{Key: "createRole", Value: "monitor"}, {Key: "privileges", Value: bson.A{bson.D{{Key: "resource", Value: bson.D{{Key: "cluster", Value: true}}}, {Key: "actions", Value: bson.A{"inprog", "serverStatus"}}}}}, {Key: "roles", Value: bson.A{}}},
		{{Key: "createUser", Value: "monitor"}, {Key: "pwd", Value: "monitor-pass"}, {Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: "app"}}, bson.D{{Key: "role", Value: "monitor"}, {Key: "db", Value: "admin"}}}}},
		{{Key: "createUser", Value: "reader"}, {Key: "pwd", Value: "reader-pass"}, {Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: "app"}}}}},
		{{Key: "createUser", Value: "watcher"}, {Key: "pwd", Value: "watcher-pass"}, {Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: "app"}}, bson.D{{Key: "role", Value: "inprogOnly"}, {Key: "db", Value: "admin"}}}}},
	} {
		if err := admin.RunCommand(ctx, cmd).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := root.Database("app").Collection("t").InsertOne(ctx, bson.M{"x": 1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ user, want string }{
		{"reader", "the inprog privilege"},
		{"watcher", "lockInfo was not read"},
		{"monitor", ""},
	} {
		t.Run(c.user, func(t *testing.T) {
			pp := p
			pp.User = c.user
			cl, err := Connect(pp, c.user+"-pass")
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Disconnect(ctx)
			base, err := ReadBase(ctx, cl, "app")
			if err != nil {
				t.Fatal(err)
			}
			rep := Analyze(base, []ingest.Input{{Name: "m.js", SQL: "db.t.createIndex({x: 1})"}})
			ReadBlockers(ctx, cl, "app", rep)
			w := strings.Join(rep.Warnings, "\n")
			if c.want == "" && (w != "" || rep.BlockersAt == nil) || !strings.Contains(w, c.want) || len(rep.Blockers) != 0 {
				t.Fatalf("warnings %v, blockers %+v", rep.Warnings, rep.Blockers)
			}
			t.Logf("observed: %s", w)
		})
	}
}

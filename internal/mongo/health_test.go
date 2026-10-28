package mongo

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/mytest"
)

func healthCheck(t *testing.T, r *health.Report, id string) health.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %+v", id, r.Checks)
	return health.Check{}
}

// TestHealthStandalone runs a slow query, then reads health as root and as
// a user with only the read role.
func TestHealthStandalone(t *testing.T) {
	defer func(d time.Duration) { slowOpAfter = d }(slowOpAfter)
	slowOpAfter = time.Second
	p := mytest.Mongo(t)
	ctx := context.Background()
	c, err := Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	app := c.Database("app")
	app.Collection("x").InsertMany(ctx, []any{bson.M{"n": 1}, bson.M{"n": 2}})
	done := make(chan struct{})
	go func() {
		app.Collection("x").Find(ctx, bson.D{{Key: "$where", Value: "sleep(1500) || true"}})
		close(done)
	}()
	time.Sleep(1500 * time.Millisecond)
	r, err := Health(ctx, c, "app")
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if ch := healthCheck(t, r, "mongo.long_operations"); ch.Status != health.Warn || !strings.Contains(ch.Detail, "app.x") {
		t.Errorf("long operations %+v", ch)
	}
	if ch := healthCheck(t, r, "mongo.replication"); ch.Status != health.OK || !strings.Contains(ch.Value, "standalone") {
		t.Errorf("replication %+v", ch)
	}
	for _, id := range []string{"mongo.connections", "mongo.cache"} {
		if ch := healthCheck(t, r, id); ch.Status != health.OK {
			t.Errorf("%s %+v", id, ch)
		}
	}

	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "createUser", Value: "reader"}, {Key: "pwd", Value: "reader-pass"},
		{Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: "app"}}}}}).Err(); err != nil {
		t.Fatal(err)
	}
	reader := p
	reader.User = "reader"
	rc, err := Connect(reader, "reader-pass")
	if err != nil {
		t.Fatal(err)
	}
	r, err = Health(ctx, rc, "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"mongo.connections", "mongo.long_operations", "mongo.replication"} {
		if ch := healthCheck(t, r, id); ch.Status != health.Unknown || !strings.Contains(ch.Detail, "clusterMonitor") {
			t.Errorf("reader %s %+v", id, ch)
		}
	}
}

// TestHealthReplicaSet initiates a one-member replica set and checks the
// replication and oplog window checks on it.
func TestHealthReplicaSet(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: mytest.MongoImage, Cmd: []string{"--replSet", "rs0", "--bind_ip_all"},
			ExposedPorts: []string{"27017/tcp"}, WaitingFor: wait.ForLog("Waiting for connections").WithStartupTimeout(2 * time.Minute)},
		Started: true,
	})
	if err != nil {
		t.Skipf("no mongo container: %v", err)
	}
	t.Cleanup(func() { ctr.Terminate(context.Background()) })
	host, _ := ctr.Host(ctx)
	port, _ := ctr.MappedPort(ctx, "27017/tcp")
	n, _ := strconv.Atoi(port.Port())
	p := conn.Profile{Engine: conn.EngineMongoDB, Host: host, Port: n, Database: "app", Options: "directConnection=true", SSLMode: conn.SSLDisable, Env: conn.EnvLocal}
	c, err := Connect(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetInitiate", Value: bson.D{{Key: "_id", Value: "rs0"},
		{Key: "members", Value: bson.A{bson.D{{Key: "_id", Value: 0}, {Key: "host", Value: "localhost:27017"}}}}}}}).Err(); err != nil {
		t.Fatal(err)
	}
	var r *health.Report
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second)
		if r, err = Health(ctx, c, "app"); err == nil && strings.Contains(healthCheck(t, r, "mongo.replication").Detail, "PRIMARY") {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if ch := healthCheck(t, r, "mongo.replication"); ch.Status != health.OK || ch.Value != "1 member" || !strings.Contains(ch.Detail, "PRIMARY") {
		t.Errorf("replication %+v", ch)
	}
	if ch := healthCheck(t, r, "mongo.oplog_window"); ch.Status != health.OK || !strings.Contains(ch.Value, "full") {
		t.Errorf("oplog window of a young set %+v", ch)
	}
}

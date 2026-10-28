package mytest

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/dbscope/dbscope/internal/conn"
)

// MongoImage is the MongoDB server the tests run against.
const MongoImage = "public.ecr.aws/docker/library/mongo:8.0"

// Mongo starts a MongoDB container and returns a root profile for its "app"
// database (root authenticates against admin, so options carry authSource).
func Mongo(t testing.TB) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_MONGO") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        MongoImage,
			Env:          map[string]string{"MONGO_INITDB_ROOT_USERNAME": "root", "MONGO_INITDB_ROOT_PASSWORD": Password},
			ExposedPorts: []string{"27017/tcp"},
			// The init script restarts mongod, so the second "Waiting for connections" is the real one.
			WaitingFor: wait.ForLog("Waiting for connections").WithOccurrence(2).WithStartupTimeout(3 * time.Minute),
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
	return conn.Profile{Engine: conn.EngineMongoDB, Host: host, Port: n, Database: "app", User: "root",
		Options: "authSource=admin&directConnection=true", SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

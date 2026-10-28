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

// Neo4jImages are the Neo4j servers the tests run against: the last 5.x
// line, and the current calendar release in both editions.
var Neo4jImages = map[string]string{
	"5":          "public.ecr.aws/docker/library/neo4j:5-community",
	"2026":       "public.ecr.aws/docker/library/neo4j:2026.10",
	"enterprise": "public.ecr.aws/docker/library/neo4j:2026.10-enterprise",
}

// Neo4jPassword is the throwaway containers' neo4j password.
const Neo4jPassword = "neo4j-test-pass"

// Neo4j starts a server and returns a profile for the neo4j user and database.
// The enterprise image runs under the evaluation license.
func Neo4j(t testing.TB, name string) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_NEO4J") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	env := map[string]string{"NEO4J_AUTH": "neo4j/" + Neo4jPassword, "NEO4J_server_memory_heap_max__size": "512m"}
	if name == "enterprise" {
		env["NEO4J_ACCEPT_LICENSE_AGREEMENT"] = "eval"
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: Neo4jImages[name], ExposedPorts: []string{"7687/tcp"}, Env: env,
			WaitingFor: wait.ForAll(wait.ForLog("Started."), wait.ForListeningPort("7687/tcp")).WithDeadline(4 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no neo4j %s container: %v", name, err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "7687/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineNeo4j, Host: host, Port: n, Database: "neo4j", User: "neo4j",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

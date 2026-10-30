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

// MemgraphImage is the Memgraph server the tests run against.
const MemgraphImage = "memgraph/memgraph:3.13.2"

// Memgraph starts a server without authentication and returns a profile for
// it; Memgraph speaks Bolt, so profiles use the neo4j engine.
func Memgraph(t testing.TB) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_NEO4J") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: MemgraphImage, ExposedPorts: []string{"7687/tcp"}, Cmd: []string{"--telemetry-enabled=false"},
			WaitingFor: wait.ForAll(wait.ForLog("You are running Memgraph"), wait.ForListeningPort("7687/tcp")).WithDeadline(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no memgraph container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "7687/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineNeo4j, Host: host, Port: n, Database: "neo4j", SSLMode: conn.SSLDisable,
		Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

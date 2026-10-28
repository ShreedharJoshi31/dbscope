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

// ClickHouseImages are the ClickHouse servers the tests run against: the
// current release and the previous LTS.
var ClickHouseImages = map[string]string{
	"26.8": "clickhouse/clickhouse-server:26.8",
	"25.8": "clickhouse/clickhouse-server:25.8",
}

// ClickHousePassword is the throwaway containers' admin password.
const ClickHousePassword = "ch-test-pass"

// ClickHouse starts a server with SQL user management on and returns a
// profile for its admin user on the default database.
func ClickHouse(t testing.TB, name string) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_CLICKHOUSE") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: ClickHouseImages[name], ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{"CLICKHOUSE_USER": "admin", "CLICKHOUSE_PASSWORD": ClickHousePassword, "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1"},
			WaitingFor: wait.ForExec([]string{"clickhouse-client", "-u", "admin", "--password", ClickHousePassword, "-q", "SELECT 1"}).
				WithPollInterval(time.Second).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no clickhouse %s container: %v", name, err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9000/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineClickHouse, Host: host, Port: n, Database: "default", User: "admin",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

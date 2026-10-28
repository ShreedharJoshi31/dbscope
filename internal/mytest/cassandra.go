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

// CassandraImages are the Cassandra and ScyllaDB servers the tests run against.
var CassandraImages = map[string]string{
	"cassandra": "public.ecr.aws/docker/library/cassandra:5.0",
	"scylla":    "scylladb/scylla:2025.1",
}

// Cassandra starts a single node with password authentication and
// CassandraAuthorizer on, so grants are enforced, and returns a profile for
// the superuser cassandra/cassandra (password: CassandraPassword) on keyspace "app",
// which the test creates.
func Cassandra(t testing.TB, flavor string) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_CASSANDRA") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	req := testcontainers.ContainerRequest{Image: CassandraImages[flavor], ExposedPorts: []string{"9042/tcp"}}
	if flavor == "scylla" {
		req.Cmd = []string{"--smp", "1", "--memory", "750M", "--overprovisioned", "1", "--developer-mode", "1",
			"--authenticator", "PasswordAuthenticator", "--authorizer", "CassandraAuthorizer"}
		req.WaitingFor = wait.ForLog("init - serving").WithStartupTimeout(4 * time.Minute)
	} else {
		req.Env = map[string]string{"MAX_HEAP_SIZE": "512M", "HEAP_NEWSIZE": "128M"}
		req.Entrypoint = []string{"sh", "-c", `sed -i -e 's/^authenticator: .*/authenticator: PasswordAuthenticator/' ` +
			`-e 's/^authorizer: .*/authorizer: CassandraAuthorizer/' /etc/cassandra/cassandra.yaml && exec docker-entrypoint.sh cassandra -f`}
		req.WaitingFor = wait.ForLog("Starting listening for CQL clients").WithStartupTimeout(4 * time.Minute)
	}
	// The default superuser is created a few seconds after the server listens.
	req.WaitingFor = wait.ForAll(req.WaitingFor, wait.ForExec([]string{"cqlsh", "-u", "cassandra", "-p", CassandraPassword, "-e", "SELECT now() FROM system.local"}).
		WithPollInterval(2*time.Second).WithStartupTimeout(2*time.Minute)).WithDeadline(6 * time.Minute)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		t.Skipf("no %s container: %v", flavor, err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9042/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineCassandra, Host: host, Port: n, Database: "app", User: "cassandra",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

// CassandraPassword is the default superuser's password in both images.
const CassandraPassword = "cassandra"

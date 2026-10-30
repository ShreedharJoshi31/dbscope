package mytest

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/dbscope/dbscope/internal/conn"
)

// CassandraImages are the Cassandra and ScyllaDB servers the tests run against.
var CassandraImages = map[string]string{
	"cassandra": "public.ecr.aws/docker/library/cassandra:5.0",
	"scylla":    "scylladb/scylla:2025.1",
}

// CassandraOptions are optional for Cassandra.
type CassandraOptions struct {
	// Settings are "name: value" pairs: lines appended to cassandra.yaml, or
	// for ScyllaDB command-line flags (--name-with-dashes value).
	Settings []string
	// Nodetool, when set, receives a function that runs nodetool in the container.
	Nodetool *func(args ...string) error
}

// Cassandra starts a single node with password authentication and
// CassandraAuthorizer on, so grants are enforced, and returns a profile for
// the superuser cassandra/cassandra (password: CassandraPassword) on keyspace "app",
// which the test creates.
func Cassandra(t testing.TB, flavor string, opts ...CassandraOptions) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_CASSANDRA") != "" {
		t.Skip("integration test")
	}
	var o CassandraOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	ctx := context.Background()
	req := testcontainers.ContainerRequest{Image: CassandraImages[flavor], ExposedPorts: []string{"9042/tcp"}}
	if flavor == "scylla" {
		req.Cmd = []string{"--smp", "1", "--memory", "750M", "--overprovisioned", "1", "--developer-mode", "1",
			"--authenticator", "PasswordAuthenticator", "--authorizer", "CassandraAuthorizer"}
		for _, s := range o.Settings {
			name, value, _ := strings.Cut(s, ":")
			req.Cmd = append(req.Cmd, "--"+strings.ReplaceAll(strings.TrimSpace(name), "_", "-"), strings.TrimSpace(value))
		}
		req.WaitingFor = wait.ForLog("init - serving").WithStartupTimeout(4 * time.Minute)
	} else {
		var appends string
		for _, s := range o.Settings {
			appends += ` && echo '` + strings.ReplaceAll(s, `'`, `'\''`) + `' >> /etc/cassandra/cassandra.yaml`
		}
		req.Env = map[string]string{"MAX_HEAP_SIZE": "512M", "HEAP_NEWSIZE": "128M"}
		req.Entrypoint = []string{"sh", "-c", `sed -i -e 's/^authenticator: .*/authenticator: PasswordAuthenticator/' ` +
			`-e 's/^authorizer: .*/authorizer: CassandraAuthorizer/' /etc/cassandra/cassandra.yaml` + appends + ` && exec docker-entrypoint.sh cassandra -f`}
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
	if o.Nodetool != nil {
		*o.Nodetool = func(args ...string) error {
			code, out, err := c.Exec(ctx, append([]string{"nodetool"}, args...), tcexec.Multiplexed())
			if err == nil && code != 0 {
				b, _ := io.ReadAll(out)
				err = fmt.Errorf("nodetool %v: exit %d: %s", args, code, b)
			}
			return err
		}
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9042/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineCassandra, Host: host, Port: n, Database: "app", User: "cassandra",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

// CassandraPassword is the default superuser's password in both images.
const CassandraPassword = "cassandra"

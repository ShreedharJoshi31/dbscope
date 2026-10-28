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

// MSSQLImages are the SQL Server releases the tests run against. Microsoft
// publishes them for amd64 only; on arm64 Docker runs them under emulation.
var MSSQLImages = map[string]string{
	"2025": "mcr.microsoft.com/mssql/server:2025-latest",
	"2022": "mcr.microsoft.com/mssql/server:2022-latest",
}

// MSSQLPassword is the throwaway containers' sa password; it meets SQL Server's complexity rule.
const MSSQLPassword = "Mssql-Test-Pass-2026!"

// MSSQL starts a Developer edition server and returns a profile for sa on master.
func MSSQL(t testing.TB, name string) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_MSSQL") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: MSSQLImages[name], ImagePlatform: "linux/amd64", ExposedPorts: []string{"1433/tcp"},
			Env: map[string]string{"ACCEPT_EULA": "Y", "MSSQL_SA_PASSWORD": MSSQLPassword, "MSSQL_PID": "Developer"},
			WaitingFor: wait.ForExec([]string{"/opt/mssql-tools18/bin/sqlcmd", "-C", "-S", "localhost", "-U", "sa", "-P", MSSQLPassword, "-Q", "SELECT 1"}).
				WithExitCodeMatcher(func(code int) bool { return code == 0 }).
				WithPollInterval(2 * time.Second).WithStartupTimeout(4 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no sql server %s container: %v", name, err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "1433/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineSQLServer, Host: host, Port: n, Database: "master", User: "sa",
		SSLMode: conn.SSLRequire, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

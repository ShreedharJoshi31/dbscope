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

// OracleImage is Oracle AI Database 26ai Free (23.26), the lite image, which
// Oracle publishes for amd64 and arm64.
const OracleImage = "container-registry.oracle.com/database/free:latest-lite"

// OraclePassword is the throwaway container's SYS and SYSTEM password.
const OraclePassword = "OraTest2026x"

// Oracle starts a server and returns a profile for SYSTEM on the FREEPDB1 service.
func Oracle(t testing.TB) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_ORACLE") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: OracleImage, ExposedPorts: []string{"1521/tcp"},
			Env:        map[string]string{"ORACLE_PWD": OraclePassword},
			WaitingFor: wait.ForLog("DATABASE IS READY TO USE").WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no oracle container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "1521/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineOracle, Host: host, Port: n, Database: "FREEPDB1", User: "system",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

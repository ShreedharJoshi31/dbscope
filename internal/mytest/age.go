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

// AGEImage is PostgreSQL 18 with the Apache AGE extension.
const AGEImage = "apache/age:release_PG18_1.8.0"

// AGEPassword is the throwaway container's postgres password.
const AGEPassword = "age-test-pass"

// AGE starts PostgreSQL with Apache AGE (not yet created in the database) and
// returns a superuser profile for the postgres database.
func AGE(t testing.TB) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_AGE") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: AGEImage, ExposedPorts: []string{"5432/tcp"}, Env: map[string]string{"POSTGRES_PASSWORD": AGEPassword},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no age container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EnginePostgres, Host: host, Port: n, Database: "postgres", User: "postgres",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

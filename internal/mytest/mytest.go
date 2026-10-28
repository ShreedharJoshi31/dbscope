// Package mytest gives integration tests a MySQL or MariaDB server in a
// container, started through testcontainers from AWS's mirror of the Docker
// official images. Tests are skipped in -short mode, with
// DBSCOPE_SKIP_MYSQL set, or when no container can start.
package mytest

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/dbscope/dbscope/internal/conn"
)

// Images per flavor.
var Images = map[string]string{
	"mysql":   "public.ecr.aws/docker/library/mysql:8.4",
	"mariadb": "public.ecr.aws/docker/library/mariadb:11.8",
}

// Password is the throwaway containers' root password.
const Password = "dbscope-test"

// Server starts a container of the flavor and returns a root profile for its "app" database.
func Server(t testing.TB, flavor string) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_MYSQL") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	env := map[string]string{"MYSQL_ROOT_PASSWORD": Password, "MYSQL_DATABASE": "app"}
	if flavor == "mariadb" {
		env = map[string]string{"MARIADB_ROOT_PASSWORD": Password, "MARIADB_DATABASE": "app"}
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: Images[flavor], Env: env, ExposedPorts: []string{"3306/tcp"},
			WaitingFor: wait.ForSQL("3306/tcp", "mysql", func(host string, port network.Port) string {
				return "root:" + Password + "@tcp(" + host + ":" + port.Port() + ")/app"
			}).WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no %s container: %v", flavor, err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "3306/tcp")
	n, _ := strconv.Atoi(port.Port())
	return conn.Profile{Engine: conn.EngineMySQL, Host: host, Port: n, Database: "app", User: "root",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

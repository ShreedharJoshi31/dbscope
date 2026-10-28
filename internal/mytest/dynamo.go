package mytest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/dbscope/dbscope/internal/conn"
)

// DynamoImage is DynamoDB Local, from AWS's own public registry.
const DynamoImage = "public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local:latest"

// Dynamo starts DynamoDB Local in memory and returns a profile for it.
func Dynamo(t testing.TB) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_DYNAMO") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: DynamoImage, ExposedPorts: []string{"8000/tcp"}, Cmd: []string{"-jar", "DynamoDBLocal.jar", "-inMemory"},
			WaitingFor: wait.ForListeningPort("8000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no dynamodb local container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "8000/tcp")
	return conn.Profile{Engine: conn.EngineDynamoDB, Options: "region=us-east-1&endpoint=http://" + host + ":" + port.Port(),
		Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

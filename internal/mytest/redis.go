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

// RedisImages are the Redis and Valkey servers the tests run against.
var RedisImages = map[string]string{
	"redis7": "redis:7-alpine",
	"redis8": "public.ecr.aws/docker/library/redis:8",
	"valkey": "valkey/valkey:8",
}

// RedisOption changes the server a Redis test starts.
type RedisOption func(*redisSetup)

type redisSetup struct {
	req testcontainers.ContainerRequest
	ip  *string
}

// RedisArgs passes server arguments, such as "--appendonly", "yes".
func RedisArgs(args ...string) RedisOption {
	return func(s *redisSetup) { s.req.Cmd = append(s.req.Cmd, args...) }
}

// RedisTmpfsData mounts /data, where the server saves, as a tmpfs of the given size ("1m").
func RedisTmpfsData(size string) RedisOption {
	return func(s *redisSetup) { s.req.Tmpfs = map[string]string{"/data": "rw,size=" + size} }
}

// RedisIP stores the container's address on the Docker network, where other containers reach it on port 6379.
func RedisIP(ip *string) RedisOption {
	return func(s *redisSetup) { s.ip = ip }
}

// Redis starts a server with no password on the default user and returns a profile for database 0.
func Redis(t testing.TB, name string, opts ...RedisOption) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_REDIS") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	s := redisSetup{req: testcontainers.ContainerRequest{
		Image: RedisImages[name], ExposedPorts: []string{"6379/tcp"},
		WaitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(2 * time.Minute),
	}}
	for _, o := range opts {
		o(&s)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: s.req, Started: true})
	if err != nil {
		t.Skipf("no %s container: %v", name, err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "6379/tcp")
	n, _ := strconv.Atoi(port.Port())
	if s.ip != nil {
		if *s.ip, err = c.ContainerIP(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return conn.Profile{Engine: conn.EngineRedis, Host: host, Port: n, Database: "0", SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
}

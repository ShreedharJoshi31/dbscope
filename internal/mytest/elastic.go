package mytest

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/dbscope/dbscope/internal/conn"
)

// ElasticImages are the Elasticsearch and OpenSearch servers the tests run against.
var ElasticImages = map[string]string{
	"es9": "public.ecr.aws/docker/library/elasticsearch:9.5.5",
	"es8": "public.ecr.aws/docker/library/elasticsearch:8.19.23",
	"os3": "opensearchproject/opensearch:3.9.0",
	"os2": "opensearchproject/opensearch:2.19.7",
}

// ElasticPassword is the elastic or admin user's password in the throwaway containers.
const ElasticPassword = "Dbscope-Test-Pass-2026!"

// Elastic starts a single node with security on and returns a profile for
// its superuser: elastic over http on Elasticsearch, admin over https with
// the demo certificate (sslMode require) on OpenSearch.
func Elastic(t testing.TB, name string) conn.Profile {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_ELASTIC") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	es := strings.HasPrefix(name, "es")
	env := map[string]string{"discovery.type": "single-node"}
	var waitFor wait.Strategy
	if es {
		env["ELASTIC_PASSWORD"] = ElasticPassword
		env["xpack.security.enabled"] = "true"
		env["xpack.security.http.ssl.enabled"] = "false"
		env["ES_JAVA_OPTS"] = "-Xms512m -Xmx512m"
		waitFor = wait.ForHTTP("/_cluster/health").WithPort("9200/tcp").WithBasicAuth("elastic", ElasticPassword).WithStartupTimeout(4 * time.Minute)
	} else {
		env["OPENSEARCH_INITIAL_ADMIN_PASSWORD"] = ElasticPassword
		env["OPENSEARCH_JAVA_OPTS"] = "-Xms512m -Xmx512m"
		waitFor = wait.ForHTTP("/_cluster/health").WithPort("9200/tcp").WithTLS(true).WithAllowInsecure(true).
			WithBasicAuth("admin", ElasticPassword).WithStartupTimeout(5 * time.Minute)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: ElasticImages[name], ExposedPorts: []string{"9200/tcp"}, Env: env, WaitingFor: waitFor},
		Started:          true,
	})
	if err != nil {
		t.Skipf("no %s container: %v", name, err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9200/tcp")
	n, _ := strconv.Atoi(port.Port())
	p := conn.Profile{Engine: conn.EngineElasticsearch, Host: host, Port: n, Database: "*", User: "elastic",
		SSLMode: conn.SSLDisable, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
	if !es {
		p.User, p.SSLMode = "admin", conn.SSLRequire
	}
	return p
}

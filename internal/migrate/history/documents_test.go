package history

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/elastic"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mongo"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/neo4j"
)

// migrate-mongo writes {fileName, appliedAt} and, with useFileHash, fileHash
// (lib/actions/up.js); the documents below have that shape.
func TestMongo(t *testing.T) {
	p := mytest.Mongo(t)
	c, err := mongo.Connect(p, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect(context.Background())
	db := c.Database(p.Database)
	files := []ingest.Input{
		{Name: "migrations/20240101000000-a.js", SQL: "module.exports = {up() {}, down() {}};\n"},
		{Name: "migrations/20240102000000-b.js", SQL: "module.exports = {up(db) { return db.collection('x').createIndex({a: 1}); }};\n"},
		{Name: "migrations/20240103000000-c.js", SQL: "module.exports = {up() {}};\n"},
		{Name: "migrate-mongo-config.json", SQL: "{}"},
	}
	check(t, sqlCase{warning: "looked for migrate-mongo's changelog collection"}, Mongo(t.Context(), db, files))

	if _, err := db.Collection("changelog").InsertMany(t.Context(), []any{
		bson.D{{Key: "fileName", Value: "20240101000000-a.js"}, {Key: "appliedAt", Value: time.Now()}},
		bson.D{{Key: "fileName", Value: "20240102000000-b.js"}, {Key: "fileHash", Value: sha256Hex("an older text")}, {Key: "appliedAt", Value: time.Now()}},
	}); err != nil {
		t.Fatal(err)
	}
	check(t, sqlCase{tool: "migrate-mongo", applied: []string{"migrations/20240101000000-a.js"}, changed: []string{"migrations/20240102000000-b.js"}},
		Mongo(t.Context(), db, files))
}

// neo4j-migrations chains (:__Neo4jMigration {version: 'BASELINE'}) to one
// node per applied migration over MIGRATED_TO, each with version,
// description, type, checksum and source (MigrationsChain, ChainBuilder).
func TestNeo4j(t *testing.T) {
	p := mytest.Neo4j(t, "5")
	c, err := neo4j.Connect(p, mytest.Neo4jPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a := "CREATE CONSTRAINT a IF NOT EXISTS FOR (n:A) REQUIRE n.id IS UNIQUE;\n"
	files := []ingest.Input{
		{Name: "neo4j/migrations/V0001__a.cypher", SQL: a},
		{Name: "neo4j/migrations/V0002__b.cypher", SQL: "CREATE INDEX b IF NOT EXISTS FOR (n:B) ON (n.x);\n"},
		{Name: "neo4j/migrations/V0003__c.cypher", SQL: "MATCH (n:C) SET n.y = 1;\n"},
	}
	check(t, sqlCase{warning: "__Neo4jMigration nodes"}, Neo4j(t.Context(), c.Driver, c.Database, files))

	_, err = drv.ExecuteQuery(t.Context(), c.Driver, `
		CREATE (b:__Neo4jMigration {version: 'BASELINE'})
		CREATE (m1:__Neo4jMigration {version: '0001', description: 'a', type: 'CYPHER', checksum: $a, source: 'V0001__a.cypher', repeatable: false})
		CREATE (m2:__Neo4jMigration {version: '0002', description: 'b', type: 'CYPHER', checksum: '1', source: 'V0002__b.cypher', repeatable: false})
		CREATE (b)-[:MIGRATED_TO {at: datetime(), in: duration('PT0.1S'), by: 'neo4j', connectedAs: 'neo4j'}]->(m1)
		CREATE (m1)-[:MIGRATED_TO {at: datetime(), in: duration('PT0.1S'), by: 'neo4j', connectedAs: 'neo4j'}]->(m2)`,
		map[string]any{"a": Neo4jChecksum(a)}, drv.EagerResultTransformer, drv.ExecuteQueryWithDatabase(c.Database))
	if err != nil {
		t.Fatal(err)
	}
	check(t, sqlCase{tool: "neo4j-migrations", applied: []string{"neo4j/migrations/V0001__a.cypher"}, changed: []string{"neo4j/migrations/V0002__b.cypher"}},
		Neo4j(t.Context(), c.Driver, c.Database, files))
}

// elasticsearch-evolution stores one document per script run in es_evolution
// with the fields of MigrationScriptProtocolMapper, id the version, and a lock
// document of version 0.1 (HistoryRepositoryImpl).
func TestElastic(t *testing.T) {
	p := mytest.Elastic(t, "es9")
	c, err := elastic.Connect(p, mytest.ElasticPassword)
	if err != nil {
		t.Fatal(err)
	}
	files := []ingest.Input{
		{Name: "es_migration/V1.0__mapping.http", SQL: "PUT /books/_mapping\n{\"properties\":{\"t\":{\"type\":\"text\"}}}\n"},
		{Name: "es_migration/V1.1__failed.http", SQL: "PUT /books/_settings\n{}\n"},
		{Name: "es_migration/V2__new.http", SQL: "PUT /books/_mapping\n{}\n"},
	}
	check(t, sqlCase{warning: "es_evolution index"}, Elastic(t.Context(), c, files))

	doc := func(version, script string, success, locked bool) {
		body := fmt.Sprintf(`{"version":%q,"indexName":"books","scriptName":%q,"description":"d","checksum":12345,"executionTimestamp":"2026-10-30T10:00:00Z","executionRuntimeInMillis":5,"success":%v,"locked":%v}`,
			version, script, success, locked)
		put(t, p, "/es_evolution/_doc/"+version+"?refresh=true", body)
	}
	doc("0.1", "-", true, true)
	doc("1.0", "V1.0__mapping.http", true, true)
	doc("1.1", "V1.1__failed.http", false, true)
	check(t, sqlCase{tool: "elasticsearch-evolution", applied: []string{"es_migration/V1.0__mapping.http"}}, Elastic(t.Context(), c, files))
}

// put writes a document as the test container's superuser (dbscope's own client only reads).
func put(t *testing.T, p conn.Profile, path, body string) {
	t.Helper()
	scheme := "http"
	if p.SSLMode != conn.SSLDisable && p.SSLMode != conn.SSLPrefer {
		scheme = "https"
	}
	req, _ := http.NewRequestWithContext(t.Context(), "PUT", scheme+"://"+net.JoinHostPort(p.Host, strconv.Itoa(p.Port))+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(p.User, mytest.ElasticPassword)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // the demo certificate
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT %s: %s", path, resp.Status)
	}
}

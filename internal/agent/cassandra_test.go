package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/cassandra"
	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestCassandraProfile(t *testing.T) {
	srv := mytest.Cassandra(t, "scylla")
	ctx := context.Background()
	setup := srv
	setup.Database = ""
	s, err := cassandra.Connect(setup, mytest.CassandraPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE KEYSPACE app WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}",
		"CREATE TABLE app.users (id text PRIMARY KEY, email text)",
		"INSERT INTO app.users (id, email) VALUES ('u1', 'a@x')",
		"CREATE ROLE ro WITH LOGIN = true AND PASSWORD = 'ro-pass'",
		"GRANT SELECT ON KEYSPACE app TO ro",
	} {
		if err := s.Query(q).ExecContext(ctx); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Cql", "engine": "cassandra", "host": srv.Host, "port": srv.Port, "database": "app",
		"user": "ro", "password": "ro-pass", "sslMode": "disable", "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if !p.Capabilities.Query || !p.Capabilities.Migrations {
		t.Fatalf("created %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/test", "")
	if !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), `"engine":"scylladb"`) {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "scylla" || len(m.Relations) != 1 || m.Relations[0].Column("email") == nil {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM users WHERE id = 'u1'"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"a@x"`) || !strings.Contains(w.Body.String(), "Single-partition read (id)") {
		t.Errorf("query %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"DELETE FROM users WHERE id = 'u1'"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM users WHERE email = 'a@x'"}`), 400, "database")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM nope"}`), 400, "database")
	bad, _ := json.Marshal(map[string]any{"name": "X", "engine": "cassandra", "host": "h", "database": "app", "options": "region=x", "env": "local"})
	expect(t, e.do("POST", "/api/profiles", string(bad)), 400, "validation")
}

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	neo4jdrv "github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/neo4j"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestNeo4jProfile(t *testing.T) {
	srv := mytest.Neo4j(t, "2026")
	ctx := context.Background()
	c, err := neo4j.Connect(srv, mytest.Neo4jPassword)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Driver.NewSession(ctx, neo4jWriteSession())
	r, err := s.Run(ctx, "CREATE (:User {id: 'u1', email: 'a@x'})-[:OWNS]->(:Repo {name: 'dbscope'})", nil)
	if err == nil {
		_, err = r.Consume(ctx)
	}
	s.Close(ctx)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Graph", "engine": "neo4j", "host": srv.Host, "port": srv.Port,
		"user": "neo4j", "password": mytest.Neo4jPassword, "sslMode": "disable", "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Database != "neo4j" || p.Port != srv.Port || !p.Capabilities.Query {
		t.Fatalf("created %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/test", "")
	if !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), "Community Edition") {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "neo4j" || len(m.Relations) != 2 || len(m.Relations[1].ForeignKeys) != 1 {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"MATCH (u:User)-[:OWNS]->(r) RETURN u.email, r.name"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"a@x"`) {
		t.Errorf("query %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"MATCH (n) DETACH DELETE n"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"CALL db.createLabel('X')"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"MATCH (n RETURN n"}`), 400, "database")
	bad, _ := json.Marshal(map[string]any{"name": "X", "engine": "neo4j", "host": "h", "options": "region=x", "env": "local"})
	expect(t, e.do("POST", "/api/profiles", string(bad)), 400, "validation")
}

func neo4jWriteSession() neo4jdrv.SessionConfig { return neo4jdrv.SessionConfig{DatabaseName: "neo4j"} }

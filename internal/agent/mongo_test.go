package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mongo"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestMongoProfile(t *testing.T) {
	srv := mytest.Mongo(t)
	c, err := mongo.Connect(srv, mytest.Password)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Database("app").Collection("users").InsertMany(ctx, []any{
		bson.D{{Key: "email", Value: "a@x"}}, bson.D{{Key: "email", Value: "b@x"}},
	}); err != nil {
		t.Fatal(err)
	}
	c.Disconnect(ctx)

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Docs", "engine": "mongodb", "host": srv.Host, "port": srv.Port,
		"database": "app", "user": "root", "password": mytest.Password, "sslMode": "disable", "env": "local", "options": srv.Options})
	w := e.do("POST", "/api/profiles/test", string(body))
	var tr conn.TestResult
	json.Unmarshal(w.Body.Bytes(), &tr)
	if !tr.OK || tr.Engine != conn.EngineMongoDB {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Options != srv.Options || !p.Capabilities.Query || p.Capabilities.Migrations {
		t.Fatalf("created %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "mongodb" || len(m.Relations) != 1 || m.Relations[0].Kind != schema.KindCollection {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"{\"find\": \"users\", \"projection\": {\"_id\": 0}, \"sort\": {\"email\": 1}}"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"rows":[["a@x"],["b@x"]]`) {
		t.Errorf("query %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/explain", `{"sql":"{\"find\": \"users\", \"filter\": {\"email\": \"a@x\"}}"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), "COLLSCAN") || !strings.Contains(w.Body.String(), `"stepsOnly":true`) {
		t.Errorf("explain %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"{\"aggregate\": \"users\", \"pipeline\": [{\"$out\": \"x\"}]}"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"db.users.find()"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"{\"find\": \"users\", \"filter\": {\"$bad\": 1}}"}`), 400, "database")
	bad, _ := json.Marshal(map[string]any{"name": "X", "engine": "postgres", "host": "h", "database": "d", "user": "u", "env": "local", "options": "a=b"})
	expect(t, e.do("POST", "/api/profiles", string(bad)), 400, "validation")
}

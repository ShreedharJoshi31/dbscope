package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestElasticProfile(t *testing.T) {
	srv := mytest.Elastic(t, "es9")
	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Search", "engine": "elasticsearch", "host": srv.Host, "port": srv.Port,
		"user": srv.User, "password": mytest.ElasticPassword, "sslMode": "disable", "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Database != "*" || !p.Capabilities.Query {
		t.Fatalf("created %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/test", "")
	if !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), "cluster all") {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "elasticsearch" {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SHOW TABLES"}`)
	expect(t, w, 200, "")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"DELETE FROM x"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"{\"index\": \"nope\", \"query\": {\"match_all\": {}}}"}`), 400, "database")
	bad, _ := json.Marshal(map[string]any{"name": "X", "engine": "elasticsearch", "host": "h", "options": "x=1", "env": "local"})
	expect(t, e.do("POST", "/api/profiles", string(bad)), 400, "validation")
}

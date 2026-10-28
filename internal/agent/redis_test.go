package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/redis"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestRedisProfile(t *testing.T) {
	srv := mytest.Redis(t, "redis8")
	c, err := redis.Connect(srv, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		c.HSet(ctx, "user:"+string(rune('a'+i%26))+strings.Repeat("1", i%3)+":1"+strings.Repeat("0", i)+":profile", "n", "x")
		c.Set(ctx, "order:"+strings.Repeat("9", i+1), "x", 0)
	}
	c.Close()

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Cache", "engine": "redis", "host": srv.Host, "port": srv.Port, "sslMode": "disable", "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Database != "0" || !p.Capabilities.Keyspace || p.Capabilities.Query {
		t.Fatalf("created %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/test", "")
	var tr conn.TestResult
	json.Unmarshal(w.Body.Bytes(), &tr)
	if !tr.OK || tr.Engine != conn.EngineRedis {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "redis" || len(m.Relations) == 0 || m.Relations[0].Kind != schema.KindKeyPattern {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/keyspace", "")
	expect(t, w, 200, "")
	var rep redis.Report
	json.Unmarshal(w.Body.Bytes(), &rep)
	if rep.Scanned != 100 || !rep.Complete {
		t.Errorf("keyspace %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"pattern":"order:*"`) {
		t.Errorf("patterns %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"GET x"}`), 501, "unsupported")
}

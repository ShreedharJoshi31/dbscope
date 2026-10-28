package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/clickhouse"
	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestClickHouseProfile(t *testing.T) {
	srv := mytest.ClickHouse(t, "26.8")
	ctx := context.Background()
	c, err := clickhouse.Connect(srv, mytest.ClickHousePassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE default.hits (at DateTime, url String) ENGINE = MergeTree ORDER BY at",
		"INSERT INTO default.hits VALUES ('2026-10-28 10:00:00', '/a')",
	} {
		if err := c.Conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Analytics", "engine": "clickhouse", "host": srv.Host, "port": srv.Port,
		"user": srv.User, "password": mytest.ClickHousePassword, "sslMode": "disable", "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Database != "default" || !p.Capabilities.Query {
		t.Fatalf("created %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/test", "")
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "clickhouse" || len(m.Relations) != 1 || m.Relations[0].Column("url") == nil {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT url FROM hits"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"/a"`) || !strings.Contains(w.Body.String(), `"plan":{`) {
		t.Errorf("query %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"INSERT INTO hits VALUES (now(), 'x')"}`), 422, "rejected")
	// The admin may insert, but dbscope's readonly = 1 refuses it at the server.
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT 1 SETTINGS readonly = 0"}`)
	expect(t, w, 400, "database")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM nope"}`), 400, "database")
}

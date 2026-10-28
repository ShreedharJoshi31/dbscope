package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mysql"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestMySQLProfile(t *testing.T) {
	srv := mytest.Server(t, "mysql")
	db, err := mysql.Open(srv, mytest.Password, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE users (id BIGINT AUTO_INCREMENT PRIMARY KEY, email VARCHAR(100) NOT NULL)`,
		`CREATE TABLE orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT, FOREIGN KEY (user_id) REFERENCES users (id))`,
		`INSERT INTO users (email) VALUES ('a@x')`,
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Shop", "engine": "mysql", "host": srv.Host, "port": srv.Port,
		"database": "app", "user": "root", "password": mytest.Password, "sslMode": "disable", "env": "local"})
	w := e.do("POST", "/api/profiles/test", string(body))
	var tr conn.TestResult
	json.Unmarshal(w.Body.Bytes(), &tr)
	if !tr.OK || tr.Engine != conn.EngineMySQL || tr.ServerVersionNum < 80000 {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Engine != conn.EngineMySQL || !p.Capabilities.Query || !p.Capabilities.Migrations {
		t.Fatalf("created %s", w.Body.String())
	}

	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "mysql" || len(m.Relations) != 2 {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT email FROM users"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"rows":[["a@x"]]`) {
		t.Errorf("query %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM users INTO OUTFILE '/tmp/x'"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM nope"}`), 400, "database")
	expect(t, e.do("GET", "/api/profiles/"+p.ID+"/workload", ""), 501, "unsupported")
	w = e.do("POST", "/api/profiles/"+p.ID+"/migrations/analyze", `{"files":[{"name":"1.sql","sql":"ALTER TABLE users MODIFY email TEXT NOT NULL;"}]}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"engine":"mysql"`) || !strings.Contains(w.Body.String(), "DBS-MYSQL-BLOCKS-WRITES") {
		t.Errorf("analyze %s", w.Body.String())
	}
}

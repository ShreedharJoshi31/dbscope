package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mssql"
	"github.com/dbscope/dbscope/internal/mytest"
)

func TestMSSQLMigrations(t *testing.T) {
	srv := mytest.MSSQL(t, "2022")
	db, err := mssql.Open(srv, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE users (id int PRIMARY KEY, email varchar(100) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	files := `{"files":[{"name":"1.sql","sql":"ALTER TABLE users ALTER COLUMN email nvarchar(100) NOT NULL\nGO\nALTER TABLE users DROP COLUMN email"}]}`

	src := newMSSQLSource()
	defer src.Close()
	srv.ID = "direct"
	rep, err := src.Analyze(context.Background(), srv, mytest.MSSQLPassword, []ingest.Input{{Name: "1.sql", SQL: "ALTER TABLE users ALTER COLUMN email nvarchar(100) NOT NULL"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Engine != "sqlserver" || len(rep.Statements) != 1 || rep.Statements[0].Work != "rewrite" || rep.BlockersAt == nil {
		t.Errorf("direct %+v", rep)
	}

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Shop", "engine": "sqlserver", "host": srv.Host, "port": srv.Port,
		"database": "master", "user": "sa", "password": mytest.MSSQLPassword, "sslMode": "require", "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	w = e.do("POST", "/api/profiles/"+p.ID+"/migrations/analyze", files)
	if !p.Capabilities.Migrations {
		// Until internal/conn turns the capability on for SQL Server.
		expect(t, w, 501, "unsupported")
		return
	}
	expect(t, w, 200, "")
	var got migrate.Report
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Engine != "sqlserver" || len(got.Statements) != 2 || !strings.Contains(w.Body.String(), "DBS-MSSQL-BLOCKS-TABLE") ||
		!strings.Contains(w.Body.String(), "DBS-MSSQL-DROPS-DATA") {
		t.Errorf("analyze %s", w.Body.String())
	}
}

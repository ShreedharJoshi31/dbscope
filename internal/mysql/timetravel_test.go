package mysql

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

// Runs the flavors one after the other, so only one container is up at a time.
func TestTimeTravelDetection(t *testing.T) {
	const partitionNote = "History is partitioned; dropped history partitions read as empty, not as an error"
	versioned := &schema.TimeTravel{Kind: "system-versioned", Available: true}
	for _, c := range []struct {
		flavor string
		ddl    []string
		model  schema.ModelTimeTravel
		want   map[string]*schema.TimeTravel
	}{
		{"mariadb", []string{
			"CREATE TABLE acct (id INT PRIMARY KEY, bal INT) WITH SYSTEM VERSIONING",
			`CREATE TABLE ph (id INT PRIMARY KEY, bal INT) WITH SYSTEM VERSIONING
				PARTITION BY SYSTEM_TIME LIMIT 1 (PARTITION p0 HISTORY, PARTITION p1 HISTORY, PARTITION pn CURRENT)`,
			"CREATE TABLE pi (id INT PRIMARY KEY, bal INT) WITH SYSTEM VERSIONING PARTITION BY SYSTEM_TIME INTERVAL 1 HOUR PARTITIONS 3",
			"CREATE TABLE vh (id INT PRIMARY KEY, bal INT) WITH SYSTEM VERSIONING PARTITION BY HASH (id) PARTITIONS 2",
			"CREATE TABLE plain (id INT PRIMARY KEY, bal INT)",
			"CREATE TABLE plain_parts (id INT PRIMARY KEY) PARTITION BY HASH (id) PARTITIONS 2",
			"CREATE VIEW v_acct AS SELECT id, bal FROM acct",
		}, schema.ModelTimeTravel{Supported: true}, map[string]*schema.TimeTravel{
			"acct":        versioned,
			"ph":          {Kind: "system-versioned", Available: true, Note: partitionNote},
			"pi":          {Kind: "system-versioned", Available: true, Note: partitionNote},
			"vh":          versioned,
			"plain":       nil,
			"plain_parts": nil,
			"v_acct":      nil,
		}},
		{"mysql", []string{
			"CREATE TABLE acct (id INT PRIMARY KEY, bal INT)",
			"CREATE TABLE plain_parts (id INT PRIMARY KEY) PARTITION BY HASH (id) PARTITIONS 2",
			"CREATE VIEW v_acct AS SELECT id, bal FROM acct",
		}, schema.ModelTimeTravel{Supported: false, Note: "MySQL has no system-versioned tables; MariaDB does (WITH SYSTEM VERSIONING)"},
			map[string]*schema.TimeTravel{"acct": nil, "plain_parts": nil, "v_acct": nil}},
	} {
		t.Run(c.flavor, func(t *testing.T) {
			p := mytest.Server(t, c.flavor)
			ctx := context.Background()
			db, err := Open(p, mytest.Password, 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, q := range append(c.ddl, "CREATE USER 'reader'@'%' IDENTIFIED BY 'reader-test'", "GRANT SELECT ON app.* TO 'reader'@'%'") {
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			reader := p
			reader.User = "reader"
			rd, err := Open(reader, "reader-test", 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer rd.Close()
			for _, u := range []struct {
				user string
				db   *sql.DB
			}{{"root", db}, {"reader", rd}} {
				user := u.user
				m, err := Introspect(ctx, u.db, nil, "app")
				if err != nil {
					t.Fatalf("%s: %v", user, err)
				}
				if m.TimeTravel == nil || *m.TimeTravel != c.model {
					t.Errorf("%s: model time travel %+v, want %+v", user, m.TimeTravel, c.model)
				}
				got := map[string]*schema.TimeTravel{}
				for _, r := range m.Relations {
					got[r.Name] = r.TimeTravel
					if r.Name == "acct" && r.Kind != schema.KindTable {
						t.Errorf("%s: acct kind %s", user, r.Kind)
					}
				}
				if !reflect.DeepEqual(got, c.want) {
					for n, tt := range got {
						t.Logf("%s got %s: %+v", user, n, tt)
					}
					t.Errorf("%s: time travel flags differ", user)
				}
			}
		})
	}
}

package mysql

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestBusyTable writes 4,000 rows to one table and 20 to another on a real
// server, then checks DBS-HOT-TABLE against what performance_schema counted:
// a FULLTEXT index build (blocks writes) on the busy table is rated one step
// above its DBS-MYSQL-BLOCKS-WRITES, a metadata-only change there without
// lock_wait_timeout names the table at Medium, and the quiet table or a
// bounded wait gets neither. MariaDB's default (performance_schema off) leaves
// ratings alone and says why. One container at a time.
func TestBusyTable(t *testing.T) {
	t.Run("mysql", func(t *testing.T) { testBusyTable(t, "mysql", true) })
	t.Run("mariadb", func(t *testing.T) { testBusyTable(t, "mariadb", true, "--performance-schema=ON") })
	t.Run("mariadb-off", func(t *testing.T) { testBusyTable(t, "mariadb", false) })
}

func testBusyTable(t *testing.T, flavor string, counted bool, args ...string) {
	p := mytest.Server(t, flavor, args...)
	ctx := context.Background()
	db, err := Open(p, mytest.Password, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	values := func(n int) string {
		v := make([]string, n)
		for i := range v {
			v[i] = fmt.Sprintf("(%d, 'x')", i+1)
		}
		return strings.Join(v, ",")
	}
	for _, q := range []string{
		"CREATE TABLE hot (id INT PRIMARY KEY, body VARCHAR(50))",
		"CREATE TABLE quiet (id INT PRIMARY KEY, body VARCHAR(50))",
		"INSERT INTO hot VALUES " + values(2000),
		"UPDATE hot SET body = 'y'",
		"INSERT INTO quiet VALUES " + values(20),
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q[:min(len(q), 40)], err)
		}
	}
	base, err := ReadBase(ctx, db, nil, p.Database)
	if err != nil {
		t.Fatal(err)
	}
	if counted != (base.Traffic != nil) {
		t.Fatalf("traffic read = %v, want %v (warnings %v)", base.Traffic != nil, counted, base.Warnings)
	}
	if !counted {
		if len(base.Warnings) != 1 || !strings.Contains(base.Warnings[0], "performance_schema is off") {
			t.Fatalf("warnings: %v", base.Warnings)
		}
	} else if w := base.Traffic.Writes["app.hot"]; w < 4000 {
		t.Fatalf("hot rows written = %d, want at least 4000 (%+v)", w, base.Traffic)
	}

	find := func(st migrate.Statement, id string) *migrate.Finding {
		for i := range st.Findings {
			if st.Findings[i].RuleID == id {
				return &st.Findings[i]
			}
		}
		return nil
	}
	for _, c := range []struct {
		name, sql string
		hot       score.Rating // "" when no DBS-HOT-TABLE is expected
	}{
		{"fulltext on the busy table", "ALTER TABLE hot ADD FULLTEXT INDEX ft (body);", score.High},
		{"fulltext on the quiet table", "ALTER TABLE quiet ADD FULLTEXT INDEX ft (body);", ""},
		{"instant change, no lock_wait_timeout", "ALTER TABLE hot ADD COLUMN c INT;", score.Medium},
		{"instant change, bounded wait", "SET SESSION lock_wait_timeout = 5;\nALTER TABLE hot ADD COLUMN c INT;", ""},
	} {
		rep := Analyze(base, []ingest.Input{{Name: "1.sql", SQL: c.sql}})
		st := rep.Statements[len(rep.Statements)-1]
		f := find(st, "DBS-HOT-TABLE")
		want := c.hot
		if !counted {
			want = ""
		}
		switch {
		case want == "" && f != nil:
			t.Errorf("%s: unexpected %+v", c.name, *f)
		case want != "" && f == nil:
			t.Errorf("%s: no DBS-HOT-TABLE in %+v", c.name, st.Findings)
		case want != "" && (f.Rating != want || st.Rating != score.Max(st.Rating, want) || !strings.Contains(f.Message, "app.hot has")):
			t.Errorf("%s: %s %q, statement %s; want %s", c.name, f.Rating, f.Message, st.Rating, want)
		}
		if want == score.High && f != nil {
			if b := find(st, "DBS-MYSQL-BLOCKS-WRITES"); b == nil || b.Rating.Up() != f.Rating {
				t.Errorf("%s: busy rating is not one step above DBS-MYSQL-BLOCKS-WRITES: %+v", c.name, st.Findings)
			}
		}
	}
}

package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

func analyzeBase() *Base {
	return &Base{
		DDL: []string{
			"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, name TEXT)",
			"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users ON DELETE CASCADE, total REAL)",
			"CREATE INDEX orders_user ON orders (user_id)",
		},
		Rows:   map[string]int64{"users": 2_000_000, "orders": 50_000},
		Source: "live",
	}
}

// findings maps "file:line" to the rule IDs found there.
func findings(r *migrate.Report) map[string][]string {
	out := map[string][]string{}
	for _, s := range r.Statements {
		for _, f := range s.Findings {
			k := s.File + ":" + strings.Fields(s.SQL)[0] + ":" + strconv.Itoa(s.Line)
			out[k] = append(out[k], f.RuleID+"/"+string(f.Rating))
		}
	}
	return out
}

func TestAnalyze(t *testing.T) {
	dir := t.TempDir()
	vacuumTarget := filepath.Join(dir, "copy.db")
	files := []ingest.Input{
		{Name: "1_alter.sql", SQL: `ALTER TABLE users ADD COLUMN status TEXT NOT NULL;
ALTER TABLE users ADD COLUMN nickname TEXT;
ALTER TABLE users DROP COLUMN name;
CREATE INDEX users_email ON users (email);
ALTER TABLE users RENAME COLUMN nickname TO handle;`},
		{Name: "2_drop.sql", SQL: `BEGIN;
PRAGMA foreign_keys = OFF;
DROP TABLE users;
COMMIT;`},
		{Name: "3_rebuild.sql", SQL: `ALTER TABLE orders RENAME TO orders_old;
CREATE TABLE orders (id INTEGER PRIMARY KEY, total REAL NOT NULL DEFAULT 0);
INSERT INTO orders (id, total) SELECT id, coalesce(total, 0) FROM orders_old;
DROP TABLE orders_old;`},
		{Name: "4_risky.sql", SQL: `PRAGMA journal_mode = OFF;
VACUUM INTO '` + vacuumTarget + `';
ATTACH '` + filepath.Join(dir, "other.db") + `' AS other;
VACUUM;`},
	}
	rep, err := Analyze(context.Background(), analyzeBase(), files)
	if err != nil {
		t.Fatal(err)
	}
	got := findings(rep)
	want := map[string][]string{
		"1_alter.sql:ALTER:1":    {"DBS-SQLITE-NOT-NULL-NO-DEFAULT/high"},
		"1_alter.sql:ALTER:3":    {"DBS-SQLITE-VERSION/medium", "DBS-SQLITE-TABLE-WORK/high"},
		"1_alter.sql:CREATE:4":   {"DBS-SQLITE-TABLE-WORK/high"},
		"1_alter.sql:ALTER:5":    {"DBS-SQLITE-VERSION/low"},
		"2_drop.sql:PRAGMA:2":    {"DBS-SQLITE-FK-PRAGMA-IN-TX/high"},
		"2_drop.sql:DROP:3":      {"DBS-SQLITE-DROP-CASCADE/high"},
		"3_rebuild.sql:CREATE:2": {"DBS-SQLITE-RENAME-FIRST/high"},
		"3_rebuild.sql:INSERT:3": {"DBS-SQLITE-TABLE-WORK/low"}, // the copy reads the renamed 50,000-row orders
		"4_risky.sql:PRAGMA:1":   {"DBS-SQLITE-UNSAFE-PRAGMA/high"},
		"4_risky.sql:VACUUM:2":   {"DBS-SQLITE-NOT-RUN/medium", "DBS-SQLITE-VACUUM/high"},
		"4_risky.sql:ATTACH:3":   {"DBS-SQLITE-NOT-RUN/medium"},
		"4_risky.sql:VACUUM:4":   {"DBS-SQLITE-VACUUM/high"},
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected %s: %v", k, got[k])
		}
	}
	if rep.Rating != score.High || rep.Engine != "sqlite" || rep.Base != "live" {
		t.Errorf("report %s %s %s", rep.Rating, rep.Engine, rep.Base)
	}
	for _, f := range []string{vacuumTarget, filepath.Join(dir, "other.db")} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("analysis created %s", f)
		}
	}
	if !slices.Equal(rep.Diff.Dropped, []string{"main.users"}) || !slices.Equal(rep.Diff.Altered, []string{"main.orders"}) {
		t.Errorf("diff %+v", rep.Diff)
	}
	var drop migrate.Statement
	for _, s := range rep.Statements {
		if strings.HasPrefix(s.SQL, "ALTER TABLE users DROP") {
			drop = s
		}
	}
	if drop.Work != "rewrite" || len(drop.Locks) != 1 || drop.Locks[0].Mode != "DATABASE WRITE" {
		t.Errorf("drop column %+v", drop)
	}
}

func TestAnalyzeForeignKeysOffFirst(t *testing.T) {
	rep, err := Analyze(context.Background(), analyzeBase(), []ingest.Input{{Name: "1.sql", SQL: `PRAGMA foreign_keys = OFF;
BEGIN;
DROP TABLE users;
COMMIT;
PRAGMA foreign_keys = ON;`}})
	if err != nil {
		t.Fatal(err)
	}
	got := findings(rep)
	if !slices.Equal(got["1.sql:PRAGMA:1"], []string{"DBS-SQLITE-FK-PRAGMA-IN-TX/medium"}) || got["1.sql:DROP:3"] != nil {
		t.Fatalf("%v", got)
	}
}

func TestBaseFromLiveFile(t *testing.T) {
	db, err := Open(fixtureFile(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base, err := ReadBase(context.Background(), db.DB)
	if err != nil {
		t.Fatal(err)
	}
	if base.Rows["users"] != 2 || base.Rows["orders"] != 1 || base.Rows["events"] != 0 {
		t.Errorf("rows %v", base.Rows)
	}
	rep, err := Analyze(context.Background(), base, []ingest.Input{{Name: "1.sql", SQL: "ALTER TABLE users DROP COLUMN name;\nALTER TABLE orders DROP COLUMN total;"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 { // only the SQLite version note: the base replays cleanly, FTS included
		t.Errorf("warnings %v", rep.Warnings)
	}
	got := findings(rep)
	// name is indexed (users_name_lower), so SQLite refuses to drop it and nothing is rewritten.
	if !slices.Equal(got["1.sql:ALTER:1"], []string{"DBS-SQLITE-REJECTED/high", "DBS-SQLITE-VERSION/medium"}) {
		t.Errorf("indexed column: %v", got["1.sql:ALTER:1"])
	}
	if !slices.Equal(got["1.sql:ALTER:2"], []string{"DBS-SQLITE-VERSION/medium", "DBS-SQLITE-TABLE-WORK/low"}) {
		t.Errorf("plain column: %v", got["1.sql:ALTER:2"])
	}
}

// Cases from review: names in another case, foreign keys turned back on,
// unsized index builds, CTE-led DML, savepoint transactions, and defaults
// SQLite only refuses on tables with rows.
func TestAnalyzeReviewedCases(t *testing.T) {
	cases := map[string]struct {
		sql  string
		want map[string][]string
	}{
		"case-insensitive names": {"ALTER TABLE USERS ADD COLUMN status TEXT NOT NULL;\nCREATE INDEX i ON Users (email);", map[string][]string{
			"1.sql:ALTER:1":  {"DBS-SQLITE-NOT-NULL-NO-DEFAULT/high"},
			"1.sql:CREATE:2": {"DBS-SQLITE-TABLE-WORK/high"},
		}},
		"foreign keys back on": {"PRAGMA foreign_keys = OFF;\nPRAGMA foreign_keys = ON;\nDROP TABLE users;", map[string][]string{
			"1.sql:PRAGMA:1": {"DBS-SQLITE-FK-PRAGMA-IN-TX/medium"},
			"1.sql:DROP:3":   {"DBS-SQLITE-DROP-CASCADE/high"},
		}},
		"cte-led update": {"WITH s AS (SELECT id FROM orders WHERE total > 1) UPDATE users SET name = (SELECT 'x' WHERE 1);", map[string][]string{
			"1.sql:WITH:1": {"DBS-SQLITE-TABLE-WORK/high"},
		}},
		"savepoint released": {"SAVEPOINT a;\nRELEASE a;\nPRAGMA foreign_keys = OFF;", map[string][]string{
			"1.sql:PRAGMA:3": {"DBS-SQLITE-FK-PRAGMA-IN-TX/medium"},
		}},
		"defaults checked against rows": {"ALTER TABLE users ADD COLUMN created TEXT DEFAULT CURRENT_TIMESTAMP;\nALTER TABLE users ADD COLUMN n INTEGER DEFAULT (1 + 1);\nALTER TABLE orders ADD COLUMN uid INTEGER REFERENCES users DEFAULT 1;\nALTER TABLE orders ADD COLUMN ok INTEGER REFERENCES users DEFAULT NULL;", map[string][]string{
			"1.sql:ALTER:1": {"DBS-SQLITE-NONCONSTANT-DEFAULT/high"},
			"1.sql:ALTER:2": {"DBS-SQLITE-NONCONSTANT-DEFAULT/high"},
			"1.sql:ALTER:3": {"DBS-SQLITE-REFERENCES-DEFAULT/high"},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rep, err := Analyze(context.Background(), analyzeBase(), []ingest.Input{{Name: "1.sql", SQL: c.sql}})
			if err != nil {
				t.Fatal(err)
			}
			got := findings(rep)
			for k, v := range c.want {
				if !slices.Equal(got[k], v) {
					t.Errorf("%s: %v, want %v", k, got[k], v)
				}
			}
			for k := range got {
				if _, ok := c.want[k]; !ok {
					t.Errorf("unexpected %s: %v", k, got[k])
				}
			}
			if name == "savepoint released" && len(rep.Warnings) != 1 {
				t.Errorf("warnings %v", rep.Warnings)
			}
		})
	}

	// A schema dump has no sizes: an index build is still work of unknown size.
	dump := BaseFromDump("CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT);\nINSERT INTO users VALUES (1, 'a');\nPRAGMA temp_store_directory = '/tmp';")
	rep, err := Analyze(context.Background(), dump, []ingest.Input{{Name: "1.sql", SQL: "CREATE INDEX i ON users (email);"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := findings(rep)["1.sql:CREATE:1"]; !slices.Equal(got, []string{"DBS-SQLITE-TABLE-WORK/medium"}) {
		t.Errorf("unsized index: %v", got)
	}
	if !slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.HasPrefix(w, "2 statements of the base schema") }) {
		t.Errorf("warnings %v", rep.Warnings)
	}

	// A rename in one file and a CREATE in the next is still rename-first.
	rep, err = Analyze(context.Background(), analyzeBase(), []ingest.Input{
		{Name: "1.sql", SQL: "ALTER TABLE orders RENAME TO orders_old;"},
		{Name: "2.sql", SQL: "CREATE TABLE orders (id INTEGER PRIMARY KEY);"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := findings(rep)["2.sql:CREATE:1"]; !slices.Equal(got, []string{"DBS-SQLITE-RENAME-FIRST/high"}) {
		t.Errorf("rename across files: %v", got)
	}
}

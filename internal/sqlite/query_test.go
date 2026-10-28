package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/query/guard"
)

func TestCheck(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"  with t as (select 1) select * from t;",
		"VALUES (1), (2)",
		"EXPLAIN QUERY PLAN SELECT * FROM users",
		"SELECT ';' AS semi, \"a;b\" FROM [x;y] -- trailing; comment",
		"SELECT 1; ;",
		"/* lead */ SELECT attach FROM t",
	}
	for _, q := range ok {
		if err := Check(q); err != nil {
			t.Errorf("%q rejected: %v", q, err)
		}
	}
	bad := map[string]guard.Reason{
		"SELECT 1; SELECT 2":                 guard.MultipleStatements,
		"DELETE FROM users":                  guard.NotSelect,
		"ATTACH '/etc/passwd' AS p":          guard.NotSelect,
		"PRAGMA query_only = 0":              guard.NotSelect,
		"VACUUM INTO '/tmp/copy.db'":         guard.NotSelect,
		"SELECT load_extension('/tmp/evil')": guard.FunctionNotAllowed,
		"   -- only a comment":               guard.Syntax,
		"SELECT 'it''s'; DROP TABLE users":   guard.MultipleStatements,
	}
	for q, want := range bad {
		var ge *guard.Error
		if err := Check(q); !errors.As(err, &ge) || ge.Reason != want {
			t.Errorf("%q: %v, want %s", q, err, want)
		}
	}
}

func TestRun(t *testing.T) {
	db, err := Open(fixtureFile(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	res, err := Run(ctx, db.DB, "SELECT id, email, 9007199254740993 AS big, x'00ff' AS blob, 1.5 AS f, NULL AS n FROM users ORDER BY id;", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.RowCount != 1 || len(res.Columns) != 6 {
		t.Fatalf("result %+v", res)
	}
	var got []string
	for _, v := range res.Rows[0] {
		got = append(got, string(v))
	}
	if want := `1,"a@x","9007199254740993","\\x00ff",1.5,null`; strings.Join(got, ",") != want {
		t.Errorf("row %s, want %s", strings.Join(got, ","), want)
	}
	if res.Plan == nil || len(res.Plan.Children) == 0 || !strings.Contains(res.Plan.Children[0].NodeType, "users") {
		t.Errorf("plan %+v", res.Plan)
	}

	res, err = Run(ctx, db.DB, "SELECT * FROM orders WHERE user_id = 1", 10, true)
	if err != nil || res.Plan == nil || len(res.Rows) != 0 {
		t.Fatalf("explain only: %+v %v", res, err)
	}
}

func TestRunCannotWrite(t *testing.T) {
	path := fixtureFile(t)
	db, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Check would refuse these; Run must still fail on its own.
	for _, q := range []string{
		"DELETE FROM users",
		"WITH x AS (SELECT 1) INSERT INTO users (email) SELECT 'c@x' FROM x",
		"VACUUM INTO '" + filepath.Join(t.TempDir(), "copy.db") + "'",
		"SELECT load_extension('nonexistent')",
		"ATTACH '" + filepath.Join(t.TempDir(), "new.db") + "' AS n",
	} {
		if _, err := Run(context.Background(), db.DB, q, 10, false); err == nil {
			t.Errorf("%s succeeded", q)
		}
	}
	w, _ := sql.Open("sqlite", path)
	defer w.Close()
	var n int
	if err := w.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("users = %d, %v", n, err)
	}
}

func TestRunStopsAtDeadline(t *testing.T) {
	db, err := Open(fixtureFile(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Run(ctx, db.DB, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c", 10, false)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
)

const fixture = `
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  name TEXT DEFAULT 'anon',
  email_lower TEXT GENERATED ALWAYS AS (lower(email)) VIRTUAL
);
CREATE TABLE orders (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,
  total REAL,
  status TEXT,
  FOREIGN KEY (status) REFERENCES statuses (code)
);
CREATE TABLE statuses (code TEXT PRIMARY KEY, label TEXT) WITHOUT ROWID;
CREATE TABLE events (id INTEGER PRIMARY KEY DESC, at TEXT);
CREATE TABLE tags (order_id INTEGER, tag TEXT, PRIMARY KEY (order_id, tag),
  FOREIGN KEY (order_id) REFERENCES orders (id)) STRICT;
CREATE INDEX orders_open ON orders (user_id) WHERE status <> 'closed';
CREATE INDEX users_name_lower ON users (lower(name));
CREATE VIEW open_orders AS SELECT id, user_id FROM orders WHERE status = 'open';
CREATE VIRTUAL TABLE notes USING fts5(body);
INSERT INTO users (email) VALUES ('a@x'), ('b@x');
INSERT INTO statuses VALUES ('open', 'Open');
INSERT INTO orders (user_id, total, status) VALUES (1, 9.5, 'open');
ANALYZE;
`

func fixtureFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fixture); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIntrospect(t *testing.T) {
	db, err := Open(fixtureFile(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := Introspect(context.Background(), db.DB)
	if err != nil {
		t.Fatal(err)
	}
	if m.Engine != "sqlite" || m.ServerVersion < 3_035_000 {
		t.Fatalf("engine %q version %d", m.Engine, m.ServerVersion)
	}
	var names []string
	for _, r := range m.Relations {
		names = append(names, string(r.Kind)+":"+r.Name)
	}
	if want := []string{"table:events", "view:open_orders", "table:orders", "table:statuses", "table:tags", "table:users"}; !slices.Equal(names, want) {
		t.Fatalf("relations %v, want %v (the FTS table and its shadow tables are skipped)", names, want)
	}

	users := m.Relation(schema.QualifiedName{Schema: "main", Name: "users"})
	if id := users.Column("id"); id.Identity != "by_default" || id.Nullable {
		t.Errorf("users.id %+v", id)
	}
	if c := users.Column("email_lower"); c.GeneratedKind != "virtual" {
		t.Errorf("generated column %+v", c)
	}
	if c := users.Column("name"); c.Default != "'anon'" || !c.Nullable {
		t.Errorf("users.name %+v", c)
	}
	if !slices.EqualFunc(users.Uniques, [][]string{{"email"}}, slices.Equal) {
		t.Errorf("users uniques %v", users.Uniques)
	}
	if users.RowEstimate == nil || *users.RowEstimate != 2 {
		t.Errorf("users row estimate %v", users.RowEstimate)
	}

	orders := m.Relation(schema.QualifiedName{Schema: "main", Name: "orders"})
	if len(orders.ForeignKeys) != 2 {
		t.Fatalf("orders fks %+v", orders.ForeignKeys)
	}
	byCol := map[string]schema.ForeignKey{}
	for _, fk := range orders.ForeignKeys {
		byCol[fk.Columns[0]] = fk
	}
	if fk := byCol["user_id"]; fk.Ref.Name != "users" || !slices.Equal(fk.RefCols, []string{"id"}) || fk.OnDelete != "cascade" {
		t.Errorf("REFERENCES users without columns: %+v", fk)
	}
	if fk := byCol["status"]; fk.Ref.Name != "statuses" || !slices.Equal(fk.RefCols, []string{"code"}) {
		t.Errorf("status fk %+v", fk)
	}
	var partial *schema.Index
	for i := range orders.Indexes {
		if orders.Indexes[i].Name == "orders_open" {
			partial = &orders.Indexes[i]
		}
	}
	if partial == nil || partial.Partial != "status <> 'closed'" || !slices.Equal(partial.Columns, []string{"user_id"}) {
		t.Errorf("partial index %+v", partial)
	}

	statuses := m.Relation(schema.QualifiedName{Schema: "main", Name: "statuses"})
	if c := statuses.Column("code"); c.Identity != "" || c.Nullable {
		t.Errorf("WITHOUT ROWID text key %+v", c)
	}
	if c := m.Relation(schema.QualifiedName{Schema: "main", Name: "events"}).Column("id"); c.Identity != "" {
		t.Errorf("INTEGER PRIMARY KEY DESC is not the rowid: %+v", c)
	}
	tags := m.Relation(schema.QualifiedName{Schema: "main", Name: "tags"})
	if !slices.Equal(tags.PrimaryKey, []string{"order_id", "tag"}) || tags.Column("order_id").Identity != "" {
		t.Errorf("composite key %v %+v", tags.PrimaryKey, tags.Column("order_id"))
	}
	view := m.Relation(schema.QualifiedName{Schema: "main", Name: "open_orders"})
	if len(view.Columns) != 2 {
		t.Errorf("view columns %+v", view.Columns)
	}
}

func TestOpenIsReadOnly(t *testing.T) {
	path := fixtureFile(t)
	db, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`DELETE FROM users`, `CREATE TABLE x (a)`, `PRAGMA query_only = 0; DELETE FROM users`} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s succeeded on a read-only open", q)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("users = %d, %v", n, err)
	}
}

func TestOpenMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	if _, err := Open(path, time.Second); err == nil {
		t.Fatal("opened a missing file")
	}
}

func TestOpenPathWithSpacesAndQuery(t *testing.T) {
	// The fixture is written at a plain path and moved, since the writer's DSN cannot carry '?'.
	dir := filepath.Join(t.TempDir(), "my data?x=1#y%20")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.db")
	if err := os.Rename(fixtureFile(t), path); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := Introspect(context.Background(), db.DB)
	if err != nil || len(m.Relations) != 6 {
		t.Fatalf("%v %v", m, err)
	}
}

// A WAL database needs a -shm file; when its folder is read-only Open falls back to immutable.
func TestOpenWALInReadOnlyFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("folder permissions work differently on Windows")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.db")
	w, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE TABLE t (a); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + side); err == nil {
			t.Fatalf("%s left behind by the writer; the test needs a clean close", side)
		}
	}

	db, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if db.Immutable {
		t.Error("immutable in a writable folder")
	}
	db.Close()

	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	db, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !db.Immutable {
		t.Error("not immutable in a read-only folder")
	}
	res, err := Run(context.Background(), db.DB, "SELECT a FROM t", 10, false)
	if err != nil || res.RowCount != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if tr := Test(context.Background(), path); !tr.OK || len(tr.Warnings) != 1 {
		t.Fatalf("test result %+v", tr)
	}
}

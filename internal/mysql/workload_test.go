package mysql

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// TestWorkload runs a known workload on MySQL 8.4 and on MariaDB 11.8 started
// with performance_schema, then reads it as root and as a user without
// SELECT on performance_schema; MariaDB's default (off) says how to enable it.
// One container at a time: they share a small Docker memory budget.
func TestWorkload(t *testing.T) {
	t.Run("mysql", func(t *testing.T) { testWorkload(t, "mysql") })
	t.Run("mariadb", func(t *testing.T) { testWorkload(t, "mariadb", "--performance-schema=ON") })
	t.Run("mariadb-off", func(t *testing.T) {
		p := mytest.Server(t, "mariadb")
		db, err := Open(p, mytest.Password, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ctx := context.Background()
		m, err := Introspect(ctx, db, nil, p.Database)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := Workload(ctx, db, m)
		if err != nil || snap.Statements.Status != workload.StatusNotLoaded || len(snap.Tables) != 0 ||
			!strings.Contains(snap.Statements.Message, "performance_schema=ON under [mysqld] in my.cnf") {
			t.Fatalf("off: %+v %v", snap, err)
		}
	})
}

const workloadDDL = `CREATE TABLE users (id INT PRIMARY KEY, name VARCHAR(50));
CREATE TABLE orders (id INT PRIMARY KEY, user_id INT NOT NULL, total INT NOT NULL, KEY orders_user (user_id));
CREATE TABLE notes (id INT PRIMARY KEY, body VARCHAR(50));
CREATE TABLE status (id INT PRIMARY KEY);
INSERT INTO users VALUES (1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e'),(6,'f'),(7,'g'),(8,'h'),(9,'i'),(10,'j');
INSERT INTO orders VALUES (1,2,10),(2,3,10),(3,4,10),(4,5,10),(5,6,10),(6,7,10),(7,8,10),(8,9,10),(9,10,10),(10,1,10),
  (11,2,10),(12,3,10),(13,4,10),(14,5,10),(15,6,10),(16,7,10),(17,8,10),(18,9,10),(19,10,10),(20,1,10);
INSERT INTO notes VALUES (1,'a'),(2,'b'),(3,'ab'),(4,'c'),(5,'d');
CREATE USER 'plain'@'%' IDENTIFIED BY 'plain-pass';
GRANT SELECT ON app.* TO 'plain'@'%'`

func testWorkload(t *testing.T, flavor string, args ...string) {
	p := mytest.Server(t, flavor, args...)
	ctx := context.Background()
	db, err := Open(p, mytest.Password, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		return n
	}
	for _, q := range strings.Split(workloadDDL, ";\n") {
		exec(q)
	}
	m, err := Introspect(ctx, db, nil, p.Database)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Workload(ctx, db, m)
	if err != nil || before.Statements.Status != workload.StatusOK {
		t.Fatalf("before: %+v %v", before, err)
	}

	// The application: a join four times and a full scan once, as text;
	// a lookup twice as a server-side prepared statement (database/sql
	// with arguments); four row writes; and catalog reads that must not count.
	const join = "SELECT o.id, u.name FROM orders o JOIN users u ON u.id = o.user_id WHERE o.user_id = 3"
	for range 4 {
		if n := count(join); n != 2 {
			t.Fatalf("join returned %d rows", n)
		}
	}
	if n := count("SELECT id FROM notes WHERE body LIKE '%a%'"); n != 2 {
		t.Fatalf("scan returned %d rows", n)
	}
	const prepared = "SELECT o.total FROM orders o WHERE o.user_id = ?"
	for range 2 {
		if n := count(prepared, 4); n != 2 {
			t.Fatalf("prepared returned %d rows", n)
		}
	}
	exec("UPDATE orders SET total = total + 1 WHERE id = 1")
	exec("INSERT INTO notes VALUES (6, 'f'), (7, 'g')")
	exec("DELETE FROM notes WHERE id = 6")
	count("SELECT id FROM status")
	count("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'app'")
	count("SHOW TABLES")

	snap, err := Workload(ctx, db, m)
	if err != nil || snap.Statements.Status != workload.StatusOK {
		t.Fatalf("after: %+v %v", snap, err)
	}
	logSnapshot(t, before, snap)

	// Table I/O counts rows: fetched by a full scan, through an index, written.
	for key, want := range map[string][3]int64{
		"app.orders": {0, 4*2 + 2*2 + 1, 1}, // the join's and lookup's rows by user_id, the update's by id
		"app.notes":  {5, 1, 3},             // every row by the scan, the delete's by id
	} {
		a, b := snap.Tables[key], before.Tables[key]
		if a == nil || b == nil {
			t.Fatalf("%s missing", key)
		}
		if got := [3]int64{a.SeqScans - b.SeqScans, a.IdxScans - b.IdxScans, a.RowsWritten - b.RowsWritten}; got != want {
			t.Errorf("%s delta (seq, idx, written) %v, want %v", key, got, want)
		}
	}
	// The seed inserts count as writes too.
	if a := snap.Tables["app.users"]; a == nil || a.RowsWritten != 10 || a.SeqScans != 0 || a.IdxScans < 4 || a.IdxScans > 8 {
		t.Errorf("users %+v", a)
	}

	// Calls of the statements using each table: seed insert, join, update;
	// seed insert, join; the two inserts (one digest), scan, delete. The
	// prepared lookup has no digest row on either server, so it shows in
	// orders' index reads above but not in its calls.
	for key, want := range map[string]int64{"app.orders": 1 + 4 + 1, "app.users": 1 + 4, "app.notes": 2 + 1 + 1, "app.status": 1} {
		if tb := snap.Tables[key]; tb.QueryCalls != want || tb.QueryTimeMs <= 0 {
			t.Errorf("%s calls %d, want %d", key, tb.QueryCalls, want)
		}
	}
	distinct := map[string]int64{}
	for key, tb := range snap.Tables {
		joined := false
		for _, q := range tb.TopQueries {
			distinct[q.QueryID] = q.Calls
			joined = joined || strings.Contains(q.Text, "JOIN `users`") && q.Calls == 4 && q.MeanMs > 0
			for _, bad := range []string{"information_schema", "performance_schema", "VERSION", "@@"} {
				if strings.Contains(q.Text, bad) {
					t.Errorf("%s: %q attributed", key, q.Text)
				}
			}
			for _, bad := range []string{"SHOW", "SET", "START", "ROLLBACK"} {
				if strings.HasPrefix(q.Text, bad) {
					t.Errorf("%s: %q attributed", key, q.Text)
				}
			}
		}
		if (key == "app.orders" || key == "app.users") && !joined {
			t.Errorf("%s: no join in %+v", key, tb.TopQueries)
		}
	}
	// Not tied to a table: four CREATE TABLE, CREATE USER, GRANT. Catalog,
	// session and dbscope's own statements are left out of the totals.
	var attributed int64
	for _, c := range distinct {
		attributed += c
	}
	st := snap.Statements
	if st.UnattributedCalls != 6 || attributed != 12 || st.TotalCalls != attributed+st.UnattributedCalls || st.UnreadCalls != 0 ||
		st.TotalTimeMs <= 0 || st.Since == nil || st.Since.After(snap.GeneratedAt) {
		t.Errorf("statements %+v, attributed %d", st, attributed)
	}

	// An admin stops digest collection: tables still read, statements say how to resume.
	exec("UPDATE performance_schema.setup_consumers SET ENABLED = 'NO' WHERE NAME = 'statements_digest'")
	off, err := Workload(ctx, db, m)
	exec("UPDATE performance_schema.setup_consumers SET ENABLED = 'YES' WHERE NAME = 'statements_digest'")
	if err != nil || off.Statements.Status != workload.StatusNotLoaded || off.Tables["app.orders"] == nil ||
		!strings.Contains(off.Statements.Message, "SET ENABLED = 'YES' WHERE NAME = 'statements_digest'") {
		t.Errorf("digests off: %+v %v", off, err)
	}

	plain := p
	plain.User = "plain"
	pdb, err := Open(plain, "plain-pass", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	denied, err := Workload(ctx, pdb, m)
	const grant = "This user may not read performance_schema: an admin runs GRANT SELECT ON performance_schema.* TO 'plain'@'%'."
	if err != nil || denied.Statements.Status != workload.StatusUnavailable || denied.Statements.Message != grant || len(denied.Tables) != 0 {
		t.Fatalf("plain: %+v %v", denied, err)
	}
	exec("GRANT SELECT ON performance_schema.* TO 'plain'@'%'")
	granted, err := Workload(ctx, pdb, m)
	if err != nil || granted.Statements.Status != workload.StatusOK || granted.Tables["app.notes"].QueryCalls != 4 {
		t.Fatalf("plain after the grant: %+v %v", granted, err)
	}
}

func logSnapshot(t *testing.T, before, snap *workload.Snapshot) {
	t.Helper()
	t.Logf("statements %+v since %v", snap.Statements, snap.Statements.Since)
	for k, tb := range snap.Tables {
		b := before.Tables[k]
		if b == nil {
			b = &workload.Table{}
		}
		t.Logf("%s seq %d (+%d) idx %d (+%d) written %d (+%d) calls %d", k, tb.SeqScans, tb.SeqScans-b.SeqScans,
			tb.IdxScans, tb.IdxScans-b.IdxScans, tb.RowsWritten, tb.RowsWritten-b.RowsWritten, tb.QueryCalls)
		for _, q := range tb.TopQueries {
			t.Logf("    %d x %q", q.Calls, q.Text)
		}
	}
}

func TestWorkloadTables(t *testing.T) {
	m := &schema.Model{Relations: []schema.Relation{{Schema: "app", Name: "orders"}, {Schema: "app", Name: "users"},
		{Schema: "app", Name: "status"}, {Schema: "app", Name: "total"}, {Schema: "crm", Name: "users"}}}
	rels := indexRelations(m, false)
	for _, c := range []struct {
		digest string
		want   []string
	}{
		{"SELECT `o` . `id` FROM `orders` `o` , `users` `u` WHERE `u` . `id` = `o` . `user_id`", []string{"app.orders", "app.users"}},
		{"SELECT `users` FROM `orders`", []string{"app.orders"}},      // a column named like a table
		{"SELECT * FROM `orders` AS `users`", []string{"app.orders"}}, // an alias named like one
		{"SELECT * FROM `crm` . `users` JOIN `users` USING ( `id` )", []string{"crm.users", "app.users"}},
		{"INSERT INTO `orders` ( `total` ) VALUES (...) ON DUPLICATE KEY UPDATE `total` = ?", []string{"app.orders"}},
		{"UPDATE IGNORE `orders` SET `total` = ?", []string{"app.orders"}},
		{"SELECT ID FROM STATUS WHERE ID = ? ", []string{"app.status"}}, // MariaDB prints keyword names bare
		{"SELECT * FROM ( SELECT * FROM `users` ) `orders`", []string{"app.users"}},
		{"SELECT * FROM `orders` FOR UPDATE", []string{"app.orders"}},
		{"SELECT * FROM `missing`", nil},
	} {
		toks := Tokens(c.digest)
		if k := kindOf(toks); k != dataStmt {
			t.Errorf("%s: kind %d", c.digest, k)
		}
		if got := tablesIn(toks, "app", rels); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.digest, got, c.want)
		}
	}
	for digest, want := range map[string]statementKind{
		"SELECT `TABLE_NAME` FROM `information_schema` . `TABLES` WHERE `TABLE_SCHEMA` = ?": catalogStmt,
		"SELECT `VERSION` ( )":              catalogStmt,
		"SELECT ?":                          catalogStmt,
		"START TRANSACTION READ ONLY":       catalogStmt,
		"SHOW TABLES":                       catalogStmt,
		"CREATE TABLE `t` ( `id` INTEGER )": otherStmt,
		"CALL `p` ( )":                      otherStmt,
		"( SELECT * FROM `orders` ) UNION ( SELECT * FROM `users` )": dataStmt,
	} {
		if got := kindOf(Tokens(digest)); got != want {
			t.Errorf("%s: kind %d, want %d", digest, got, want)
		}
	}
}

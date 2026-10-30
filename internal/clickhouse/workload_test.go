package clickhouse

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/workload"
)

// TestWorkload runs a known workload and reads it back from system.query_log,
// as admin and as a user who may not read it, on the current release and LTS.
func TestWorkload(t *testing.T) {
	for _, name := range []string{"26.8", "25.8"} {
		t.Run(name, func(t *testing.T) { testWorkload(t, name) })
	}
}

func testWorkload(t *testing.T, name string) {
	p := mytest.ClickHouse(t, name)
	ctx := context.Background()
	c, err := Connect(p, mytest.ClickHousePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exec := func(q string) {
		t.Helper()
		if err := c.Conn.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	exec("CREATE TABLE events (id UInt64, kind String) ENGINE = MergeTree ORDER BY id")
	exec("CREATE TABLE users (id UInt64) ENGINE = MergeTree ORDER BY id")
	exec("INSERT INTO events SELECT number, toString(number % 3) FROM numbers(500)")
	const hot = "SELECT kind, count() FROM events GROUP BY kind"
	for range 4 {
		exec(hot)
	}
	exec("SELECT count() FROM events JOIN users USING id")
	exec("SYSTEM FLUSH LOGS")

	m, err := Introspect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Workload(ctx, c, m)
	if err != nil || snap.Statements.Status != workload.StatusOK || snap.Statements.Since == nil {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	ev, us := snap.Tables["default.events"], snap.Tables["default.users"]
	// 4 hot selects, the join, the insert (CREATE TABLE has no tables in query_log).
	if ev == nil || ev.QueryCalls != 6 || ev.RowsWritten != 500 || len(ev.TopQueries) != 3 {
		t.Fatalf("events %+v", ev)
	}
	found := false
	for _, q := range ev.TopQueries {
		found = found || q.Text == hot && q.Calls == 4 && q.MeanMs >= 0
		if strings.Contains(q.Text, "system.") {
			t.Errorf("system query attributed: %s", q.Text)
		}
	}
	if !found {
		t.Errorf("hot query missing: %+v", ev.TopQueries)
	}
	if us == nil || us.QueryCalls != 1 || us.RowsWritten != 0 {
		t.Errorf("users %+v", us)
	}
	if snap.Statements.TotalCalls < 6 {
		t.Errorf("statements %+v", snap.Statements)
	}

	exec("CREATE USER plain IDENTIFIED BY 'plain-pass' SETTINGS readonly = 1")
	exec("GRANT SELECT ON default.* TO plain")
	plain := p
	plain.User = "plain"
	pc, err := Connect(plain, "plain-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	snap, err = Workload(ctx, pc, m)
	if err != nil || snap.Statements.Status != workload.StatusUnavailable || !strings.Contains(snap.Statements.Message, "GRANT SELECT ON system.query_log") {
		t.Errorf("plain %+v %v", snap.Statements, err)
	}
}

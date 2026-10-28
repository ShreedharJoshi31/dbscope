package clickhouse

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/mytest"
)

func healthCheck(t *testing.T, r *health.Report, id string) health.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %+v", id, r.Checks)
	return health.Check{}
}

// TestHealth piles up parts with single-row inserts, leaves a failing
// mutation, and reads health as admin and as a user without system grants.
func TestHealth(t *testing.T) {
	p := mytest.ClickHouse(t, "26.8")
	ctx := context.Background()
	c, err := Connect(p, mytest.ClickHousePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, q := range []string{
		"CREATE TABLE t (id UInt64, n UInt32) ENGINE = MergeTree ORDER BY id SETTINGS old_parts_lifetime = 1",
		"SYSTEM STOP MERGES t",
	} {
		if err := c.Conn.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	for i := 0; i < 350; i++ {
		if err := c.Conn.Exec(ctx, "INSERT INTO t VALUES (?, 0)", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Conn.Exec(ctx, "ALTER TABLE t UPDATE n = throwIf(1, 'dbscope test failure') WHERE 1"); err != nil {
		t.Fatal(err)
	}
	r, err := Health(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if ch := healthCheck(t, r, "ch.parts"); ch.Status != health.Warn || !strings.Contains(ch.Value, "default.t") {
		t.Errorf("parts %+v", ch)
	}
	if ch := healthCheck(t, r, "ch.mutations"); ch.Value != "1" || ch.Status == health.Unknown {
		t.Errorf("mutations %+v", ch)
	}
	if ch := healthCheck(t, r, "ch.replication"); ch.Status != health.OK || ch.Value != "no replicated tables" {
		t.Errorf("replication %+v", ch)
	}
	for _, id := range []string{"ch.disk", "ch.memory", "ch.detached_parts"} {
		if ch := healthCheck(t, r, id); ch.Status == health.Unknown {
			t.Errorf("%s %+v", id, ch)
		}
	}

	for _, q := range []string{"CREATE USER plain IDENTIFIED BY 'plain-pass' SETTINGS readonly = 1", "GRANT SELECT ON default.* TO plain"} {
		if err := c.Conn.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	plain := p
	plain.User = "plain"
	pc, err := Connect(plain, "plain-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	r, err = Health(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ch.parts", "ch.mutations", "ch.replication"} {
		if ch := healthCheck(t, r, id); ch.Status != health.Unknown || !strings.Contains(ch.Detail, "GRANT SELECT ON system.") {
			t.Errorf("plain %s %+v", id, ch)
		}
	}
}

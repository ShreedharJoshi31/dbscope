package health

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func check(t *testing.T, r *Report, id string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %+v", id, r.Checks)
	return Check{}
}

func TestLevelAndWords(t *testing.T) {
	if Level(5, 5, 10) != Warn || Level(10, 5, 10) != Danger || Level(4.9, 5, 10) != OK {
		t.Error("levels")
	}
	if Duration(3*time.Hour+4*time.Minute) != "3 h 4 min" || Duration(50*time.Hour) != "2 d 2 h" || Bytes(3<<20) != "3.0 MiB" {
		t.Error("words")
	}
}

// TestPostgres builds each condition on real servers and checks that its
// check sees it, and that a role without pg_read_all_stats is told so.
func TestPostgres(t *testing.T) {
	defer func(d time.Duration) { idleAfter = d }(idleAfter)
	idleAfter = 0
	for _, srv := range pgtest.Servers(t, "14", "17", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			ctx := context.Background()
			d := srv.NewDB(t)
			d.Exec(t, `CREATE TABLE t (x int); INSERT INTO t VALUES (1), (1);
				CREATE TABLE churn (id int); INSERT INTO churn SELECT generate_series(1, 50000); DELETE FROM churn WHERE id > 10000`)
			if _, err := d.Admin.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY t_x ON t (x)"); err == nil {
				t.Fatal("the unique index on duplicates was built")
			}
			slot := d.Role("dbscope_slot")
			if _, err := d.Admin.Exec(ctx, "SELECT pg_create_physical_replication_slot($1, true)", slot); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Admin.Exec(context.Background(), "SELECT pg_drop_replication_slot($1)", slot) })

			su, pw := d.Superuser(t)
			holder, waiter := d.Connect(t, su.User, pw), d.Connect(t, su.User, pw)
			if _, err := holder.Exec(ctx, "BEGIN; LOCK TABLE t"); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { waiter.Exec(ctx, "SELECT * FROM t"); close(done) }()
			time.Sleep(time.Second) // the waiter queues on the lock, and table statistics flush
			r, err := Postgres(ctx, d.Admin)
			holder.Exec(ctx, "ROLLBACK")
			<-done
			if err != nil {
				t.Fatal(err)
			}
			if c := check(t, r, "pg.blocked_sessions"); c.Status != Warn || c.Value != "1" {
				t.Errorf("blocked %+v", c)
			}
			if c := check(t, r, "pg.oldest_transaction"); c.Value == "none" || !strings.Contains(c.Value, "idle in transaction") && !strings.Contains(c.Value, "active") {
				t.Errorf("oldest transaction %+v", c)
			}
			if c := check(t, r, "pg.idle_in_transaction"); c.Status == OK {
				t.Errorf("idle in transaction %+v", c)
			}
			if c := check(t, r, "pg.invalid_indexes"); c.Status != Warn || !strings.Contains(c.Detail, "public.t_x") {
				t.Errorf("invalid index %+v", c)
			}
			if c := check(t, r, "pg.inactive_slots"); c.Status != Warn || !strings.Contains(c.Detail, slot) {
				t.Errorf("slots %+v", c)
			}
			if c := check(t, r, "pg.dead_tuples"); c.Status != Warn || !strings.Contains(c.Detail, "public.churn") {
				t.Errorf("dead tuples %+v", c)
			}
			for _, id := range []string{"pg.connections", "pg.xid_age", "pg.cache_hit"} {
				if c := check(t, r, id); c.Status != OK {
					t.Errorf("%s %+v", id, c)
				}
			}
			if c := check(t, r, "pg.replication_lag"); c.Status != OK || c.Value != "no standbys connected" {
				t.Errorf("replication %+v", c)
			}

			plain := d.Role("health_plain")
			d.Exec(t, "CREATE ROLE "+plain+" LOGIN PASSWORD 'plain-pass'")
			pc := d.Connect(t, plain, "plain-pass")
			r, err = Postgres(ctx, pc)
			if err != nil {
				t.Fatal(err)
			}
			if c := check(t, r, "pg.replication_lag"); c.Status != Unknown || !strings.Contains(c.Detail, "pg_read_all_stats") {
				t.Errorf("plain role replication %+v", c)
			}
			if c := check(t, r, "pg.oldest_transaction"); c.Status != OK || c.Value != "none" {
				t.Errorf("no other transaction open: %+v", c)
			}
			if c := check(t, r, "pg.connections"); !strings.Contains(c.Detail, "own sessions") {
				t.Errorf("plain role connections %+v", c)
			}
		})
	}
}

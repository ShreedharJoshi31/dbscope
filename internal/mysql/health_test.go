package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

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

// TestHealth holds a row lock with one session, makes another wait on it,
// and checks what root and a user without PROCESS see.
func TestHealth(t *testing.T) {
	for _, flavor := range []string{"mysql", "mariadb"} {
		t.Run(flavor, func(t *testing.T) {
			p := mytest.Server(t, flavor)
			ctx := context.Background()
			db, err := Open(p, mytest.Password, 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, q := range []string{"CREATE TABLE t (id int PRIMARY KEY, n int)", "INSERT INTO t VALUES (1, 0)",
				"CREATE USER 'plain'@'%' IDENTIFIED BY 'plain-pass'", "GRANT SELECT ON app.* TO 'plain'@'%'"} {
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			holder, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			holder.ExecContext(ctx, "BEGIN")
			if _, err := holder.ExecContext(ctx, "SELECT * FROM t WHERE id = 1 FOR UPDATE"); err != nil {
				t.Fatal(err)
			}
			waiter, _ := db.Conn(ctx)
			defer waiter.Close()
			done := make(chan struct{})
			go func() { waiter.ExecContext(ctx, "UPDATE t SET n = 1 WHERE id = 1"); close(done) }()
			time.Sleep(time.Second)

			r, err := Health(ctx, db)
			holder.ExecContext(ctx, "ROLLBACK")
			<-done
			if err != nil {
				t.Fatal(err)
			}
			if c := healthCheck(t, r, "mysql.oldest_transaction"); c.Value == "none" || c.Status == health.Unknown {
				t.Errorf("oldest %+v", c)
			}
			if c := healthCheck(t, r, "mysql.lock_waits"); c.Status != health.Warn || c.Value != "1" {
				t.Errorf("lock waits %+v", c)
			}
			for _, id := range []string{"mysql.connections", "mysql.history_length", "mysql.buffer_pool_hit"} {
				if c := healthCheck(t, r, id); c.Status != health.OK {
					t.Errorf("%s %+v", id, c)
				}
			}
			if c := healthCheck(t, r, "mysql.replication"); c.Status != health.OK || c.Value != "not a replica" {
				t.Errorf("replication %+v", c)
			}

			plain := p
			plain.User = "plain"
			pdb, err := Open(plain, "plain-pass", 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer pdb.Close()
			r, err = Health(ctx, pdb)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"mysql.oldest_transaction", "mysql.history_length"} {
				if c := healthCheck(t, r, id); c.Status != health.Unknown || !strings.Contains(c.Detail, "PROCESS") {
					t.Errorf("plain %s %+v", id, c)
				}
			}
			if c := healthCheck(t, r, "mysql.replication"); c.Status != health.Unknown || !strings.Contains(c.Detail, "GRANT") {
				t.Errorf("plain replication %+v", c)
			}
		})
	}
}

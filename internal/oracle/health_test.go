package oracle

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
// leaves a view invalid, and reads health as SYSTEM and as a plain user.
func TestHealth(t *testing.T) {
	p := mytest.Oracle(t)
	ctx := context.Background()
	db, err := Open(p, mytest.OraclePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE USER shop IDENTIFIED BY ShopTest2026x DEFAULT TABLESPACE sysaux QUOTA UNLIMITED ON sysaux",
		"CREATE TABLE shop.t (id NUMBER PRIMARY KEY, n NUMBER, extra NUMBER)",
		"INSERT INTO shop.t VALUES (1, 0, 0)",
		"COMMIT",
		"CREATE VIEW shop.v AS SELECT id, extra FROM shop.t",
		"ALTER TABLE shop.t DROP COLUMN extra",
		"CREATE USER ro IDENTIFIED BY " + roPass,
		"GRANT CREATE SESSION TO ro",
		"GRANT READ ON shop.t TO ro",
	} {
		mustExec(t, db, q)
	}
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE shop.t SET n = 1 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	waiter, _ := db.Conn(ctx)
	defer waiter.Close()
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { waiter.ExecContext(wctx, "UPDATE shop.t SET n = 2 WHERE id = 1"); close(done) }()
	time.Sleep(2 * time.Second)

	r, err := Health(ctx, db)
	tx.Rollback()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if c := healthCheck(t, r, "ora.blocked_sessions"); c.Status != health.Warn || c.Value != "1" {
		t.Errorf("blocked %+v", c)
	}
	if c := healthCheck(t, r, "ora.oldest_transaction"); c.Value == "none" || c.Status == health.Unknown {
		t.Errorf("oldest %+v", c)
	}
	if c := healthCheck(t, r, "ora.invalid_objects"); c.Status != health.Warn || !strings.Contains(c.Detail, "SHOP.V (view)") {
		t.Errorf("invalid %+v", c)
	}
	if c := healthCheck(t, r, "ora.data_guard"); c.Status != health.OK || c.Value != "primary" {
		t.Errorf("data guard %+v", c)
	}
	for _, id := range []string{"ora.sessions", "ora.tablespaces", "ora.recovery_area"} {
		if c := healthCheck(t, r, id); c.Status == health.Unknown || c.Status == health.Danger {
			t.Errorf("%s %+v", id, c)
		}
	}

	ro := p
	ro.User = "ro"
	rodb, err := Open(ro, roPass)
	if err != nil {
		t.Fatal(err)
	}
	defer rodb.Close()
	r, err = Health(ctx, rodb)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ora.sessions", "ora.blocked_sessions", "ora.oldest_transaction", "ora.tablespaces"} {
		if c := healthCheck(t, r, id); c.Status != health.Unknown || !strings.Contains(c.Detail, "SELECT_CATALOG_ROLE") {
			t.Errorf("plain %s %+v", id, c)
		}
	}
}

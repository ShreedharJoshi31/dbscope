package mssql

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

// TestHealth blocks one session behind another's update and reads health
// as sa and as a db_datareader login.
func TestHealth(t *testing.T) {
	p := mytest.MSSQL(t, "2022")
	ctx := context.Background()
	admin, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	mustExec(t, admin, "CREATE DATABASE shop")
	p.Database = "shop"
	db, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"CREATE TABLE dbo.t (id int PRIMARY KEY, n int)", "INSERT dbo.t VALUES (1, 0)",
		"CREATE LOGIN ro WITH PASSWORD = '" + roPass + "', CHECK_POLICY = OFF", "CREATE USER ro FOR LOGIN ro", "ALTER ROLE db_datareader ADD MEMBER ro"} {
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
	if _, err := tx.ExecContext(ctx, "UPDATE dbo.t SET n = 1 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	waiter, _ := db.Conn(ctx)
	defer waiter.Close()
	done := make(chan struct{})
	go func() { waiter.ExecContext(ctx, "SELECT n FROM dbo.t WHERE id = 1"); close(done) }()
	time.Sleep(2 * time.Second)

	r, err := Health(ctx, db)
	tx.Rollback()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if c := healthCheck(t, r, "mssql.blocked_sessions"); c.Status != health.Warn || c.Value != "1" {
		t.Errorf("blocked %+v", c)
	}
	if c := healthCheck(t, r, "mssql.oldest_transaction"); c.Value == "none" || c.Status == health.Unknown {
		t.Errorf("oldest %+v", c)
	}
	if c := healthCheck(t, r, "mssql.last_backup"); c.Status != health.Warn || c.Value != "never" {
		t.Errorf("backup %+v", c)
	}
	if c := healthCheck(t, r, "mssql.availability"); c.Status != health.OK || c.Value != "not in an availability group" {
		t.Errorf("availability %+v", c)
	}
	for _, id := range []string{"mssql.log_space", "mssql.page_life"} {
		if c := healthCheck(t, r, id); c.Status != health.OK {
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
	for _, id := range []string{"mssql.blocked_sessions", "mssql.oldest_transaction", "mssql.page_life"} {
		if c := healthCheck(t, r, id); c.Status != health.Unknown || !strings.Contains(c.Detail, "VIEW SERVER") {
			t.Errorf("reader %s %+v", id, c)
		}
	}
}

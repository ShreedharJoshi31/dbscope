package migrate

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/pgtest"
)

func blockedFinding(s Statement) *Finding {
	for i, f := range s.Findings {
		if f.RuleID == BlockedNow {
			return &s.Findings[i]
		}
	}
	return nil
}

// TestBlockersPostgres opens transactions that hold or wait for locks, runs
// the analysis and checks the reported blockers against what the server
// does: the DDL really times out on lock_timeout exactly when dbscope says
// it would wait.
func TestBlockersPostgres(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	d := srv.NewDB(t)
	d.Exec(t, `CREATE TABLE films (id int PRIMARY KEY, title text); INSERT INTO films VALUES (1, 'a');
		CREATE TABLE other (id int PRIMARY KEY)`)
	p, pw := d.Superuser(t)
	ctx := context.Background()
	holder := d.Connect(t, p.User, pw)
	var holderPID int32
	if err := holder.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	probe := d.Connect(t, p.User, pw)

	// waits reports whether sql times out on its lock, rolling back either way.
	waits := func(sql string) bool {
		t.Helper()
		tx, err := probe.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '1s'"); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, sql)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "55P03" {
			return true
		}
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return false
	}
	analyze := func(db interface {
		BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	}, sql string) *Report {
		t.Helper()
		base, err := LiveBase(ctx, db, []string{"public"}, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := Analyze(ctx, base, Request{Files: []ingest.Input{{Name: "m.sql", SQL: sql}}})
		if err != nil {
			t.Fatal(err)
		}
		if rep.BlockersAt == nil {
			t.Fatalf("BlockersAt not set; warnings %v", rep.Warnings)
		}
		return rep
	}
	const alter = "ALTER TABLE films ADD COLUMN x int"
	const index = "CREATE INDEX films_title ON films (title)"

	t.Run("no open transaction", func(t *testing.T) {
		rep := analyze(d.Admin, alter)
		if len(rep.Blockers) != 0 || blockedFinding(rep.Statements[0]) != nil || waits(alter) {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
	})

	t.Run("transaction on another table", func(t *testing.T) {
		tx, err := holder.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT * FROM other"); err != nil {
			t.Fatal(err)
		}
		rep := analyze(d.Admin, alter)
		if len(rep.Blockers) != 0 || blockedFinding(rep.Statements[0]) != nil || waits(alter) {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
	})

	t.Run("idle in transaction after a read", func(t *testing.T) {
		tx, err := holder.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT * FROM films"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		rep := analyze(d.Admin, alter+";\n"+index)
		if len(rep.Blockers) != 1 {
			t.Fatalf("blockers %+v, warnings %v", rep.Blockers, rep.Warnings)
		}
		b := rep.Blockers[0]
		if b.Relation != "public.films" || b.Session != itoa(holderPID) || b.State != "idle in transaction" || b.Lock != "ACCESS SHARE" ||
			b.AgeSeconds < 1 || b.Query != "SELECT * FROM films" || b.User != p.User {
			t.Errorf("blocker %+v", b)
		}
		f := blockedFinding(rep.Statements[0])
		if f == nil || f.Rating != "high" || !strings.Contains(f.Message, "pid "+itoa(holderPID)) || !strings.Contains(f.Message, "idle in transaction") ||
			!strings.Contains(f.Message, "holding ACCESS SHARE on public.films") || rep.Statements[0].Rating != "high" {
			t.Errorf("finding %+v", f)
		}
		// CREATE INDEX takes SHARE, which ACCESS SHARE does not conflict with.
		if f := blockedFinding(rep.Statements[1]); f != nil {
			t.Errorf("index finding %+v", f)
		}
		if !waits(alter) || waits(index) {
			t.Error("the server disagrees")
		}
		t.Logf("observed: %+v", b)
	})

	t.Run("session already waiting", func(t *testing.T) {
		tx, err := holder.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT * FROM films"); err != nil {
			t.Fatal(err)
		}
		waiter := d.Connect(t, p.User, pw)
		var waiterPID int32
		if err := waiter.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&waiterPID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := waiter.Exec(ctx, "SET lock_timeout = '20s'; BEGIN; LOCK TABLE films IN ACCESS EXCLUSIVE MODE; ROLLBACK")
			done <- err
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var n int
			if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE pid = $1 AND NOT granted", waiterPID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the waiter never queued")
			}
			time.Sleep(50 * time.Millisecond)
		}
		rep := analyze(d.Admin, index)
		var got *Blocker
		for i, b := range rep.Blockers {
			if b.Session == itoa(waiterPID) {
				got = &rep.Blockers[i]
			}
			if b.Session == itoa(holderPID) {
				t.Errorf("ACCESS SHARE does not block SHARE: %+v", b)
			}
		}
		f := blockedFinding(rep.Statements[0])
		if got == nil || got.Lock != "ACCESS EXCLUSIVE (waiting)" || !strings.Contains(got.State, "waiting for a lock") ||
			f == nil || !strings.Contains(f.Message, "already waiting for ACCESS EXCLUSIVE on public.films") {
			t.Fatalf("blockers %+v, finding %+v", rep.Blockers, f)
		}
		if !waits(index) {
			t.Error("the server lets CREATE INDEX past the queued ACCESS EXCLUSIVE")
		}
		t.Logf("observed: %+v", *got)
		tx.Rollback(ctx)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("role without pg_read_all_stats", func(t *testing.T) {
		role := d.Role("plain")
		d.Exec(t, "CREATE ROLE "+role+" LOGIN PASSWORD 'plain-pass'")
		tx, err := holder.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT * FROM films"); err != nil {
			t.Fatal(err)
		}
		c := d.Connect(t, role, "plain-pass")
		rep := analyze(c, alter)
		if len(rep.Blockers) != 1 || rep.Blockers[0].Session != itoa(holderPID) || rep.Blockers[0].State != "" || blockedFinding(rep.Statements[0]) == nil {
			t.Fatalf("blockers %+v", rep.Blockers)
		}
		if !strings.Contains(strings.Join(rep.Warnings, "\n"), "pg_read_all_stats") {
			t.Errorf("warnings %v", rep.Warnings)
		}
		// Hidden is unknown: no "<insufficient privilege>" query and no age of 0 s.
		if b := rep.Blockers[0]; b.Query != "" || b.AgeSeconds != -1 || !strings.Contains(blockedFinding(rep.Statements[0]).Message, "for an unknown time") {
			t.Errorf("hidden blocker %+v: %s", b, blockedFinding(rep.Statements[0]).Message)
		}
	})
}

func itoa(n int32) string { return strconv.Itoa(int(n)) }

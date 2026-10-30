package neo4j

import (
	"context"
	"strings"
	"testing"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

func blockedFinding(s migrate.Statement) *migrate.Finding {
	for i, f := range s.Findings {
		if f.RuleID == migrate.BlockedNow {
			return &s.Findings[i]
		}
	}
	return nil
}

// TestBlockersNeo4j keeps transactions open on one label, analyzes schema
// commands and a large write, and checks the reported blockers against
// SHOW TRANSACTIONS' own "Blocked by" for the same commands.
func TestBlockersNeo4j(t *testing.T) {
	p := mytest.Neo4j(t, "2026")
	ctx := context.Background()
	c, err := Connect(p, mytest.Neo4jPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	run := func(q string, timeout time.Duration) error {
		s := c.Driver.NewSession(ctx, sessionConfig(c.Database))
		defer s.Close(ctx)
		r, err := s.Run(ctx, q, nil, drv.WithTxTimeout(timeout))
		if err == nil {
			_, err = r.Consume(ctx)
		}
		return err
	}
	if err := run("UNWIND range(1, 10000) AS i CREATE (:User {id: i})", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := run("UNWIND range(1, 10) AS i CREATE (:Other {id: i})", time.Minute); err != nil {
		t.Fatal(err)
	}
	const script = "CREATE INDEX user_id FOR (u:User) ON (u.id);\nMATCH (u:User) SET u.flag = true;\nCREATE INDEX other_id FOR (o:Other) ON (o.id)"
	analyze := func() *migrate.Report {
		t.Helper()
		base, err := ReadBase(ctx, c, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		rep := Analyze(ctx, base, []ingest.Input{{Name: "m.cypher", SQL: script}})
		if rep.BlockersAt == nil {
			t.Fatalf("BlockersAt not set; warnings %v", rep.Warnings)
		}
		return rep
	}
	flagged := func(rep *migrate.Report) []bool {
		out := make([]bool, len(rep.Statements))
		for i, s := range rep.Statements {
			out[i] = blockedFinding(s) != nil
		}
		return out
	}
	// transactions lists SHOW TRANSACTIONS rows as "query | status".
	transactions := func() []string {
		t.Helper()
		recs, _, err := c.read(ctx, 10*time.Second, "SHOW TRANSACTIONS YIELD currentQuery, status RETURN coalesce(currentQuery, '') + ' | ' + status", nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range recs {
			out = append(out, r.Values[0].(string))
		}
		return out
	}
	until := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: %v", what, transactions())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	has := func(prefix, details string) func() bool {
		return func() bool {
			for _, r := range transactions() {
				if strings.HasPrefix(r, prefix) && strings.Contains(r, details) {
					return true
				}
			}
			return false
		}
	}
	// longWrite locks every node of the label, then keeps running; the server ends it after 12 s.
	longWrite := func(label string) chan error {
		done := make(chan error, 1)
		go func() {
			done <- run("MATCH (n:"+label+") SET n.z = 1 WITH count(*) AS n UNWIND range(1, 2000000000) AS i RETURN count(i)", 12*time.Second)
		}()
		// It shows in SHOW TRANSACTIONS before its SET has taken any lock.
		until("the long write to hold locks", func() bool {
			n, err := c.count(ctx, "SHOW TRANSACTIONS YIELD currentQuery, activeLockCount WHERE currentQuery STARTS WITH 'MATCH (n:"+label+")' AND activeLockCount > 0 RETURN count(*)")
			return err == nil && n > 0
		})
		return done
	}

	t.Run("no open transaction", func(t *testing.T) {
		rep := analyze()
		if len(rep.Blockers) != 0 || slicesTrue(flagged(rep)) || len(rep.Warnings) != 0 {
			t.Fatalf("blockers %+v, warnings %v", rep.Blockers, rep.Warnings)
		}
	})

	t.Run("running write on another label", func(t *testing.T) {
		done := longWrite("Other")
		rep := analyze()
		// The User index builds at once while Other is locked.
		if err := run("CREATE INDEX user_id FOR (u:User) ON (u.id)", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := run("DROP INDEX user_id", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		<-done
		f := flagged(rep)
		if f[0] || f[1] || !f[2] || len(rep.Blockers) != 1 || rep.Blockers[0].Relation != "Other" {
			t.Fatalf("flagged %v, blockers %+v", f, rep.Blockers)
		}
		t.Logf("observed: %+v", rep.Blockers[0])
	})

	t.Run("running write on the label", func(t *testing.T) {
		done := longWrite("User")
		rep := analyze()
		built := make(chan error, 1)
		go func() { built <- run("CREATE INDEX user_id FOR (u:User) ON (u.id)", time.Minute) }()
		until("the index to queue", has("CREATE INDEX user_id", "Blocked by"))
		<-done
		if err := <-built; err != nil {
			t.Fatal(err)
		}
		if err := run("DROP INDEX user_id", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		f := flagged(rep)
		if !f[0] || !f[1] || f[2] || len(rep.Blockers) != 1 {
			t.Fatalf("flagged %v, blockers %+v", f, rep.Blockers)
		}
		b := rep.Blockers[0]
		if b.Relation != "User" || !strings.HasPrefix(b.Session, "neo4j-transaction-") || b.User != "neo4j" || !strings.HasPrefix(b.Query, "MATCH (n:User)") ||
			b.Lock == "" || strings.HasSuffix(b.Lock, "(waiting)") || b.State != "running" {
			t.Errorf("blocker %+v", b)
		}
		msg := blockedFinding(rep.Statements[0]).Message
		if !strings.Contains(msg, "transaction "+b.Session+" (neo4j), running for ") || !strings.Contains(msg, "on User") || !strings.Contains(msg, "naming the same label") {
			t.Errorf("finding %s", msg)
		}
		t.Logf("observed: %+v", b)
	})

	t.Run("idle transaction and a schema command queued behind it", func(t *testing.T) {
		s := c.Driver.NewSession(ctx, sessionConfig(c.Database))
		defer s.Close(ctx)
		tx, err := s.BeginTransaction(ctx)
		if err != nil {
			t.Fatal(err)
		}
		r, err := tx.Run(ctx, "MATCH (u:User) WHERE u.id = 1 SET u.x = 1", nil)
		if err == nil {
			_, err = r.Consume(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		rep := analyze()
		if len(rep.Blockers) != 0 || slicesTrue(flagged(rep)) || !strings.Contains(strings.Join(rep.Warnings, "\n"), "hold locks but run no query") {
			t.Fatalf("blockers %+v, warnings %v", rep.Blockers, rep.Warnings)
		}
		t.Logf("observed warning: %s", rep.Warnings[0])
		built := make(chan error, 1)
		go func() { built <- run("CREATE CONSTRAINT user_key FOR (u:User) REQUIRE u.id IS UNIQUE", time.Minute) }()
		until("the constraint to queue", has("CREATE CONSTRAINT user_key", "Blocked by"))
		rep = analyze()
		tx.Rollback(ctx)
		if err := <-built; err != nil {
			t.Fatal(err)
		}
		if err := run("DROP CONSTRAINT user_key", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		f := flagged(rep)
		if !f[0] || !f[1] || f[2] || len(rep.Blockers) != 1 || !strings.HasSuffix(rep.Blockers[0].Lock, "(waiting)") ||
			!strings.HasPrefix(rep.Blockers[0].State, "blocked by: [neo4j-transaction-") {
			t.Fatalf("flagged %v, blockers %+v", f, rep.Blockers)
		}
		t.Logf("observed: %+v", rep.Blockers[0])
	})
}

func slicesTrue(bs []bool) bool {
	for _, b := range bs {
		if b {
			return true
		}
	}
	return false
}

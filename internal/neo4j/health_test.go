package neo4j

import (
	"context"
	"strings"
	"testing"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"

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

// TestHealth keeps a write transaction open past the threshold and reads health.
func TestHealth(t *testing.T) {
	defer func(d time.Duration) { slowTxAfter = d }(slowTxAfter)
	slowTxAfter = time.Second
	for _, name := range []string{"2026", "enterprise"} {
		t.Run(name, func(t *testing.T) { testHealth(t, name) })
	}
}

func testHealth(t *testing.T, name string) {
	p := mytest.Neo4j(t, name)
	ctx := context.Background()
	c, err := Connect(p, mytest.Neo4jPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := c.Driver.NewSession(ctx, drv.SessionConfig{DatabaseName: c.Database})
	defer s.Close(ctx)
	tx, err := s.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Run(ctx, "CREATE (:Probe {at: timestamp()})", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	r, err := Health(ctx, c)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ch := healthCheck(t, r, "neo4j.long_transactions"); ch.Status != health.Warn || !strings.Contains(ch.Detail, "neo4j on neo4j") {
		t.Errorf("long transactions %+v", ch)
	}
	if ch := healthCheck(t, r, "neo4j.servers"); name == "enterprise" && ch.Value != "1 available" {
		t.Errorf("servers %+v", ch)
	}
	if ch := healthCheck(t, r, "neo4j.databases"); ch.Status != health.OK {
		t.Errorf("databases %+v", ch)
	}
	for _, id := range []string{"neo4j.servers", "neo4j.indexes"} {
		if ch := healthCheck(t, r, id); ch.Status != health.OK {
			t.Errorf("%s %+v", id, ch)
		}
	}
}

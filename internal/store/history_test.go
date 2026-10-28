package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
)

func TestHistory(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p, err := s.CreateProfile(ctx, profile("a"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateProfile(ctx, profile("b"))
	base := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for i, st := range []string{HistoryOK, HistoryError, HistoryRejected, HistoryCancelled} {
		e := HistoryEntry{SQL: "SELECT " + st, StartedAt: base.Add(time.Duration(i) * 10 * time.Millisecond), DurationMs: 1.5, Status: st}
		if st == HistoryOK {
			e.RowCount = 3
		} else {
			e.ErrorCode = "code_" + st
		}
		if _, err := s.AddHistory(ctx, p.ID, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddHistory(ctx, other.ID, HistoryEntry{SQL: "x", StartedAt: base, Status: HistoryOK}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListHistory(ctx, p.ID, 50)
	if err != nil || len(got) != 4 {
		t.Fatalf("list %v %v", got, err)
	}
	if got[0].Status != HistoryCancelled || got[3].Status != HistoryOK || got[3].RowCount != 3 || got[3].ErrorCode != "" ||
		got[0].RowCount != 0 || got[0].ErrorCode != "code_cancelled" || !got[3].StartedAt.Equal(base) || got[3].DurationMs != 1.5 {
		t.Fatalf("entries %+v", got)
	}
	if got, _ := s.ListHistory(ctx, p.ID, 2); len(got) != 2 || got[0].Status != HistoryCancelled {
		t.Fatalf("limit 2: %+v", got)
	}
	if got, _ := s.ListHistory(ctx, p.ID, 0); len(got) != 1 {
		t.Fatalf("limit 0 clamps to 1: %d", len(got))
	}
	if _, err := s.AddHistory(ctx, "missing", HistoryEntry{SQL: "x", StartedAt: base, Status: HistoryOK}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown profile: %v", err)
	}
	if _, err := s.ListHistory(ctx, "missing", 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown profile list: %v", err)
	}
	if _, err := s.AddHistory(ctx, p.ID, HistoryEntry{SQL: "x", StartedAt: base, Status: "weird"}); err == nil {
		t.Fatal("bad status accepted")
	}
	// History is deleted with the profile.
	if err := s.DeleteProfile(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM query_history WHERE profile_id = ?`, p.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("history after delete: %d %v", n, err)
	}
}

func TestMaxCostRoundTrip(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	in := profile("a")
	p, err := s.CreateProfile(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProfile(ctx, p.ID); got.MaxCost != nil {
		t.Fatalf("unset maxCost read back as %v", *got.MaxCost)
	}
	for _, c := range []float64{0, 2.5e6} {
		p.MaxCost = &c
		if _, err := s.UpdateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetProfile(ctx, p.ID); got.MaxCost == nil || *got.MaxCost != c {
			t.Fatalf("maxCost %v read back as %v", c, got.MaxCost)
		}
	}
}

// TestMigration0003OnExistingData applies 0003 to a database that already
// holds a profile, as an upgrade from Phase 1 does.
func TestMigration0003OnExistingData(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	p, err := s.CreateProfile(ctx, profile("a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE schema_snapshots`, `DROP TABLE llm_settings`, `DROP TABLE glossary`, `DROP TABLE query_history`,
		`ALTER TABLE profiles DROP COLUMN max_cost`, `ALTER TABLE profiles DROP COLUMN engine`, `ALTER TABLE profiles DROP COLUMN path`, `ALTER TABLE profiles DROP COLUMN options`,
		`DELETE FROM schema_migrations WHERE version >= 3`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.GetProfile(ctx, p.ID)
	if err != nil || got.MaxCost != nil || got.Name != "a" || got.Engine != conn.EnginePostgres || !got.Capabilities.Migrations {
		t.Fatalf("after upgrade: %+v %v", got, err)
	}
	if _, err := s2.AddHistory(ctx, p.ID, HistoryEntry{SQL: "SELECT 1", StartedAt: time.Now(), Status: HistoryOK}); err != nil {
		t.Fatal(err)
	}
}

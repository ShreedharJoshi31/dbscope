package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
)

func TestMigrationsRecorded(t *testing.T) {
	s, _ := openTest(t)
	var versions []int
	rows, err := s.db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		rows.Scan(&v)
		versions = append(versions, v)
	}
	rows.Close()
	if !slices.Equal(versions, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("migrations %v", versions)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
}

func TestInferredFKDecisions(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p, _ := s.CreateProfile(ctx, profile("a"))
	other, _ := s.CreateProfile(ctx, profile("b"))
	const k1, k2 = "public.posts(user_id)->public.users(id)", "public.posts(team_id)->public.teams(id)"

	got, err := s.InferredFKDecisions(ctx, p.ID)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty decisions: %v %v", got, err)
	}
	for _, d := range []struct{ key, status string }{
		{k1, DecisionAccepted}, {k2, DecisionAccepted}, {k1, DecisionRejected}, {k2, DecisionUnset}, {"never.set(x)->a.b(c)", DecisionUnset},
	} {
		if err := s.SetInferredFKDecision(ctx, p.ID, d.key, d.status); err != nil {
			t.Fatalf("set %s %s: %v", d.key, d.status, err)
		}
	}
	s.SetInferredFKDecision(ctx, other.ID, k2, DecisionAccepted)
	got, _ = s.InferredFKDecisions(ctx, p.ID)
	if !maps.Equal(got, map[string]string{k1: DecisionRejected}) {
		t.Fatalf("decisions %v", got)
	}
	if err := s.SetInferredFKDecision(ctx, p.ID, k1, "maybe"); err == nil {
		t.Fatal("check constraint did not reject an unknown status")
	}
	for _, st := range []string{DecisionAccepted, DecisionUnset} {
		if err := s.SetInferredFKDecision(ctx, "missing", k1, st); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing profile %s: %v", st, err)
		}
	}

	if err := s.DeleteProfile(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM inferred_fk_decisions WHERE profile_id = ?`, p.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d decisions survived profile delete", n)
	}
	if got, _ := s.InferredFKDecisions(ctx, other.ID); len(got) != 1 {
		t.Fatalf("other profile decisions %v", got)
	}
}

func TestLayouts(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p, _ := s.CreateProfile(ctx, profile("a"))

	if _, err := s.GetLayout(ctx, p.ID, "public"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing layout: %v", err)
	}
	l := Layout{Key: "public", Positions: map[string]Position{"public.users": {X: 1.5, Y: -20}}, Collapsed: []string{"public.audit"}}
	if err := s.PutLayout(ctx, p.ID, l); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetLayout(ctx, p.ID, "public")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "public" || !maps.Equal(got.Positions, l.Positions) || !slices.Equal(got.Collapsed, l.Collapsed) || got.UpdatedAt.IsZero() {
		t.Fatalf("round trip %+v", got)
	}
	// Replacing with empty values keeps them empty, not null.
	if err := s.PutLayout(ctx, p.ID, Layout{Key: "public"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetLayout(ctx, p.ID, "public")
	if got.Positions == nil || len(got.Positions) != 0 || got.Collapsed == nil || len(got.Collapsed) != 0 {
		t.Fatalf("replaced layout %+v", got)
	}
	if _, err := s.GetLayout(ctx, p.ID, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other key: %v", err)
	}
	if err := s.PutLayout(ctx, "missing", l); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing profile: %v", err)
	}

	s.DeleteProfile(ctx, p.ID)
	var n int
	s.db.QueryRow(`SELECT count(*) FROM layouts`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d layouts survived profile delete", n)
	}
}

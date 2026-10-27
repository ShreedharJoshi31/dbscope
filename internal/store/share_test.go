package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
)

func TestLayoutsAndApplyShared(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p, _ := s.CreateProfile(ctx, profile("a"))
	s.PutLayout(ctx, p.ID, Layout{Key: "public", Positions: map[string]Position{"public.users": {X: 1, Y: 2}}})
	s.PutLayout(ctx, p.ID, Layout{Key: "billing,public", Collapsed: []string{"billing.audit"}})
	s.SetInferredFKDecision(ctx, p.ID, "public.a(b_id)->public.b(id)", DecisionRejected)
	s.PutGlossary(ctx, p.ID, []GlossaryEntry{{Term: "old", Definition: "x"}})

	ls, err := s.Layouts(ctx, p.ID)
	if err != nil || len(ls) != 2 || ls[0].Key != "billing,public" || ls[1].Positions["public.users"] != (Position{1, 2}) {
		t.Fatalf("layouts %+v %v", ls, err)
	}
	if _, err := s.Layouts(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown profile: %v", err)
	}

	err = s.ApplyShared(ctx, p.ID, Shared{
		Layouts:   []Layout{{Key: "public", Positions: map[string]Position{"public.orders": {X: 5, Y: 6}}}},
		Decisions: map[string]string{"public.a(b_id)->public.b(id)": DecisionAccepted, "public.c(d_id)->public.d(id)": DecisionRejected},
		Glossary:  []GlossaryEntry{{Term: "active user", Definition: "logged in this month"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ls, _ = s.Layouts(ctx, p.ID)
	if len(ls) != 1 || len(ls[0].Positions) != 1 || ls[0].Positions["public.orders"] != (Position{5, 6}) {
		t.Fatalf("layouts are replaced as a whole: %+v", ls)
	}
	d, _ := s.InferredFKDecisions(ctx, p.ID)
	if !maps.Equal(d, map[string]string{"public.a(b_id)->public.b(id)": DecisionAccepted, "public.c(d_id)->public.d(id)": DecisionRejected}) {
		t.Fatalf("decisions %v", d)
	}
	g, _ := s.Glossary(ctx, p.ID)
	if !slices.Equal(g, []GlossaryEntry{{Term: "active user", Definition: "logged in this month"}}) {
		t.Fatalf("glossary %v", g)
	}
	if sh, err := s.Shared(ctx, p.ID); err != nil || len(sh.Layouts) != 1 || len(sh.Decisions) != 2 || len(sh.Glossary) != 1 {
		t.Fatalf("shared %+v %v", sh, err)
	}

	// A bad decision status fails the whole import, glossary included.
	err = s.ApplyShared(ctx, p.ID, Shared{Glossary: []GlossaryEntry{{Term: "new"}}, Decisions: map[string]string{"k": "maybe"}})
	if err == nil {
		t.Fatal("check constraint did not reject an unknown status")
	}
	if g, _ := s.Glossary(ctx, p.ID); len(g) != 1 || g[0].Term != "active user" {
		t.Fatalf("a failed import changed the glossary: %v", g)
	}
	if err := s.ApplyShared(ctx, "missing", Shared{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown profile: %v", err)
	}
}

func TestUpdateSharedSeesCurrentStateAndAborts(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p, _ := s.CreateProfile(ctx, profile("a"))
	s.PutGlossary(ctx, p.ID, []GlossaryEntry{{Term: "one"}})
	err := s.UpdateShared(ctx, p.ID, func(cur Shared) (Shared, error) {
		if len(cur.Glossary) != 1 || cur.Glossary[0].Term != "one" {
			t.Fatalf("current %+v", cur)
		}
		cur.Glossary = append(cur.Glossary, GlossaryEntry{Term: "two"})
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("too many terms")
	if err := s.UpdateShared(ctx, p.ID, func(Shared) (Shared, error) { return Shared{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("change error: %v", err)
	}
	if g, _ := s.Glossary(ctx, p.ID); len(g) != 2 {
		t.Fatalf("an aborted update changed the glossary: %v", g)
	}
}

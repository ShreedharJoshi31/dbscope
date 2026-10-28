package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSnapshots(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	prof, err := s.CreateProfile(ctx, profile("a"))
	if err != nil {
		t.Fatal(err)
	}
	p := prof.ID

	saved, err := s.SaveSnapshot(ctx, p, "h1", []string{"public"}, 2, []byte(`{"relations":[1,2]}`))
	if err != nil || !saved {
		t.Fatalf("first save %v %v", saved, err)
	}
	if saved, _ := s.SaveSnapshot(ctx, p, "h1", []string{"public"}, 2, []byte(`{}`)); saved {
		t.Error("an unchanged structure saved again")
	}
	for i := 2; i <= SnapshotsKept+5; i++ {
		if _, err := s.SaveSnapshot(ctx, p, fmt.Sprintf("h%d", i), []string{"public"}, i, []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.Snapshots(ctx, p)
	if err != nil || len(list) != SnapshotsKept || list[0].Relations != SnapshotsKept+5 || list[0].Schemas[0] != "public" {
		t.Fatalf("list %+v %v", list, err)
	}
	model, err := s.Snapshot(ctx, p, list[0].ID)
	if err != nil || string(model) != fmt.Sprintf(`{"n":%d}`, SnapshotsKept+5) {
		t.Errorf("snapshot %s %v", model, err)
	}
	if _, err := s.Snapshot(ctx, p, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing snapshot %v", err)
	}
	if _, err := s.Snapshot(ctx, "other", list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another profile's snapshot %v", err)
	}
}

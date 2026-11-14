package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
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
	for i := 2; i <= conn.DefaultSnapshotsKept+5; i++ {
		if _, err := s.SaveSnapshot(ctx, p, fmt.Sprintf("h%d", i), []string{"public"}, i, []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.Snapshots(ctx, p)
	if err != nil || len(list) != conn.DefaultSnapshotsKept || list[0].Relations != conn.DefaultSnapshotsKept+5 || list[0].Schemas[0] != "public" {
		t.Fatalf("list %+v %v", list, err)
	}
	model, err := s.Snapshot(ctx, p, list[0].ID)
	if err != nil || string(model) != fmt.Sprintf(`{"n":%d}`, conn.DefaultSnapshotsKept+5) {
		t.Errorf("snapshot %s %v", model, err)
	}
	if _, err := s.Snapshot(ctx, p, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing snapshot %v", err)
	}
	if _, err := s.Snapshot(ctx, "other", list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another profile's snapshot %v", err)
	}
}

func TestSnapshotLastSeen(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	prof, err := s.CreateProfile(ctx, profile("a"))
	if err != nil {
		t.Fatal(err)
	}
	p := prof.ID
	if _, err := s.SaveSnapshot(ctx, p, "h1", []string{"public"}, 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Snapshots(ctx, p)
	if len(first) != 1 || !first[0].LastSeenAt.Equal(first[0].TakenAt) {
		t.Fatalf("a new snapshot %+v", first)
	}
	time.Sleep(5 * time.Millisecond)
	if saved, err := s.SaveSnapshot(ctx, p, "h1", []string{"public"}, 1, []byte(`{}`)); saved || err != nil {
		t.Fatalf("unchanged save %v %v", saved, err)
	}
	seen, _ := s.Snapshots(ctx, p)
	if len(seen) != 1 || !seen[0].TakenAt.Equal(first[0].TakenAt) || !seen[0].LastSeenAt.After(first[0].LastSeenAt) {
		t.Fatalf("an unchanged read did not move lastSeenAt: %+v then %+v", first, seen)
	}
	if saved, err := s.SaveSnapshot(ctx, p, "h2", []string{"public"}, 2, []byte(`{}`)); !saved || err != nil {
		t.Fatalf("changed save %v %v", saved, err)
	}
	list, _ := s.Snapshots(ctx, p)
	if len(list) != 2 || list[0].Relations != 2 || !list[0].LastSeenAt.Equal(list[0].TakenAt) || !list[1].LastSeenAt.Equal(seen[0].LastSeenAt) {
		t.Fatalf("after a change %+v", list)
	}
}

func TestSnapshotsKeptPerProfile(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	in := profile("a")
	in.SnapshotsKept = 3
	prof, err := s.CreateProfile(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProfile(ctx, prof.ID); got.SnapshotsKept != 3 {
		t.Fatalf("snapshotsKept read back as %d", got.SnapshotsKept)
	}
	for i := 1; i <= 5; i++ {
		if _, err := s.SaveSnapshot(ctx, prof.ID, fmt.Sprintf("h%d", i), []string{"public"}, i, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.Snapshots(ctx, prof.ID)
	if err != nil || len(list) != 3 || list[0].Relations != 5 || list[1].Relations != 4 || list[2].Relations != 3 {
		t.Fatalf("list %+v %v", list, err)
	}
	in.ID, in.SnapshotsKept = prof.ID, 1
	if _, err := s.UpdateProfile(ctx, in); err == nil {
		t.Fatal("snapshotsKept 1 was stored")
	}
}

// TestSnapshotRevert stores A, B, A as three rows: only the newest is compared.
func TestSnapshotRevert(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	prof, err := s.CreateProfile(ctx, profile("a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"A", "B", "A"} {
		if saved, err := s.SaveSnapshot(ctx, prof.ID, h, []string{"public"}, 1, []byte(`{}`)); !saved || err != nil {
			t.Fatalf("save %s: %v %v", h, saved, err)
		}
	}
	if list, _ := s.Snapshots(ctx, prof.ID); len(list) != 3 {
		t.Fatalf("A->B->A stored %d rows", len(list))
	}
}

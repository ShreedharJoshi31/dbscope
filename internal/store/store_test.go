package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg", "dbscope.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func profile(name string) conn.Profile {
	return conn.Profile{
		Name: name, Host: "db.example", Port: 5432, Database: "app", User: "reader",
		SSLMode: conn.SSLRequire, Env: conn.EnvStaging, Schemas: []string{"public", "sales"},
		SummaryMode: conn.SummaryProfile, MaxRowsToLLM: 50, StatementTimeoutMs: 30000,
	}
}

func TestOpenCreatesPrivateDirAndIsIdempotent(t *testing.T) {
	s, path := openTest(t)
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v, want 0700", fi.Mode().Perm())
		}
	}
	if _, err := s.CreateProfile(context.Background(), profile("a")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	var n int
	if err := s2.db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("schema_migrations rows = %d, err %v", n, err)
	}
	ps, err := s2.ListProfiles(context.Background())
	if err != nil || len(ps) != 1 {
		t.Fatalf("profiles after reopen: %v %v", ps, err)
	}
}

func TestProfileCRUD(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	b, err := s.CreateProfile(ctx, profile("beta"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.ID) != 26 || b.CreatedAt.IsZero() || !b.CreatedAt.Equal(b.UpdatedAt) {
		t.Fatalf("bad generated fields: %+v", b)
	}
	if _, err := s.CreateProfile(ctx, profile("Alpha")); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetProfile(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "beta" || got.Host != "db.example" || got.SSLMode != conn.SSLRequire ||
		len(got.Schemas) != 2 || got.Schemas[1] != "sales" || !got.CreatedAt.Equal(b.CreatedAt) {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	list, err := s.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "Alpha" || list[1].Name != "beta" {
		t.Fatalf("list not sorted by name: %+v", list)
	}

	got.Name = "gamma"
	got.HasPassword = true
	got.Schemas = []string{}
	up, err := s.UpdateProfile(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if !up.CreatedAt.Equal(b.CreatedAt) || up.UpdatedAt.Before(b.UpdatedAt) {
		t.Fatalf("timestamps after update: %+v", up)
	}
	again, _ := s.GetProfile(ctx, b.ID)
	if again.Name != "gamma" || !again.HasPassword || again.Schemas == nil || len(again.Schemas) != 0 {
		t.Fatalf("update not persisted: %+v", again)
	}

	if err := s.DeleteProfile(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProfile(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if err := s.DeleteProfile(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	missing := profile("x")
	missing.ID = "nope"
	if _, err := s.UpdateProfile(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
}

func TestProfileNameUnique(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	a, err := s.CreateProfile(ctx, profile("prod"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProfile(ctx, profile("PROD")); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("create duplicate: %v", err)
	}
	b, err := s.CreateProfile(ctx, profile("other"))
	if err != nil {
		t.Fatal(err)
	}
	b.Name = a.Name
	if _, err := s.UpdateProfile(ctx, b); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename to duplicate: %v", err)
	}
	// Saving a profile under its own name is not a conflict.
	if _, err := s.UpdateProfile(ctx, a); err != nil {
		t.Fatal(err)
	}
}

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLLMSettings(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	got, err := s.GetLLMSettings(ctx)
	if err != nil || got != (LLMSettings{Provider: ProviderNone}) {
		t.Fatalf("default %+v %v", got, err)
	}
	want := LLMSettings{Provider: ProviderAnthropic, Model: "claude-opus-5-5", SummaryModel: "claude-haiku-5-5", Effort: "medium"}
	if err := s.PutLLMSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	want.Provider, want.BaseURL, want.Effort = ProviderOpenAICompat, "http://localhost:11434/v1/", ""
	if err := s.PutLLMSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, err := s2.GetLLMSettings(ctx); err != nil || got != want {
		t.Fatalf("round trip %+v %v", got, err)
	}
	var n int
	s2.db.QueryRow(`SELECT count(*) FROM llm_settings`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d settings rows", n)
	}
	if err := s2.PutLLMSettings(ctx, LLMSettings{Provider: "other"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestGlossary(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p, err := s.CreateProfile(ctx, profile("a"))
	if err != nil {
		t.Fatal(err)
	}
	if g, err := s.Glossary(ctx, p.ID); err != nil || len(g) != 0 || g == nil {
		t.Fatalf("empty %v %v", g, err)
	}
	entries := []GlossaryEntry{
		{Term: "active user", Definition: "signed in within 30 days", SQL: "last_seen_at > now() - interval '30 days'"},
		{Term: "revenue", Definition: "net of refunds"},
	}
	if err := s.PutGlossary(ctx, p.ID, entries); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Glossary(ctx, p.ID); len(g) != 2 || g[0] != entries[0] || g[1] != entries[1] {
		t.Fatalf("got %+v", g)
	}
	// Upsert replaces in place (case-insensitive term) or appends.
	if err := s.UpsertGlossaryTerm(ctx, p.ID, GlossaryEntry{Term: "Revenue", Definition: "gross"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGlossaryTerm(ctx, p.ID, GlossaryEntry{Term: "churn", Definition: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Glossary(ctx, p.ID)
	if len(g) != 3 || g[1].Term != "Revenue" || g[1].Definition != "gross" || g[2].Term != "churn" {
		t.Fatalf("after upsert %+v", g)
	}
	if err := s.PutGlossary(ctx, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Glossary(ctx, p.ID); len(g) != 0 {
		t.Fatalf("cleared %+v", g)
	}
	// Full glossary.
	full := make([]GlossaryEntry, MaxGlossary)
	for i := range full {
		full[i] = GlossaryEntry{Term: fmt.Sprintf("t%d", i)}
	}
	if err := s.PutGlossary(ctx, p.ID, full); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGlossaryTerm(ctx, p.ID, GlossaryEntry{Term: "new"}); !errors.Is(err, ErrGlossaryFull) {
		t.Fatalf("full: %v", err)
	}
	if err := s.UpsertGlossaryTerm(ctx, p.ID, GlossaryEntry{Term: "T7", Definition: "x"}); err != nil {
		t.Fatalf("replace in a full glossary: %v", err)
	}
	// Unknown profile.
	if _, err := s.Glossary(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.PutGlossary(ctx, "missing", entries); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.UpsertGlossaryTerm(ctx, "missing", entries[0]); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// Deleted with the profile.
	if err := s.DeleteProfile(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM glossary WHERE profile_id = ?`, p.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d glossary rows after delete", n)
	}
}

func TestHistorySourceAndQuestion(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p, _ := s.CreateProfile(ctx, profile("a"))
	base := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	if _, err := s.AddHistory(ctx, p.ID, HistoryEntry{SQL: "SELECT 1", StartedAt: base, Status: HistoryOK}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddHistory(ctx, p.ID, HistoryEntry{SQL: "SELECT 2", StartedAt: base.Add(time.Millisecond), Status: HistoryOK,
		Source: SourceAsk, Question: "how many?"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListHistory(ctx, p.ID, 10)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if got[0].Source != SourceAsk || got[0].Question != "how many?" || got[1].Source != SourceQuery || got[1].Question != "" {
		t.Fatalf("%+v", got)
	}
	if _, err := s.AddHistory(ctx, p.ID, HistoryEntry{SQL: "x", StartedAt: base, Status: HistoryOK, Source: "cli"}); err == nil {
		t.Fatal("unknown source accepted")
	}
}

// TestMigration0004OnExistingData upgrades a Phase 2 database that already
// holds history: old rows read back as source "query".
func TestMigration0004OnExistingData(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	p, _ := s.CreateProfile(ctx, profile("a"))
	for _, q := range []string{`DROP TABLE llm_settings`, `DROP TABLE glossary`,
		`ALTER TABLE query_history DROP COLUMN question`, `ALTER TABLE query_history DROP COLUMN source`,
		`DELETE FROM schema_migrations WHERE version = 4`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO query_history (id, profile_id, sql, started_at, duration_ms, status)
		VALUES ('01J0000000000000000000000A', ?, 'SELECT 1', '2026-10-01T00:00:00Z', 1, 'ok')`, p.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	h, err := s2.ListHistory(ctx, p.ID, 5)
	if err != nil || len(h) != 1 || h[0].Source != SourceQuery {
		t.Fatalf("%+v %v", h, err)
	}
}

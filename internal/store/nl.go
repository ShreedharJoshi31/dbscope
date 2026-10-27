package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// LLM providers.
const (
	ProviderNone         = "none"
	ProviderAnthropic    = "anthropic"
	ProviderOpenAICompat = "openai_compat"
)

// LLMSettings is the global provider configuration. The API key is not
// part of it: it lives in the secrets vault.
type LLMSettings struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	SummaryModel string `json:"summaryModel"`
	BaseURL      string `json:"baseUrl,omitempty"`
	Effort       string `json:"effort,omitempty"`
}

// GetLLMSettings returns the saved settings, or provider "none" with empty
// models when nothing was saved yet.
func (s *Store) GetLLMSettings(ctx context.Context) (LLMSettings, error) {
	var l LLMSettings
	err := s.db.QueryRowContext(ctx, `SELECT provider, model, summary_model, base_url, effort FROM llm_settings WHERE id = 1`).
		Scan(&l.Provider, &l.Model, &l.SummaryModel, &l.BaseURL, &l.Effort)
	if errors.Is(err, sql.ErrNoRows) {
		return LLMSettings{Provider: ProviderNone}, nil
	}
	return l, err
}

// PutLLMSettings replaces the settings. Validation is the caller's job;
// the table only checks the provider name.
func (s *Store) PutLLMSettings(ctx context.Context, l LLMSettings) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO llm_settings (id, provider, model, summary_model, base_url, effort, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET provider = excluded.provider, model = excluded.model,
			summary_model = excluded.summary_model, base_url = excluded.base_url, effort = excluded.effort,
			updated_at = excluded.updated_at`,
		l.Provider, l.Model, l.SummaryModel, l.BaseURL, l.Effort, formatTime(now()))
	return err
}

// GlossaryEntry is one business term of a profile's glossary.
type GlossaryEntry struct {
	Term       string `json:"term"`
	Definition string `json:"definition"`
	SQL        string `json:"sql"`
}

// MaxGlossary is the most entries a profile's glossary holds.
const MaxGlossary = 500

// ErrGlossaryFull means an append would exceed MaxGlossary.
var ErrGlossaryFull = errors.New("the glossary already holds 500 entries")

// Glossary returns the profile's entries in order. It returns ErrNotFound
// for an unknown profile.
func (s *Store) Glossary(ctx context.Context, profileID string) ([]GlossaryEntry, error) {
	if err := s.profileExists(ctx, profileID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT term, definition, sql FROM glossary WHERE profile_id = ? ORDER BY position`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GlossaryEntry{}
	for rows.Next() {
		var g GlossaryEntry
		if err := rows.Scan(&g.Term, &g.Definition, &g.SQL); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// PutGlossary replaces the profile's glossary in one transaction. It
// returns ErrNotFound for an unknown profile.
func (s *Store) PutGlossary(ctx context.Context, profileID string, entries []GlossaryEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceGlossary(ctx, tx, profileID, entries); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceGlossary(ctx context.Context, tx *sql.Tx, profileID string, entries []GlossaryEntry) error {
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM profiles WHERE id = ?`, profileID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM glossary WHERE profile_id = ?`, profileID); err != nil {
		return err
	}
	for i, g := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO glossary (profile_id, position, term, definition, sql) VALUES (?, ?, ?, ?, ?)`,
			profileID, i, g.Term, g.Definition, g.SQL); err != nil {
			return fkErr(err)
		}
	}
	return nil
}

// UpsertGlossaryTerm adds e at the end of the glossary, or replaces the
// entry with the same term (compared case-insensitively) in place. It
// returns ErrNotFound for an unknown profile and ErrGlossaryFull when a new
// term would exceed MaxGlossary.
func (s *Store) UpsertGlossaryTerm(ctx context.Context, profileID string, e GlossaryEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT term, definition, sql FROM glossary WHERE profile_id = ? ORDER BY position`, profileID)
	if err != nil {
		return err
	}
	var entries []GlossaryEntry
	for rows.Next() {
		var g GlossaryEntry
		if err := rows.Scan(&g.Term, &g.Definition, &g.SQL); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	replaced := false
	for i := range entries {
		if strings.EqualFold(entries[i].Term, e.Term) {
			entries[i], replaced = e, true
			break
		}
	}
	if !replaced {
		if len(entries) >= MaxGlossary {
			return ErrGlossaryFull
		}
		entries = append(entries, e)
	}
	if err := replaceGlossary(ctx, tx, profileID, entries); err != nil {
		return err
	}
	return tx.Commit()
}

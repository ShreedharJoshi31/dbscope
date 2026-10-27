package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/oklog/ulid/v2"
)

// History statuses.
const (
	HistoryOK        = "ok"
	HistoryError     = "error"
	HistoryRejected  = "rejected"
	HistoryCancelled = "cancelled"
)

// History sources: the query route or the NL ask route.
const (
	SourceQuery = "query"
	SourceAsk   = "ask"
)

// MaxHistory is the most entries ListHistory returns.
const MaxHistory = 200

// HistoryEntry is one query run: SQL and metadata, never row data.
type HistoryEntry struct {
	ID         string    `json:"id"`
	SQL        string    `json:"sql"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs float64   `json:"durationMs"`
	RowCount   int       `json:"rowCount"`
	Status     string    `json:"status"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	// Source is SourceQuery or SourceAsk; empty is stored as SourceQuery.
	Source string `json:"source"`
	// Question is the natural-language question of an ask run.
	Question string `json:"question,omitempty"`
}

// AddHistory records a run and returns it with its ID. It returns
// ErrNotFound when the profile does not exist (e.g. deleted mid-query).
func (s *Store) AddHistory(ctx context.Context, profileID string, e HistoryEntry) (HistoryEntry, error) {
	e.StartedAt = e.StartedAt.UTC().Truncate(time.Millisecond)
	e.ID = ulid.MustNew(ulid.Timestamp(e.StartedAt), ulid.DefaultEntropy()).String()
	if e.Source == "" {
		e.Source = SourceQuery
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO query_history
		(id, profile_id, sql, started_at, duration_ms, row_count, status, error_code, source, question)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, profileID, e.SQL, formatTime(e.StartedAt), e.DurationMs, e.RowCount, e.Status, nullable(e.ErrorCode),
		e.Source, nullable(e.Question))
	if err != nil {
		return HistoryEntry{}, fkErr(err)
	}
	return e, nil
}

// ListHistory returns the profile's newest entries first (IDs are ULIDs of
// the start time; the RFC 3339 text does not sort as time), at most limit
// (clamped to 1..MaxHistory). It returns ErrNotFound for an unknown profile.
func (s *Store) ListHistory(ctx context.Context, profileID string, limit int) ([]HistoryEntry, error) {
	if err := s.profileExists(ctx, profileID); err != nil {
		return nil, err
	}
	limit = min(max(limit, 1), MaxHistory)
	rows, err := s.db.QueryContext(ctx, `SELECT id, sql, started_at, duration_ms, row_count, status, coalesce(error_code, ''),
		source, coalesce(question, '')
		FROM query_history WHERE profile_id = ? ORDER BY id DESC LIMIT ?`, profileID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		var started string
		if err := rows.Scan(&e.ID, &e.SQL, &started, &e.DurationMs, &e.RowCount, &e.Status, &e.ErrorCode, &e.Source, &e.Question); err != nil {
			return nil, err
		}
		if e.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullable(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

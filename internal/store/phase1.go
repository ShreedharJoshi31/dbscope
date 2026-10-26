package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Decision statuses for inferred foreign keys; DecisionUnset removes the decision.
const (
	DecisionAccepted = "accepted"
	DecisionRejected = "rejected"
	DecisionUnset    = "unset"
)

// SetInferredFKDecision records the user's verdict on an inferred foreign
// key. It returns ErrNotFound when the profile does not exist.
func (s *Store) SetInferredFKDecision(ctx context.Context, profileID, key, status string) error {
	if status == DecisionUnset {
		if err := s.profileExists(ctx, profileID); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `DELETE FROM inferred_fk_decisions WHERE profile_id = ? AND key = ?`, profileID, key)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO inferred_fk_decisions (profile_id, key, status, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (profile_id, key) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
		profileID, key, status, formatTime(now()))
	return fkErr(err)
}

// InferredFKDecisions returns the profile's decisions keyed by inferred FK key.
func (s *Store) InferredFKDecisions(ctx context.Context, profileID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, status FROM inferred_fk_decisions WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Layout struct {
	Key       string              `json:"key"`
	Positions map[string]Position `json:"positions"`
	Collapsed []string            `json:"collapsed"`
	UpdatedAt time.Time           `json:"updatedAt"`
}

// GetLayout returns ErrNotFound when no layout is saved under key.
func (s *Store) GetLayout(ctx context.Context, profileID, key string) (Layout, error) {
	var positions, collapsed, updated string
	err := s.db.QueryRowContext(ctx, `SELECT positions_json, collapsed_json, updated_at FROM layouts
		WHERE profile_id = ? AND key = ?`, profileID, key).Scan(&positions, &collapsed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Layout{}, ErrNotFound
	}
	if err != nil {
		return Layout{}, err
	}
	l := Layout{Key: key}
	if err := json.Unmarshal([]byte(positions), &l.Positions); err != nil {
		return Layout{}, err
	}
	if err := json.Unmarshal([]byte(collapsed), &l.Collapsed); err != nil {
		return Layout{}, err
	}
	if l.Positions == nil {
		l.Positions = map[string]Position{}
	}
	if l.Collapsed == nil {
		l.Collapsed = []string{}
	}
	if l.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return Layout{}, err
	}
	return l, nil
}

// PutLayout replaces the layout saved under l.Key. It returns ErrNotFound
// when the profile does not exist.
func (s *Store) PutLayout(ctx context.Context, profileID string, l Layout) error {
	if l.Positions == nil {
		l.Positions = map[string]Position{}
	}
	if l.Collapsed == nil {
		l.Collapsed = []string{}
	}
	positions, err := json.Marshal(l.Positions)
	if err != nil {
		return err
	}
	collapsed, err := json.Marshal(l.Collapsed)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO layouts (profile_id, key, positions_json, collapsed_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, key) DO UPDATE SET positions_json = excluded.positions_json,
			collapsed_json = excluded.collapsed_json, updated_at = excluded.updated_at`,
		profileID, l.Key, string(positions), string(collapsed), formatTime(now()))
	return fkErr(err)
}

func (s *Store) profileExists(ctx context.Context, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM profiles WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// fkErr maps a foreign key violation (no such profile) to ErrNotFound.
func fkErr(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		return ErrNotFound
	}
	return err
}

package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// SnapshotsKept is how many schema snapshots a profile keeps.
const SnapshotsKept = 20

// SnapshotInfo describes a stored schema snapshot.
type SnapshotInfo struct {
	ID        int64     `json:"id"`
	TakenAt   time.Time `json:"takenAt"`
	Relations int       `json:"relations"`
	Schemas   []string  `json:"schemas"`
}

// SaveSnapshot stores model (schema.Model JSON) unless its structure hash
// equals the newest snapshot's, then keeps only the newest SnapshotsKept.
// It reports whether a snapshot was written.
func (s *Store) SaveSnapshot(ctx context.Context, profileID, hash string, schemas []string, relations int, model []byte) (bool, error) {
	var last string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM schema_snapshots WHERE profile_id = ? ORDER BY id DESC LIMIT 1`, profileID).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if last == hash {
		return false, nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(model); err != nil {
		return false, err
	}
	if err := zw.Close(); err != nil {
		return false, err
	}
	names, err := json.Marshal(schemas)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_snapshots (profile_id, taken_at, hash, relations, schemas_json, model)
		VALUES (?, ?, ?, ?, ?, ?)`, profileID, formatTime(now()), hash, relations, string(names), buf.Bytes()); err != nil {
		return false, fkErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_snapshots WHERE profile_id = ? AND id NOT IN (
		SELECT id FROM schema_snapshots WHERE profile_id = ? ORDER BY id DESC LIMIT ?)`, profileID, profileID, SnapshotsKept); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Snapshots lists a profile's snapshots, newest first.
func (s *Store) Snapshots(ctx context.Context, profileID string) ([]SnapshotInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, taken_at, relations, schemas_json FROM schema_snapshots
		WHERE profile_id = ? ORDER BY id DESC`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SnapshotInfo{}
	for rows.Next() {
		var info SnapshotInfo
		var taken, names string
		if err := rows.Scan(&info.ID, &taken, &info.Relations, &names); err != nil {
			return nil, err
		}
		if info.TakenAt, err = time.Parse(time.RFC3339Nano, taken); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(names), &info.Schemas); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// Snapshot returns a snapshot's schema.Model JSON, or ErrNotFound.
func (s *Store) Snapshot(ctx context.Context, profileID string, id int64) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT model FROM schema_snapshots WHERE profile_id = ? AND id = ?`, profileID, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

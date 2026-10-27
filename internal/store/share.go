package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Layouts returns every layout saved for the profile, ordered by key.
func (s *Store) Layouts(ctx context.Context, profileID string) ([]Layout, error) {
	if err := s.profileExists(ctx, profileID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM layouts WHERE profile_id = ? ORDER BY key`, profileID)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Layout{}
	for _, k := range keys {
		l, err := s.GetLayout(ctx, profileID, k)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// Shared is the per-connection state teammates exchange: layouts, inferred
// foreign key decisions and the glossary.
type Shared struct {
	Layouts   []Layout
	Decisions map[string]string // inferred FK key -> DecisionAccepted or DecisionRejected
	Glossary  []GlossaryEntry
}

// Shared returns the profile's layouts, decisions and glossary.
func (s *Store) Shared(ctx context.Context, profileID string) (Shared, error) {
	var sh Shared
	var err error
	if sh.Layouts, err = s.Layouts(ctx, profileID); err != nil {
		return sh, err
	}
	if sh.Decisions, err = s.InferredFKDecisions(ctx, profileID); err != nil {
		return sh, err
	}
	sh.Glossary, err = s.Glossary(ctx, profileID)
	return sh, err
}

// ApplyShared replaces the profile's layouts, decisions and glossary with sh
// in one transaction, so an import is applied completely or not at all.
func (s *Store) ApplyShared(ctx context.Context, profileID string, sh Shared) error {
	return s.UpdateShared(ctx, profileID, func(Shared) (Shared, error) { return sh, nil })
}

// UpdateShared reads the profile's shared state, lets change compute the new
// one, and writes it, all in one transaction: nothing another request saves
// in between is lost.
func (s *Store) UpdateShared(ctx context.Context, profileID string, change func(Shared) (Shared, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A write first takes SQLite's write lock now, so a concurrent writer waits
	// (busy_timeout) instead of failing when this transaction upgrades later.
	res, err := tx.ExecContext(ctx, `UPDATE profiles SET updated_at = updated_at WHERE id = ?`, profileID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	current, err := readShared(ctx, tx, profileID)
	if err != nil {
		return err
	}
	sh, err := change(current)
	if err != nil {
		return err
	}
	if err := replaceGlossary(ctx, tx, profileID, sh.Glossary); err != nil {
		return err
	}
	for _, table := range []string{"layouts", "inferred_fk_decisions"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE profile_id = ?`, profileID); err != nil {
			return err
		}
	}
	ts := formatTime(now())
	for _, l := range sh.Layouts {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO layouts (profile_id, key, positions_json, collapsed_json, updated_at)
			VALUES (?, ?, ?, ?, ?)`,
			profileID, l.Key, string(positions), string(collapsed), ts); err != nil {
			return fkErr(err)
		}
	}
	for key, status := range sh.Decisions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO inferred_fk_decisions (profile_id, key, status, updated_at)
			VALUES (?, ?, ?, ?)`,
			profileID, key, status, ts); err != nil {
			return fkErr(err)
		}
	}
	return tx.Commit()
}

// readShared reads the state inside tx.
func readShared(ctx context.Context, tx *sql.Tx, profileID string) (Shared, error) {
	sh := Shared{Decisions: map[string]string{}, Glossary: []GlossaryEntry{}, Layouts: []Layout{}}
	rows, err := tx.QueryContext(ctx, `SELECT key, positions_json, collapsed_json FROM layouts WHERE profile_id = ? ORDER BY key`, profileID)
	if err != nil {
		return sh, err
	}
	for rows.Next() {
		var l Layout
		var positions, collapsed string
		if err := rows.Scan(&l.Key, &positions, &collapsed); err != nil {
			rows.Close()
			return sh, err
		}
		if err := json.Unmarshal([]byte(positions), &l.Positions); err != nil {
			rows.Close()
			return sh, err
		}
		if err := json.Unmarshal([]byte(collapsed), &l.Collapsed); err != nil {
			rows.Close()
			return sh, err
		}
		sh.Layouts = append(sh.Layouts, l)
	}
	rows.Close()
	if rows, err = tx.QueryContext(ctx, `SELECT key, status FROM inferred_fk_decisions WHERE profile_id = ?`, profileID); err != nil {
		return sh, err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return sh, err
		}
		sh.Decisions[k] = v
	}
	rows.Close()
	if rows, err = tx.QueryContext(ctx, `SELECT term, definition, sql FROM glossary WHERE profile_id = ? ORDER BY position`, profileID); err != nil {
		return sh, err
	}
	defer rows.Close()
	for rows.Next() {
		var g GlossaryEntry
		if err := rows.Scan(&g.Term, &g.Definition, &g.SQL); err != nil {
			return sh, err
		}
		sh.Glossary = append(sh.Glossary, g)
	}
	return sh, rows.Err()
}

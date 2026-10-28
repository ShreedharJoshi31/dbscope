package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/oklog/ulid/v2"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/dbscope/dbscope/internal/conn"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrNameTaken means another profile already uses the name (compared case-insensitively).
	ErrNameTaken = errors.New("a profile with this name already exists")
)

const profileColumns = `id, name, engine, path, options, host, port, database, username, ssl_mode, ssl_root_cert, env, schemas,
	summary_mode, max_rows_to_llm, statement_timeout_ms, max_cost, has_password, created_at, updated_at`

func (s *Store) ListProfiles(ctx context.Context) ([]conn.Profile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+profileColumns+` FROM profiles ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []conn.Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProfile(ctx context.Context, id string) (conn.Profile, error) {
	p, err := scanProfile(s.db.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return conn.Profile{}, ErrNotFound
	}
	return p, err
}

// CreateProfile assigns a new ULID and timestamps and inserts the profile.
func (s *Store) CreateProfile(ctx context.Context, p conn.Profile) (conn.Profile, error) {
	p.ID = ulid.Make().String()
	p.CreatedAt = now()
	p.UpdatedAt = p.CreatedAt
	schemas, err := json.Marshal(p.Schemas)
	if err != nil {
		return conn.Profile{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO profiles (`+profileColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Engine, p.Path, p.Options, p.Host, p.Port, p.Database, p.User, p.SSLMode, p.SSLRootCert, p.Env, string(schemas),
		p.SummaryMode, p.MaxRowsToLLM, p.StatementTimeoutMs, p.MaxCost, p.HasPassword, formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if err != nil {
		return conn.Profile{}, mapErr(err)
	}
	return p, nil
}

// UpdateProfile replaces every field of the profile with p.ID, keeping its
// creation time and setting a new update time.
func (s *Store) UpdateProfile(ctx context.Context, p conn.Profile) (conn.Profile, error) {
	p.UpdatedAt = now()
	schemas, err := json.Marshal(p.Schemas)
	if err != nil {
		return conn.Profile{}, err
	}
	var created string
	err = s.db.QueryRowContext(ctx, `UPDATE profiles SET name = ?, engine = ?, path = ?, options = ?, host = ?, port = ?, database = ?, username = ?,
		ssl_mode = ?, ssl_root_cert = ?, env = ?, schemas = ?, summary_mode = ?, max_rows_to_llm = ?,
		statement_timeout_ms = ?, max_cost = ?, has_password = ?, updated_at = ?
		WHERE id = ? RETURNING created_at`,
		p.Name, p.Engine, p.Path, p.Options, p.Host, p.Port, p.Database, p.User, p.SSLMode, p.SSLRootCert, p.Env, string(schemas),
		p.SummaryMode, p.MaxRowsToLLM, p.StatementTimeoutMs, p.MaxCost, p.HasPassword, formatTime(p.UpdatedAt), p.ID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return conn.Profile{}, ErrNotFound
	}
	if err != nil {
		return conn.Profile{}, mapErr(err)
	}
	if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return conn.Profile{}, err
	}
	return p, nil
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanProfile(row interface{ Scan(...any) error }) (conn.Profile, error) {
	var p conn.Profile
	var schemas, created, updated string
	err := row.Scan(&p.ID, &p.Name, &p.Engine, &p.Path, &p.Options, &p.Host, &p.Port, &p.Database, &p.User, &p.SSLMode, &p.SSLRootCert, &p.Env,
		&schemas, &p.SummaryMode, &p.MaxRowsToLLM, &p.StatementTimeoutMs, &p.MaxCost, &p.HasPassword, &created, &updated)
	if err != nil {
		return conn.Profile{}, err
	}
	if err := json.Unmarshal([]byte(schemas), &p.Schemas); err != nil {
		return conn.Profile{}, err
	}
	if p.Schemas == nil {
		p.Schemas = []string{}
	}
	p.Capabilities = p.Engine.Capabilities()
	if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return conn.Profile{}, err
	}
	if p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return conn.Profile{}, err
	}
	return p, nil
}

func mapErr(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return ErrNameTaken
	}
	return err
}

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

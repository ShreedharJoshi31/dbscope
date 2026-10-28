package agent

import (
	"context"
	"fmt"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/sqlite"
	"github.com/dbscope/dbscope/internal/workload"
)

// sqliteSource opens the profile's file for each call: opening a local file
// costs well under a millisecond, and holding it open would keep it locked
// against tools that replace it.
type sqliteSource struct{}

func (sqliteSource) Namespaces(ctx context.Context, p conn.Profile, _ string) ([]introspect.Namespace, error) {
	m, err := sqliteSource{}.Introspect(ctx, p, "")
	if err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: sqlite.Schema, RelationCount: len(m.Relations)}}, nil
}

func (sqliteSource) Introspect(ctx context.Context, p conn.Profile, _ string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p))
	defer cancel()
	db, err := sqlite.Open(p.Path, statementTimeout(p))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return sqlite.Introspect(ctx, db.DB)
}

// Query is PostgreSQL's request shape (a parsed tree); SQLite statements go through Run.
func (sqliteSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

// Analyze checks migration files against the profile's file (internal/sqlite.Analyze).
func (sqliteSource) Analyze(ctx context.Context, p conn.Profile, files []ingest.Input) (*migrate.Report, error) {
	db, err := sqlite.Open(p.Path, statementTimeout(p))
	if err != nil {
		return nil, err
	}
	base, err := sqlite.ReadBase(ctx, db.DB)
	db.Close()
	if err != nil {
		return nil, err
	}
	return sqlite.Analyze(ctx, base, files)
}

// Run executes a statement that passed sqlite.Check within the profile's statement timeout.
func (sqliteSource) Run(ctx context.Context, p conn.Profile, sqlText string, maxRows int, explainOnly bool) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p))
	defer cancel()
	db, err := sqlite.Open(p.Path, statementTimeout(p))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer db.Close()
	res, err := sqlite.Run(ctx, db.DB, sqlText, maxRows, explainOnly)
	if err != nil && ctx.Err() != nil {
		// The statement timeout interrupted it; say so rather than SQLite's "interrupted".
		return nil, fmt.Errorf("%w: %w", ctx.Err(), err)
	}
	return res, err
}

func (sqliteSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (sqliteSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (sqliteSource) Forget(string) {}
func (sqliteSource) Close()        {}

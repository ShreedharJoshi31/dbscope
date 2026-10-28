package agent

import (
	"context"
	"database/sql"
	"sync"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mysql"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// mysqlSource keeps a database/sql pool per MySQL or MariaDB profile,
// rebuilt when the profile's connection settings change.
type mysqlSource struct {
	mu     sync.Mutex
	byID   map[string]mysqlEntry
	closed bool
}

type mysqlEntry struct {
	db          *sql.DB
	fingerprint string
}

func newMySQLSource() *mysqlSource { return &mysqlSource{byID: map[string]mysqlEntry{}} }

func (ms *mysqlSource) get(p conn.Profile, password string) (*sql.DB, error) {
	fp := fingerprint(p, password)
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.closed {
		return nil, errClosed
	}
	if e, ok := ms.byID[p.ID]; ok {
		if e.fingerprint == fp {
			return e.db, nil
		}
		e.db.Close() // waits for nothing: database/sql closes idle connections and the rest as they return
		delete(ms.byID, p.ID)
	}
	db, err := mysql.Open(p, password, statementTimeout(p))
	if err != nil {
		return nil, err
	}
	ms.byID[p.ID] = mysqlEntry{db: db, fingerprint: fp}
	return db, nil
}

func (ms *mysqlSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mysql.Namespaces(ctx, db)
}

func (ms *mysqlSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mysql.Introspect(ctx, db, p.Schemas, p.Database)
}

// Run executes a statement that passed mysql.Check.
func (ms *mysqlSource) Run(ctx context.Context, p conn.Profile, password, sqlText string, maxRows int, explainOnly bool) (*exec.Result, error) {
	// The plan, then the statement, each within the server-side limit.
	ctx, cancel := context.WithTimeout(ctx, 2*statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mysql.Run(ctx, db, sqlText, maxRows, explainOnly, statementTimeout(p))
}

// Analyze checks migration files against the profile's server (internal/mysql.Analyze).
func (ms *mysqlSource) Analyze(ctx context.Context, p conn.Profile, password string, files []ingest.Input) (*migrate.Report, error) {
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	base, err := mysql.ReadBase(ctx, db, p.Schemas, p.Database)
	if err != nil {
		return nil, err
	}
	return mysql.Analyze(base, files), nil
}

// Health reads the server's health checks (internal/mysql.Health).
func (ms *mysqlSource) Health(ctx context.Context, p conn.Profile, password string) (*health.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mysql.Health(ctx, db)
}

// Query is PostgreSQL's request shape (a parsed tree); MySQL statements go through Run.
func (ms *mysqlSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (ms *mysqlSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (ms *mysqlSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (ms *mysqlSource) Forget(profileID string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if e, ok := ms.byID[profileID]; ok {
		e.db.Close()
		delete(ms.byID, profileID)
	}
}

func (ms *mysqlSource) Close() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.closed = true
	for id, e := range ms.byID {
		e.db.Close()
		delete(ms.byID, id)
	}
}

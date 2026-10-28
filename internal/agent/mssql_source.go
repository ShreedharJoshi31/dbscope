package agent

import (
	"context"
	"database/sql"
	"sync"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/mssql"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// mssqlSource keeps a database/sql pool per SQL Server profile, rebuilt
// when the profile's connection settings change.
type mssqlSource struct {
	mu     sync.Mutex
	byID   map[string]mssqlEntry
	closed bool
}

type mssqlEntry struct {
	db          *sql.DB
	fingerprint string
}

func newMSSQLSource() *mssqlSource { return &mssqlSource{byID: map[string]mssqlEntry{}} }

func (ms *mssqlSource) get(p conn.Profile, password string) (*sql.DB, error) {
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
		e.db.Close()
		delete(ms.byID, p.ID)
	}
	db, err := mssql.Open(p, password)
	if err != nil {
		return nil, err
	}
	ms.byID[p.ID] = mssqlEntry{db: db, fingerprint: fp}
	return db, nil
}

func (ms *mssqlSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mssql.Namespaces(ctx, db)
}

func (ms *mssqlSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mssql.Introspect(ctx, db, p.Schemas)
}

// Run executes a statement that passed mssql.Check.
func (ms *mssqlSource) Run(ctx context.Context, p conn.Profile, password, stmt string, maxRows int, explainOnly bool) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mssql.Run(ctx, db, stmt, maxRows, explainOnly, statementTimeout(p))
}

func (ms *mssqlSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (ms *mssqlSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (ms *mssqlSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (ms *mssqlSource) Forget(profileID string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if e, ok := ms.byID[profileID]; ok {
		e.db.Close()
		delete(ms.byID, profileID)
	}
}

func (ms *mssqlSource) Close() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.closed = true
	for id, e := range ms.byID {
		e.db.Close()
		delete(ms.byID, id)
	}
}

package agent

import (
	"context"
	"sync"

	"github.com/dbscope/dbscope/internal/clickhouse"
	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// clickhouseSource keeps a connection pool per ClickHouse profile, rebuilt
// when its connection settings change.
type clickhouseSource struct {
	mu     sync.Mutex
	byID   map[string]clickhouseEntry
	closed bool
}

type clickhouseEntry struct {
	client      *clickhouse.Client
	fingerprint string
}

func newClickHouseSource() *clickhouseSource {
	return &clickhouseSource{byID: map[string]clickhouseEntry{}}
}

func (cs *clickhouseSource) get(p conn.Profile, password string) (*clickhouse.Client, error) {
	fp := fingerprint(p, password+"\x00"+p.Options)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closed {
		return nil, errClosed
	}
	if e, ok := cs.byID[p.ID]; ok {
		if e.fingerprint == fp {
			return e.client, nil
		}
		go e.client.Close()
		delete(cs.byID, p.ID)
	}
	c, err := clickhouse.Connect(p, password)
	if err != nil {
		return nil, err
	}
	cs.byID[p.ID] = clickhouseEntry{client: c, fingerprint: fp}
	return c, nil
}

func (cs *clickhouseSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	m, err := cs.Introspect(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: p.Database, RelationCount: len(m.Relations)}}, nil
}

func (cs *clickhouseSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*statementTimeout(p)+connectSlack)
	defer cancel()
	c, err := cs.get(p, password)
	if err != nil {
		return nil, err
	}
	return clickhouse.Introspect(ctx, c)
}

// Run executes SQL that passed clickhouse.Check.
func (cs *clickhouseSource) Run(ctx context.Context, p conn.Profile, password, stmt string, maxRows int, explainOnly bool) (*exec.Result, error) {
	c, err := cs.get(p, password)
	if err != nil {
		return nil, err
	}
	return clickhouse.Run(ctx, c, stmt, maxRows, explainOnly, statementTimeout(p))
}

// Analyze checks migration files against the profile's database (internal/clickhouse.Analyze).
func (cs *clickhouseSource) Analyze(ctx context.Context, p conn.Profile, password string, files []ingest.Input) (*migrate.Report, error) {
	c, err := cs.get(p, password)
	if err != nil {
		return nil, err
	}
	base, err := clickhouse.ReadBase(ctx, c)
	if err != nil {
		return nil, err
	}
	return clickhouse.Analyze(base, files), nil
}

func (cs *clickhouseSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (cs *clickhouseSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (cs *clickhouseSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (cs *clickhouseSource) Forget(profileID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if e, ok := cs.byID[profileID]; ok {
		go e.client.Close()
		delete(cs.byID, profileID)
	}
}

func (cs *clickhouseSource) Close() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.closed = true
	for id, e := range cs.byID {
		e.client.Close()
		delete(cs.byID, id)
	}
}

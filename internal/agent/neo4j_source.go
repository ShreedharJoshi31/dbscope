package agent

import (
	"context"
	"sync"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/neo4j"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// neo4jSource keeps a driver per Neo4j profile, rebuilt when its connection
// settings change.
type neo4jSource struct {
	mu     sync.Mutex
	byID   map[string]neo4jEntry
	closed bool
}

type neo4jEntry struct {
	client      *neo4j.Client
	fingerprint string
}

func newNeo4jSource() *neo4jSource { return &neo4jSource{byID: map[string]neo4jEntry{}} }

func (ns *neo4jSource) get(p conn.Profile, password string) (*neo4j.Client, error) {
	fp := fingerprint(p, password+"\x00"+p.Options)
	ns.mu.Lock()
	defer ns.mu.Unlock()
	if ns.closed {
		return nil, errClosed
	}
	if e, ok := ns.byID[p.ID]; ok {
		if e.fingerprint == fp {
			return e.client, nil
		}
		go e.client.Close()
		delete(ns.byID, p.ID)
	}
	c, err := neo4j.Connect(p, password)
	if err != nil {
		return nil, err
	}
	ns.byID[p.ID] = neo4jEntry{client: c, fingerprint: fp}
	return c, nil
}

func (ns *neo4jSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	m, err := ns.Introspect(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: p.Database, RelationCount: len(m.Relations)}}, nil
}

func (ns *neo4jSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*statementTimeout(p)+connectSlack)
	defer cancel()
	c, err := ns.get(p, password)
	if err != nil {
		return nil, err
	}
	return neo4j.Introspect(ctx, c)
}

// Run executes Cypher that passed neo4j.Check.
func (ns *neo4jSource) Run(ctx context.Context, p conn.Profile, password, stmt string, maxRows int, explainOnly bool) (*exec.Result, error) {
	c, err := ns.get(p, password)
	if err != nil {
		return nil, err
	}
	return neo4j.Run(ctx, c, stmt, maxRows, explainOnly, statementTimeout(p))
}

func (ns *neo4jSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (ns *neo4jSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (ns *neo4jSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (ns *neo4jSource) Forget(profileID string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	if e, ok := ns.byID[profileID]; ok {
		go e.client.Close()
		delete(ns.byID, profileID)
	}
}

func (ns *neo4jSource) Close() {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.closed = true
	for id, e := range ns.byID {
		e.client.Close()
		delete(ns.byID, id)
	}
}

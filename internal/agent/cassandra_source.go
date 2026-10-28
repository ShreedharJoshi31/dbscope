package agent

import (
	"context"
	"sync"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/dbscope/dbscope/internal/cassandra"
	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// cassandraSource keeps a session per Cassandra or ScyllaDB profile, rebuilt
// when its connection settings change.
type cassandraSource struct {
	mu     sync.Mutex
	byID   map[string]cassandraEntry
	closed bool
}

type cassandraEntry struct {
	session     *gocql.Session
	fingerprint string
}

func newCassandraSource() *cassandraSource {
	return &cassandraSource{byID: map[string]cassandraEntry{}}
}

func (cs *cassandraSource) get(p conn.Profile, password string) (*gocql.Session, error) {
	fp := fingerprint(p, password+"\x00"+p.Options)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closed {
		return nil, errClosed
	}
	if e, ok := cs.byID[p.ID]; ok {
		if e.fingerprint == fp {
			return e.session, nil
		}
		go e.session.Close()
		delete(cs.byID, p.ID)
	}
	s, err := cassandra.Connect(p, password)
	if err != nil {
		return nil, err
	}
	cs.byID[p.ID] = cassandraEntry{session: s, fingerprint: fp}
	return s, nil
}

func (cs *cassandraSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	m, err := cs.Introspect(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: p.Database, RelationCount: len(m.Relations)}}, nil
}

func (cs *cassandraSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	s, err := cs.get(p, password)
	if err != nil {
		return nil, err
	}
	return cassandra.Introspect(ctx, s, p.Database)
}

// Run executes a CQL SELECT that passed cassandra.Check.
func (cs *cassandraSource) Run(ctx context.Context, p conn.Profile, password, stmt string, maxRows int, explainOnly bool) (*exec.Result, error) {
	s, err := cs.get(p, password)
	if err != nil {
		return nil, err
	}
	return cassandra.Run(ctx, s, p.Database, stmt, maxRows, explainOnly, statementTimeout(p))
}

// Analyze checks CQL schema changes against the profile's keyspace (internal/cassandra.Analyze).
func (cs *cassandraSource) Analyze(ctx context.Context, p conn.Profile, password string, files []ingest.Input) (*migrate.Report, error) {
	s, err := cs.get(p, password)
	if err != nil {
		return nil, err
	}
	base, err := cassandra.ReadBase(ctx, s, p.Database)
	if err != nil {
		return nil, err
	}
	return cassandra.Analyze(base, files), nil
}

func (cs *cassandraSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (cs *cassandraSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (cs *cassandraSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (cs *cassandraSource) Forget(profileID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if e, ok := cs.byID[profileID]; ok {
		go e.session.Close()
		delete(cs.byID, profileID)
	}
}

func (cs *cassandraSource) Close() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.closed = true
	for id, e := range cs.byID {
		e.session.Close()
		delete(cs.byID, id)
	}
}

package agent

import (
	"context"
	"sync"

	mongodrv "go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mongo"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// mongoSource keeps a client per MongoDB profile, rebuilt when its
// connection settings change.
type mongoSource struct {
	mu     sync.Mutex
	byID   map[string]mongoEntry
	closed bool
}

type mongoEntry struct {
	client      *mongodrv.Client
	fingerprint string
}

func newMongoSource() *mongoSource { return &mongoSource{byID: map[string]mongoEntry{}} }

func (ms *mongoSource) get(p conn.Profile, password string) (*mongodrv.Client, error) {
	fp := fingerprint(p, password+"\x00"+p.Options)
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.closed {
		return nil, errClosed
	}
	if e, ok := ms.byID[p.ID]; ok {
		if e.fingerprint == fp {
			return e.client, nil
		}
		go e.client.Disconnect(context.Background())
		delete(ms.byID, p.ID)
	}
	c, err := mongo.Connect(p, password)
	if err != nil {
		return nil, err
	}
	ms.byID[p.ID] = mongoEntry{client: c, fingerprint: fp}
	return c, nil
}

func (ms *mongoSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	c, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mongo.Namespaces(ctx, c)
}

func (ms *mongoSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	c, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mongo.Introspect(ctx, c, p.Database)
}

// Run executes a command document that passed mongo.Check.
func (ms *mongoSource) Run(ctx context.Context, p conn.Profile, password, text string, maxRows int, explainOnly bool) (*exec.Result, error) {
	cmd, err := mongo.Parse(text)
	if err != nil {
		return nil, err
	}
	c, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mongo.Run(ctx, c.Database(p.Database), cmd, maxRows, explainOnly, statementTimeout(p))
}

func (ms *mongoSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

// Analyze checks JavaScript migrations against the profile's database (internal/mongo.Analyze).
func (ms *mongoSource) Analyze(ctx context.Context, p conn.Profile, password string, files []ingest.Input) (*migrate.Report, error) {
	c, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	base, err := mongo.ReadBase(ctx, c, p.Database)
	if err != nil {
		return nil, err
	}
	return mongo.Analyze(base, files), nil
}

func (ms *mongoSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (ms *mongoSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (ms *mongoSource) Forget(profileID string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if e, ok := ms.byID[profileID]; ok {
		go e.client.Disconnect(context.Background())
		delete(ms.byID, profileID)
	}
}

func (ms *mongoSource) Close() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.closed = true
	for id, e := range ms.byID {
		e.client.Disconnect(context.Background())
		delete(ms.byID, id)
	}
}

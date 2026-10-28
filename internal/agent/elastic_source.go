package agent

import (
	"context"
	"sync"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/elastic"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// elasticSource keeps an HTTP client per Elasticsearch or OpenSearch
// profile, rebuilt when its connection settings change.
type elasticSource struct {
	mu   sync.Mutex
	byID map[string]elasticEntry
}

type elasticEntry struct {
	client      *elastic.Client
	fingerprint string
}

func newElasticSource() *elasticSource { return &elasticSource{byID: map[string]elasticEntry{}} }

func (es *elasticSource) get(p conn.Profile, password string) (*elastic.Client, error) {
	fp := fingerprint(p, password+"\x00"+p.Options)
	es.mu.Lock()
	defer es.mu.Unlock()
	if e, ok := es.byID[p.ID]; ok && e.fingerprint == fp {
		return e.client, nil
	}
	c, err := elastic.Connect(p, password)
	if err != nil {
		return nil, err
	}
	es.byID[p.ID] = elasticEntry{client: c, fingerprint: fp}
	return c, nil
}

func (es *elasticSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	m, err := es.Introspect(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: "indices", RelationCount: len(m.Relations)}}, nil
}

func (es *elasticSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*statementTimeout(p)+connectSlack)
	defer cancel()
	c, err := es.get(p, password)
	if err != nil {
		return nil, err
	}
	return elastic.Introspect(ctx, c)
}

// Run executes a search request or SQL that passed elastic.Parse.
func (es *elasticSource) Run(ctx context.Context, p conn.Profile, password string, req *elastic.Request, maxRows int, explainOnly bool) (*exec.Result, error) {
	c, err := es.get(p, password)
	if err != nil {
		return nil, err
	}
	return elastic.Run(ctx, c, req, maxRows, explainOnly, statementTimeout(p))
}

// Analyze checks console-format migration requests against the profile's indices (internal/elastic.Analyze).
func (es *elasticSource) Analyze(ctx context.Context, p conn.Profile, password string, files []ingest.Input) (*migrate.Report, error) {
	c, err := es.get(p, password)
	if err != nil {
		return nil, err
	}
	base, err := elastic.ReadBase(ctx, c)
	if err != nil {
		return nil, err
	}
	return elastic.Analyze(base, files), nil
}

func (es *elasticSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (es *elasticSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (es *elasticSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (es *elasticSource) Forget(profileID string) {
	es.mu.Lock()
	defer es.mu.Unlock()
	delete(es.byID, profileID)
}

func (es *elasticSource) Close() {}

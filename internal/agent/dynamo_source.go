package agent

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/dynamo"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// dynamoSource keeps a DynamoDB client per profile. Clients hold no
// connections of their own, so nothing needs closing.
type dynamoSource struct {
	mu   sync.Mutex
	byID map[string]dynamoEntry
}

type dynamoEntry struct {
	client  *dynamodb.Client
	options string
}

func newDynamoSource() *dynamoSource { return &dynamoSource{byID: map[string]dynamoEntry{}} }

func (ds *dynamoSource) get(ctx context.Context, p conn.Profile) (*dynamodb.Client, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if e, ok := ds.byID[p.ID]; ok && e.options == p.Options {
		return e.client, nil
	}
	c, err := dynamo.Connect(ctx, p)
	if err != nil {
		return nil, err
	}
	ds.byID[p.ID] = dynamoEntry{client: c, options: p.Options}
	return c, nil
}

func (ds *dynamoSource) Namespaces(ctx context.Context, p conn.Profile, _ string) ([]introspect.Namespace, error) {
	m, err := ds.Introspect(ctx, p, "")
	if err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: "tables", RelationCount: len(m.Relations)}}, nil
}

func (ds *dynamoSource) Introspect(ctx context.Context, p conn.Profile, _ string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*statementTimeout(p)+connectSlack)
	defer cancel()
	c, err := ds.get(ctx, p)
	if err != nil {
		return nil, err
	}
	return dynamo.Introspect(ctx, c)
}

// Run executes a PartiQL SELECT that passed dynamo.Check.
func (ds *dynamoSource) Run(ctx context.Context, p conn.Profile, stmt string, maxRows int, explainOnly bool) (*exec.Result, error) {
	c, err := ds.get(ctx, p)
	if err != nil {
		return nil, err
	}
	return dynamo.Run(ctx, c, stmt, maxRows, explainOnly, statementTimeout(p))
}

func (ds *dynamoSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (ds *dynamoSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (ds *dynamoSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (ds *dynamoSource) Forget(profileID string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.byID, profileID)
}

func (ds *dynamoSource) Close() {}

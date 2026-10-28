package agent

import (
	"context"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/redis"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// redisSource keeps a client per Redis or Valkey profile. Its "schema" is
// the key-pattern map of a bounded keyspace scan.
type redisSource struct {
	mu     sync.Mutex
	byID   map[string]redisEntry
	closed bool
}

type redisEntry struct {
	client      *goredis.Client
	fingerprint string
	// last is the latest report, reused for reportTTL so loading the map and
	// opening the report scan once.
	last   *redis.Report
	lastAt time.Time
}

const reportTTL = 30 * time.Second

func newRedisSource() *redisSource { return &redisSource{byID: map[string]redisEntry{}} }

func (rs *redisSource) get(p conn.Profile, password string) (*goredis.Client, error) {
	fp := fingerprint(p, password)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return nil, errClosed
	}
	if e, ok := rs.byID[p.ID]; ok {
		if e.fingerprint == fp {
			return e.client, nil
		}
		e.client.Close()
		delete(rs.byID, p.ID)
	}
	c, err := redis.Connect(p, password)
	if err != nil {
		return nil, err
	}
	rs.byID[p.ID] = redisEntry{client: c, fingerprint: fp}
	return c, nil
}

// Keyspace scans the profile's database within the default budget.
func (rs *redisSource) Keyspace(ctx context.Context, p conn.Profile, password string) (*redis.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, redis.DefaultBudget.Time+statementTimeout(p))
	defer cancel()
	c, err := rs.get(p, password)
	if err != nil {
		return nil, err
	}
	rs.mu.Lock()
	e := rs.byID[p.ID]
	rs.mu.Unlock()
	if e.last != nil && time.Since(e.lastAt) < reportTTL {
		return e.last, nil
	}
	rep, err := redis.Analyze(ctx, c, redis.DefaultBudget)
	if err != nil {
		return nil, err
	}
	rs.mu.Lock()
	if cur, ok := rs.byID[p.ID]; ok && cur.client == c {
		cur.last, cur.lastAt = rep, time.Now()
		rs.byID[p.ID] = cur
	}
	rs.mu.Unlock()
	return rep, nil
}

func (rs *redisSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	r, err := rs.Keyspace(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: "db" + p.Database, RelationCount: len(r.Patterns)}}, nil
}

func (rs *redisSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	r, err := rs.Keyspace(ctx, p, password)
	if err != nil {
		return nil, err
	}
	db, _ := strconv.Atoi(p.Database)
	return r.Model(db), nil
}

func (rs *redisSource) Query(context.Context, conn.Profile, string, exec.Request) (*exec.Result, error) {
	return nil, errUnsupported
}

func (rs *redisSource) MigrationBase(context.Context, conn.Profile, string) (*migrate.Base, error) {
	return nil, errUnsupported
}

func (rs *redisSource) Workload(context.Context, conn.Profile, string, *schema.Model) (*workload.Snapshot, error) {
	return nil, errUnsupported
}

func (rs *redisSource) Forget(profileID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if e, ok := rs.byID[profileID]; ok {
		e.client.Close()
		delete(rs.byID, profileID)
	}
}

func (rs *redisSource) Close() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.closed = true
	for id, e := range rs.byID {
		e.client.Close()
		delete(rs.byID, id)
	}
}

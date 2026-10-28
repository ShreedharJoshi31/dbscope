package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// SchemaSource reads database structure and runs guarded queries for a
// saved profile. Errors are pgx errors (and exec's typed errors), which
// the handlers turn into API responses.
type SchemaSource interface {
	Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error)
	Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error)
	// Query runs a statement that passed guard.Check (exec.Run). It shares
	// the profile's pool: exec passes QueryExecModeExec on every call.
	Query(ctx context.Context, p conn.Profile, password string, req exec.Request) (*exec.Result, error)
	// MigrationBase reads the base schema for migration analysis: structure,
	// sizes, role/database timeouts, and a live impact source on the same
	// pool. ctx bounds the whole analysis, including the impact queries.
	MigrationBase(ctx context.Context, p conn.Profile, password string) (*migrate.Base, error)
	// Workload reads table and statement statistics, attributed to m's tables.
	Workload(ctx context.Context, p conn.Profile, password string, m *schema.Model) (*workload.Snapshot, error)
	// Forget drops anything held for the profile, e.g. after it changed.
	Forget(profileID string)
	// Close releases everything and waits for it to be released.
	Close()
}

var errClosed = errors.New("agent is shutting down")

// connectSlack bounds a request beyond its statement timeout: the 10s
// connect timeout plus time to send the batch and read the results.
const connectSlack = 15 * time.Second

// pools is the default SchemaSource. It keeps one lazily created pool per
// profile (conn.NewPool caps each at 2 connections) and replaces it when the
// profile's connection settings or password change.
type pools struct {
	mu      sync.Mutex
	byID    map[string]*poolEntry
	closed  bool
	retired sync.WaitGroup
}

type poolEntry struct {
	pool *pgxpool.Pool
	// fingerprint hashes the settings the pool was built from.
	fingerprint string
}

func newPools() *pools { return &pools{byID: map[string]*poolEntry{}} }

func fingerprint(p conn.Profile, password string) string {
	h := sha256.New()
	for _, f := range []string{p.Host, strconv.Itoa(p.Port), p.Database, p.User, string(p.SSLMode), p.SSLRootCert, password} {
		h.Write([]byte(strconv.Itoa(len(f)) + ":" + f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (ps *pools) get(ctx context.Context, p conn.Profile, password string) (*pgxpool.Pool, error) {
	fp := fingerprint(p, password)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return nil, errClosed
	}
	if e := ps.byID[p.ID]; e != nil {
		if e.fingerprint == fp {
			return e.pool, nil
		}
		ps.retire(e.pool)
		delete(ps.byID, p.ID)
	}
	// NewPool does not connect (MinConns is 0), so holding the lock is cheap.
	pool, err := conn.NewPool(ctx, p, password)
	if err != nil {
		return nil, err
	}
	ps.byID[p.ID] = &poolEntry{pool: pool, fingerprint: fp}
	return pool, nil
}

// retire closes a pool in the background: Close blocks until in-flight
// requests return their connections, which can take a statement timeout.
// Callers hold ps.mu.
func (ps *pools) retire(pool *pgxpool.Pool) {
	ps.retired.Add(1)
	go func() {
		defer ps.retired.Done()
		pool.Close()
	}()
}

func (ps *pools) Forget(profileID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if e := ps.byID[profileID]; e != nil {
		ps.retire(e.pool)
		delete(ps.byID, profileID)
	}
}

func (ps *pools) Close() {
	ps.mu.Lock()
	ps.closed = true
	for id, e := range ps.byID {
		ps.retire(e.pool)
		delete(ps.byID, id)
	}
	ps.mu.Unlock()
	ps.retired.Wait()
}

func (ps *pools) open() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.byID)
}

func statementTimeout(p conn.Profile) time.Duration {
	return time.Duration(p.StatementTimeoutMs) * time.Millisecond
}

func (ps *pools) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	pool, err := ps.get(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return introspect.Namespaces(ctx, pool, statementTimeout(p))
}

func (ps *pools) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	pool, err := ps.get(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return introspect.Introspect(ctx, pool, p.Schemas, statementTimeout(p))
}

func (ps *pools) Query(ctx context.Context, p conn.Profile, password string, req exec.Request) (*exec.Result, error) {
	// EXPLAIN, EXPLAIN ANALYZE and the fetch can each take a statement timeout.
	ctx, cancel := context.WithTimeout(ctx, 3*statementTimeout(p)+connectSlack)
	defer cancel()
	pool, err := ps.get(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return exec.Run(ctx, pool, req)
}

// Health reads the server's health checks (internal/health.Postgres).
func (ps *pools) Health(ctx context.Context, p conn.Profile, password string) (*health.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	pool, err := ps.get(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return health.Postgres(ctx, pool)
}

func (ps *pools) Workload(ctx context.Context, p conn.Profile, password string, m *schema.Model) (*workload.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	pool, err := ps.get(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return workload.Read(ctx, pool, m, statementTimeout(p))
}

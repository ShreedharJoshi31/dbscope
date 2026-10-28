package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// errUnsupported means the profile's engine lacks the requested mode.
var errUnsupported = errors.New("not supported for this engine")

// engineSource routes each call to the source for the profile's engine
// (docs/ENGINES.md). An engine supplies only the modes its Capabilities name.
type engineSource map[conn.Engine]SchemaSource

func (e engineSource) of(p conn.Profile) (SchemaSource, error) {
	if src := e[p.Engine]; src != nil {
		return src, nil
	}
	return nil, fmt.Errorf("%s: %w", p.Engine, errUnsupported)
}

func (e engineSource) Namespaces(ctx context.Context, p conn.Profile, password string) ([]introspect.Namespace, error) {
	src, err := e.of(p)
	if err != nil {
		return nil, err
	}
	return src.Namespaces(ctx, p, password)
}

func (e engineSource) Introspect(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	src, err := e.of(p)
	if err != nil {
		return nil, err
	}
	return src.Introspect(ctx, p, password)
}

func (e engineSource) Query(ctx context.Context, p conn.Profile, password string, req exec.Request) (*exec.Result, error) {
	src, err := e.of(p)
	if err != nil {
		return nil, err
	}
	return src.Query(ctx, p, password, req)
}

func (e engineSource) MigrationBase(ctx context.Context, p conn.Profile, password string) (*migrate.Base, error) {
	src, err := e.of(p)
	if err != nil {
		return nil, err
	}
	return src.MigrationBase(ctx, p, password)
}

func (e engineSource) Workload(ctx context.Context, p conn.Profile, password string, m *schema.Model) (*workload.Snapshot, error) {
	src, err := e.of(p)
	if err != nil {
		return nil, err
	}
	return src.Workload(ctx, p, password, m)
}

func (e engineSource) Forget(profileID string) {
	for _, src := range e {
		src.Forget(profileID)
	}
}

func (e engineSource) Close() {
	for _, src := range e {
		src.Close()
	}
}

// supports writes a 501 and returns false when the profile's engine lacks a
// mode, before any work for it starts.
// healthSource is a source that reads server health checks.
type healthSource interface {
	Health(ctx context.Context, p conn.Profile, password string) (*health.Report, error)
}

// Health reads the health checks of the profile's engine, when it has them.
func (e engineSource) Health(ctx context.Context, p conn.Profile, password string) (*health.Report, error) {
	src, err := e.of(p)
	if err != nil {
		return nil, err
	}
	hs, ok := src.(healthSource)
	if !ok {
		return nil, errUnsupported
	}
	return hs.Health(ctx, p, password)
}

func supports(w http.ResponseWriter, p conn.Profile, has bool, mode string) bool {
	if !has {
		writeError(w, http.StatusNotImplemented, "unsupported", fmt.Sprintf("%s is not available for %s databases yet", mode, p.Engine))
	}
	return has
}

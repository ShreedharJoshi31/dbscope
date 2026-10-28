package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/infer"
	"github.com/dbscope/dbscope/internal/sample"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/store"
)

const maxKeyBytes = 4096

// profileWithSecret loads the profile named in the path and its password.
// A profile without a password works while the vault is locked.
func (s *server) profileWithSecret(w http.ResponseWriter, r *http.Request) (conn.Profile, string, bool) {
	p, err := s.store.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return p, "", false
	}
	if !p.HasPassword {
		return p, "", true
	}
	password, _, err := s.vault.Get(p.ID)
	if err != nil {
		s.secretError(w, r, err)
		return p, "", false
	}
	return p, password, true
}

// upstreamError reports a failure talking to the profile's database:
// statement timeouts as 504, everything else as 502 with the error kind.
func (s *server) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errClosed) {
		writeError(w, http.StatusServiceUnavailable, "internal", err.Error())
		return
	}
	if errors.Is(err, errUnsupported) {
		writeError(w, http.StatusNotImplemented, "unsupported", err.Error())
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "57014" {
		writeError(w, http.StatusGatewayTimeout, "timeout",
			"the database cancelled introspection after the profile's statement timeout")
		return
	}
	te := conn.Classify(err)
	s.log.Warn("upstream request failed", "path", r.URL.Path, "kind", te.Kind, "sqlstate", te.SQLState)
	writeErrorDetail(w, http.StatusBadGateway, "upstream", te.Message, te.Kind)
}

func (s *server) namespaces(w http.ResponseWriter, r *http.Request) {
	p, password, ok := s.profileWithSecret(w, r)
	if !ok {
		return
	}
	ns, err := s.source.Namespaces(r.Context(), p, password)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

func (s *server) schema(w http.ResponseWriter, r *http.Request) {
	p, password, ok := s.profileWithSecret(w, r)
	if !ok {
		return
	}
	m, err := s.source.Introspect(r.Context(), p, password)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	s.saveSnapshot(r, p.ID, m)
	decisions, err := s.store.InferredFKDecisions(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	infer.Apply(m, decisions)
	writeJSON(w, http.StatusOK, m)
}

// saveSnapshot keeps the schema as read, before the user's inferred-key
// decisions apply, when its structure changed since the last snapshot. A
// failure is logged, never sent: the schema itself was read.
func (s *server) saveSnapshot(r *http.Request, profileID string, m *schema.Model) {
	body, err := json.Marshal(m)
	if err == nil {
		_, err = s.store.SaveSnapshot(r.Context(), profileID, schema.StructureHash(m), m.Schemas, len(m.Relations), body)
	}
	if err != nil {
		s.log.Warn("schema snapshot not saved", "profile", profileID, "err", err)
	}
}

// snapshots lists the profile's schema snapshots, newest first.
func (s *server) snapshots(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.GetProfile(r.Context(), r.PathValue("id")); err != nil {
		s.storeError(w, r, err)
		return
	}
	list, err := s.store.Snapshots(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// snapshot returns one snapshot's model as it was read.
func (s *server) snapshot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("snapshot"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "snapshot must be a number")
		return
	}
	body, err := s.store.Snapshot(r.Context(), r.PathValue("id"), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (s *server) setInferredFK(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key    string `json:"key"`
		Status string `json:"status"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch {
	case body.Key == "" || len(body.Key) > maxKeyBytes:
		writeError(w, http.StatusBadRequest, "validation", "key is required and must be at most 4096 bytes")
		return
	case !strings.Contains(body.Key, ")->") || !strings.HasSuffix(body.Key, ")"):
		writeError(w, http.StatusBadRequest, "validation", "key must look like schema.table(col)->schema.table(col)")
		return
	}
	switch body.Status {
	case store.DecisionAccepted, store.DecisionRejected, store.DecisionUnset:
	default:
		writeError(w, http.StatusBadRequest, "validation", "status must be one of accepted, rejected, unset")
		return
	}
	if err := s.store.SetInferredFKDecision(r.Context(), r.PathValue("id"), body.Key, body.Status); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getLayout(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" || len(key) > maxKeyBytes {
		writeError(w, http.StatusBadRequest, "validation", "key query parameter is required and must be at most 4096 bytes")
		return
	}
	if _, err := s.store.GetProfile(r.Context(), r.PathValue("id")); err != nil {
		s.storeError(w, r, err)
		return
	}
	l, err := s.store.GetLayout(r.Context(), r.PathValue("id"), key)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no layout saved under this key")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *server) putLayout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key       string                    `json:"key"`
		Positions map[string]store.Position `json:"positions"`
		Collapsed []string                  `json:"collapsed"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Key == "" || len(body.Key) > maxKeyBytes {
		writeError(w, http.StatusBadRequest, "validation", "key is required and must be at most 4096 bytes")
		return
	}
	l := store.Layout{Key: body.Key, Positions: body.Positions, Collapsed: body.Collapsed}
	if err := s.store.PutLayout(r.Context(), r.PathValue("id"), l); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sampleJSON is the embedded sample model with inferred keys applied,
// encoded once.
var sampleJSON = sync.OnceValues(func() ([]byte, error) {
	m, err := sample.Model()
	if err != nil {
		return nil, err
	}
	infer.Apply(m, nil)
	return json.Marshal(m)
})

func (s *server) sampleSchema(w http.ResponseWriter, r *http.Request) {
	b, err := sampleJSON()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// workload reads the connection's traffic statistics (docs/WORKLOAD.md),
// attributed to the tables of its current schema.
// keyspace is the key-pattern report of a Redis or Valkey profile.
func (s *server) keyspace(w http.ResponseWriter, r *http.Request) {
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.Keyspace, "The keyspace report") {
		return
	}
	src, _ := s.source.(engineSource)[conn.EngineRedis].(*redisSource)
	if src == nil {
		s.upstreamError(w, r, errUnsupported)
		return
	}
	rep, err := src.Keyspace(r.Context(), p, password)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// serverHealth reads a profile's server health checks (docs/HEALTH.md); s.health is the agent's own.
func (s *server) serverHealth(w http.ResponseWriter, r *http.Request) {
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.Health, "Health checks") {
		return
	}
	src, ok := s.source.(engineSource)
	if !ok {
		s.upstreamError(w, r, errUnsupported)
		return
	}
	rep, err := src.Health(r.Context(), p, password)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *server) workload(w http.ResponseWriter, r *http.Request) {
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.Workload, "Workload") {
		return
	}
	m, err := s.source.Introspect(r.Context(), p, password)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	snap, err := s.source.Workload(r.Context(), p, password, m)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

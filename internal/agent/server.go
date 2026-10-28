package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/nl"
	qguard "github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/secrets"
	"github.com/dbscope/dbscope/internal/store"
)

// Version is set at build time with -ldflags "-X github.com/dbscope/dbscope/internal/agent.Version=...".
var Version = "dev"

const maxBodyBytes = 1 << 20

type Config struct {
	Port      int
	Token     string
	DevOrigin string
	// Assets is the built web UI; nil serves a placeholder page.
	Assets  fs.FS
	Store   *store.Store
	Secrets *secrets.Vault
	Logger  *slog.Logger
	// Schema reads PostgreSQL structure and runs queries; nil keeps a pgx
	// pool per profile. Other engines have their own sources.
	Schema SchemaSource
	// NewProvider builds the LLM client from the saved settings and the
	// vault's key; nil uses the real Anthropic and OpenAI-compatible clients.
	NewProvider ProviderFactory
}

type server struct {
	store       *store.Store
	vault       *secrets.Vault
	log         *slog.Logger
	source      SchemaSource
	newProvider ProviderFactory
	convs       *nl.Conversations
	// rowsConfirmed holds the prod-replica profiles whose rows the user
	// agreed to send to the model during this agent run.
	rowsConfirmed sync.Map
}

// Agent is the agent's HTTP handler. Close releases its database pools.
type Agent struct {
	http.Handler
	source SchemaSource
}

// Close closes every database pool, waiting for in-flight queries to return
// their connections. Call it after the HTTP server has shut down.
func (a *Agent) Close() { a.source.Close() }

// New returns the agent's HTTP handler: API routes and static assets behind
// the localhost guard.
func New(cfg Config) (*Agent, error) {
	switch {
	case cfg.Token == "":
		return nil, errors.New("agent: empty token would disable API authentication")
	case cfg.Port < 1 || cfg.Port > 65535:
		return nil, fmt.Errorf("agent: invalid port %d", cfg.Port)
	case cfg.Store == nil || cfg.Secrets == nil:
		return nil, errors.New("agent: Store and Secrets are required")
	}
	s := &server{store: cfg.Store, vault: cfg.Secrets, log: cfg.Logger, source: cfg.Schema,
		newProvider: cfg.NewProvider, convs: nl.NewConversations()}
	if s.newProvider == nil {
		s.newProvider = newProvider
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.source == nil {
		s.source = newPools()
	}
	s.source = engineSource{conn.EnginePostgres: s.source, conn.EngineSQLite: sqliteSource{}}
	qguard.Warm() // the first parse loads a WebAssembly module (~600 ms)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("POST /api/profiles", s.createProfile)
	mux.HandleFunc("PUT /api/profiles/{id}", s.updateProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.deleteProfile)
	mux.HandleFunc("POST /api/profiles/test", s.testUnsaved)
	mux.HandleFunc("POST /api/profiles/{id}/test", s.testSaved)
	mux.HandleFunc("POST /api/connection-url/parse", s.parseURL)
	mux.HandleFunc("GET /api/secrets/status", s.secretsStatus)
	mux.HandleFunc("POST /api/secrets/unlock", s.unlock)
	mux.HandleFunc("GET /api/profiles/{id}/namespaces", s.namespaces)
	mux.HandleFunc("GET /api/profiles/{id}/schema", s.schema)
	mux.HandleFunc("PUT /api/profiles/{id}/inferred-fks", s.setInferredFK)
	mux.HandleFunc("GET /api/profiles/{id}/layout", s.getLayout)
	mux.HandleFunc("PUT /api/profiles/{id}/layout", s.putLayout)
	mux.HandleFunc("GET /api/sample/schema", s.sampleSchema)
	mux.HandleFunc("POST /api/profiles/{id}/query", s.runQuery)
	mux.HandleFunc("POST /api/profiles/{id}/explain", s.runExplain)
	mux.HandleFunc("GET /api/profiles/{id}/history", s.history)
	mux.HandleFunc("GET /api/llm/settings", s.getLLMSettings)
	mux.HandleFunc("PUT /api/llm/settings", s.putLLMSettings)
	mux.HandleFunc("POST /api/llm/test", s.testLLM)
	mux.HandleFunc("GET /api/llm/models", s.llmModels)
	mux.HandleFunc("GET /api/profiles/{id}/glossary", s.getGlossary)
	mux.HandleFunc("PUT /api/profiles/{id}/glossary", s.putGlossary)
	mux.HandleFunc("GET /api/profiles/{id}/workload", s.workload)
	mux.HandleFunc("GET /api/profiles/{id}/shared", s.exportShared)
	mux.HandleFunc("POST /api/profiles/{id}/shared/preview", s.previewShared)
	mux.HandleFunc("POST /api/profiles/{id}/shared/import", s.importShared)
	mux.HandleFunc("POST /api/profiles/{id}/ask", s.ask)
	mux.HandleFunc("POST /api/profiles/{id}/migrations/analyze", s.analyzeMigrations)
	mux.HandleFunc("POST /api/sample/migrations/analyze", s.analyzeSampleMigrations)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such API route")
	})
	mux.Handle("/", getOnly(static(cfg.Assets)))

	g := guard{port: strconv.Itoa(cfg.Port), token: []byte(cfg.Token), devOrigin: cfg.DevOrigin}
	return &Agent{Handler: g.wrap(mux), source: s.source}, nil
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

func writeErrorDetail(w http.ResponseWriter, status int, code, msg, detail string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg, Detail: detail}})
}

// internalError logs the cause and returns a generic 500; causes can name
// local file paths that the UI has no use for.
func (s *server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

// decode reads exactly one JSON value into v, rejecting unknown fields and
// bodies over 1 MiB. It writes the error response and returns false on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, maxBodyBytes)
}

// decodeLimit is decode with a body limit of limit bytes.
func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, extra := dec.Token(); extra != io.EOF {
			err = errors.New("body must contain a single JSON value")
		}
	}
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "validation", fmt.Sprintf("request body must be at most %d MiB", limit>>20))
	case errors.Is(err, io.EOF):
		writeError(w, http.StatusBadRequest, "validation", "request body is required")
	default:
		writeError(w, http.StatusBadRequest, "validation", "invalid JSON body: "+err.Error())
	}
	return false
}

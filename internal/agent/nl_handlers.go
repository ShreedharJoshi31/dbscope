package agent

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/infer"
	"github.com/dbscope/dbscope/internal/nl"
	"github.com/dbscope/dbscope/internal/nl/llm"
	"github.com/dbscope/dbscope/internal/nl/retrieve"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/secrets"
	"github.com/dbscope/dbscope/internal/store"
)

// Vault names of the LLM API keys. They cannot collide with profile IDs,
// which are ULIDs.
const (
	vaultKeyAnthropic    = "llm:anthropic"
	vaultKeyOpenAICompat = "llm:openai_compat"
)

func apiKeyName(provider string) string {
	if provider == store.ProviderAnthropic {
		return vaultKeyAnthropic
	}
	return vaultKeyOpenAICompat
}

// ProviderFactory builds a provider from settings and the vault's API key.
type ProviderFactory func(s store.LLMSettings, apiKey string) (llm.Provider, error)

func newProvider(s store.LLMSettings, apiKey string) (llm.Provider, error) {
	cfg := llm.Config{APIKey: apiKey, Model: s.Model, SummaryModel: s.SummaryModel, BaseURL: s.BaseURL, Effort: s.Effort}
	if s.Provider == store.ProviderAnthropic {
		cfg.BaseURL = ""
	}
	return llm.New(s.Provider, cfg)
}

// llmSettingsBody is LLMSettings on the wire.
type llmSettingsBody struct {
	store.LLMSettings
	HasAPIKey bool `json:"hasApiKey"`
}

type llmSettingsInput struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	SummaryModel string  `json:"summaryModel"`
	BaseURL      string  `json:"baseUrl"`
	Effort       string  `json:"effort"`
	HasAPIKey    *bool   `json:"hasApiKey"` // accepted and ignored, so a GET body can be PUT back
	APIKey       *string `json:"apiKey"`
}

const (
	maxModelLen  = 200
	maxBaseURL   = 2048
	maxAPIKeyLen = 1000
)

// validLLMSettings applies defaults and checks the settings.
func validLLMSettings(in llmSettingsInput) (store.LLMSettings, error) {
	l := store.LLMSettings{Provider: in.Provider, Model: strings.TrimSpace(in.Model), SummaryModel: strings.TrimSpace(in.SummaryModel),
		BaseURL: strings.TrimSpace(in.BaseURL), Effort: in.Effort}
	if len(l.Model) > maxModelLen || len(l.SummaryModel) > maxModelLen {
		return l, errors.New("model names must be at most 200 characters")
	}
	if in.APIKey != nil && (len(*in.APIKey) > maxAPIKeyLen || strings.ContainsAny(*in.APIKey, "\r\n")) {
		return l, errors.New("apiKey must be one line of at most 1000 characters")
	}
	switch l.Provider {
	case "", store.ProviderNone:
		return store.LLMSettings{Provider: store.ProviderNone}, nil
	case store.ProviderAnthropic:
		if l.BaseURL != "" {
			return l, errors.New("baseUrl applies to openai_compat only")
		}
		if l.Model == "" {
			l.Model = llm.DefaultAnthropicModel
		}
		if l.SummaryModel == "" {
			l.SummaryModel = llm.DefaultAnthropicSummary
		}
		if !slices.Contains(llm.AnthropicModels, l.Model) || !slices.Contains(llm.AnthropicModels, l.SummaryModel) {
			return l, errors.New("model and summaryModel must be one of " + strings.Join(llm.AnthropicModels, ", "))
		}
		switch l.Effort {
		case "":
			l.Effort = llm.DefaultEffort
		case "low", "medium", "high":
		default:
			return l, errors.New("effort must be one of low, medium, high")
		}
	case store.ProviderOpenAICompat:
		if l.Effort != "" {
			return l, errors.New("effort applies to anthropic only")
		}
		if l.Model == "" {
			return l, errors.New("model is required for openai_compat")
		}
		if l.SummaryModel == "" {
			l.SummaryModel = l.Model
		}
		u, err := url.Parse(l.BaseURL)
		if l.BaseURL == "" || len(l.BaseURL) > maxBaseURL || err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return l, errors.New("baseUrl must be an http(s) URL without credentials, e.g. http://localhost:11434/v1/")
		}
		if !strings.HasSuffix(l.BaseURL, "/") {
			l.BaseURL += "/"
		}
	default:
		return l, errors.New("provider must be one of anthropic, openai_compat, none")
	}
	return l, nil
}

// llmConfig loads the settings and the provider's key. A locked vault is
// reported by the caller through secretError.
func (s *server) llmConfig(ctx context.Context) (store.LLMSettings, string, bool, error) {
	l, err := s.store.GetLLMSettings(ctx)
	if err != nil {
		return l, "", false, err
	}
	if l.Provider == store.ProviderNone {
		return l, "", false, nil
	}
	key, ok, err := s.vault.Get(apiKeyName(l.Provider))
	return l, key, ok, err
}

func (s *server) getLLMSettings(w http.ResponseWriter, r *http.Request) {
	l, _, has, err := s.llmConfig(r.Context())
	if err != nil {
		s.secretError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, llmSettingsBody{LLMSettings: l, HasAPIKey: has})
}

func (s *server) putLLMSettings(w http.ResponseWriter, r *http.Request) {
	var in llmSettingsInput
	if !decode(w, r, &in) {
		return
	}
	l, err := validLLMSettings(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	if s.locked(w) {
		return
	}
	if in.APIKey != nil {
		if l.Provider == store.ProviderNone {
			writeError(w, http.StatusBadRequest, "validation", "apiKey needs a provider")
			return
		}
		name := apiKeyName(l.Provider)
		if *in.APIKey == "" {
			err = s.vault.Delete(name)
		} else {
			err = s.vault.Set(name, *in.APIKey)
		}
		if err != nil {
			s.secretError(w, r, err)
			return
		}
	}
	if err := s.store.PutLLMSettings(r.Context(), l); err != nil {
		s.internalError(w, r, err)
		return
	}
	_, has, err := s.vault.Get(apiKeyName(l.Provider))
	if err != nil {
		s.secretError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, llmSettingsBody{LLMSettings: l, HasAPIKey: has && l.Provider != store.ProviderNone})
}

func writeNotConfigured(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusConflict, "llm_not_configured", msg)
}

// provider builds the configured provider, writing the error response
// (409 llm_not_configured, 423) itself when it returns false.
func (s *server) provider(w http.ResponseWriter, r *http.Request) (llm.Provider, store.LLMSettings, bool) {
	l, key, has, err := s.llmConfig(r.Context())
	if err != nil {
		s.secretError(w, r, err)
		return nil, l, false
	}
	switch {
	case l.Provider == store.ProviderNone:
		writeNotConfigured(w, "choose an LLM provider in settings first")
		return nil, l, false
	case l.Provider == store.ProviderAnthropic && !has:
		writeNotConfigured(w, "add an Anthropic API key in settings first")
		return nil, l, false
	}
	p, err := s.newProvider(l, key)
	if err != nil {
		writeNotConfigured(w, err.Error())
		return nil, l, false
	}
	return p, l, true
}

const llmTestTimeout = 60 * time.Second

// testLLM makes one tiny structured-output call. It never sends schema or data.
func (s *server) testLLM(w http.ResponseWriter, r *http.Request) {
	p, l, ok := s.provider(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), llmTestTimeout)
	defer cancel()
	start := time.Now()
	resp, _, err := p.Generate(ctx, llm.Request{
		System:   `This is a connection test. Reply with kind "sql", sql "SELECT 1", tables [], and empty other fields.`,
		Messages: []llm.Message{{Role: "user", Text: "Connection test"}},
	})
	if err == nil {
		_, err = llm.ParseOutput(resp.Text)
		if err != nil {
			err = &llm.Error{Kind: llm.KindOther, Message: "the model did not return the structured output: " + err.Error()}
		}
	}
	body := map[string]any{"ok": err == nil, "provider": l.Provider, "model": l.Model,
		"latencyMs": time.Since(start).Milliseconds()}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		kind, msg := llm.KindOf(err), err.Error()
		var le *llm.Error
		if errors.As(err, &le) {
			msg = le.Message
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			kind, msg = llm.KindNetwork, "the provider did not answer within 60 seconds"
		case kind != llm.KindAuth && kind != llm.KindNetwork && kind != llm.KindModel && kind != llm.KindRateLimit:
			kind = llm.KindOther
		}
		body["error"] = map[string]string{"kind": string(kind), "message": msg}
		s.log.Warn("llm test failed", "provider", l.Provider, "kind", kind)
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) llmModels(w http.ResponseWriter, r *http.Request) {
	l, key, _, err := s.llmConfig(r.Context())
	if err != nil {
		s.secretError(w, r, err)
		return
	}
	if l.Provider == store.ProviderNone {
		writeNotConfigured(w, "choose an LLM provider in settings first")
		return
	}
	p, err := s.newProvider(l, key)
	if err != nil {
		writeNotConfigured(w, err.Error())
		return
	}
	lister, ok := p.(llm.ModelLister)
	if !ok {
		writeJSON(w, http.StatusOK, []string{p.Model()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), llmTestTimeout)
	defer cancel()
	models, err := lister.ListModels(ctx)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		kind, msg := llm.KindOf(err), err.Error()
		var le *llm.Error
		if errors.As(err, &le) {
			msg = le.Message
		}
		if kind == "" {
			kind = llm.KindNetwork
		}
		writeErrorDetail(w, http.StatusBadGateway, "upstream", msg, string(kind))
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *server) getGlossary(w http.ResponseWriter, r *http.Request) {
	g, err := s.store.Glossary(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func validGlossary(entries []store.GlossaryEntry) error {
	if len(entries) > store.MaxGlossary {
		return errors.New("the glossary holds at most 500 entries")
	}
	seen := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		e.Term = strings.TrimSpace(e.Term)
		key := strings.ToLower(e.Term)
		switch {
		case e.Term == "" || utf8.RuneCountInString(e.Term) > nl.MaxTermLen:
			return errors.New("each term must be 1-100 characters")
		case utf8.RuneCountInString(e.Definition) > nl.MaxDefinitionLen:
			return errors.New("each definition must be at most 1000 characters")
		case utf8.RuneCountInString(e.SQL) > nl.MaxTermSQLLen:
			return errors.New("each sql must be at most 1000 characters")
		case seen[key]:
			return errors.New("duplicate term: " + e.Term)
		}
		seen[key] = true
	}
	return nil
}

func (s *server) putGlossary(w http.ResponseWriter, r *http.Request) {
	var entries []store.GlossaryEntry
	if !decode(w, r, &entries) {
		return
	}
	if err := validGlossary(entries); err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	if err := s.store.PutGlossary(r.Context(), r.PathValue("id"), entries); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const (
	maxQuestionLen  = 2000
	maxOverrides    = 20
	maxOverrideText = 1000
)

func validAsk(a *nl.Ask) error {
	a.Question = strings.TrimSpace(a.Question)
	if n := utf8.RuneCountInString(a.Question); n < 1 || n > maxQuestionLen {
		return errors.New("question must be 1-2000 characters")
	}
	if a.MaxRows != nil && (*a.MaxRows < 1 || *a.MaxRows > maxMaxRows) {
		return errors.New("maxRows must be 1-5000")
	}
	if len(a.ConversationID) > 64 {
		return errors.New("conversationId is not valid")
	}
	if len(a.AssumptionOverrides) > maxOverrides {
		return errors.New("at most 20 assumptionOverrides")
	}
	for _, o := range a.AssumptionOverrides {
		if strings.TrimSpace(o.Label) == "" || utf8.RuneCountInString(o.Label) > maxOverrideText || utf8.RuneCountInString(o.SQLFragment) > maxOverrideText {
			return errors.New("each assumption override needs a label; label and sqlFragment are at most 1000 characters")
		}
	}
	if c := a.Clarification; c != nil {
		if strings.TrimSpace(c.Question) == "" || strings.TrimSpace(c.Answer) == "" ||
			utf8.RuneCountInString(c.Question) > maxOverrideText || utf8.RuneCountInString(c.Answer) > maxOverrideText {
			return errors.New("clarification needs a question and an answer of at most 1000 characters each")
		}
	}
	return nil
}

func retrieveOptions(provider string) retrieve.Options {
	if provider == store.ProviderAnthropic {
		return retrieve.AnthropicOptions()
	}
	return retrieve.LocalOptions()
}

// ask streams one natural-language answer as Server-Sent Events.
func (s *server) ask(w http.ResponseWriter, r *http.Request) {
	var a nl.Ask
	if !decode(w, r, &a) {
		return
	}
	if err := validAsk(&a); err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.Ask, "Asking questions") {
		return
	}
	provider, settings, ok := s.provider(w, r)
	if !ok {
		return
	}
	glossary, err := s.store.Glossary(r.Context(), p.ID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	maxRows := defaultMaxRows
	if a.MaxRows != nil {
		maxRows = *a.MaxRows
	}
	rowsMode := p.Env == conn.EnvProdReplica && p.SummaryMode == conn.SummaryRows
	if rowsMode && a.ConfirmRows {
		s.rowsConfirmed.Store(p.ID, true)
	}
	_, confirmed := s.rowsConfirmed.Load(p.ID)

	ctx := r.Context()
	start := time.Now()
	env := &nl.Env{
		Provider: provider, Glossary: glossary, Retrieve: retrieveOptions(settings.Provider),
		Conversations: s.convs, ProfileID: p.ID,
		LoadModel: func(ctx context.Context) (*schema.Model, error) { return s.model(ctx, p, password) },
		Exec: func(ctx context.Context, req exec.Request) (*exec.Result, error) {
			return s.source.Query(ctx, p, password, req)
		},
		Request:     exec.Request{Schemas: p.Schemas, StatementTimeout: statementTimeout(p), MaxRows: maxRows, MaxCost: p.CostLimit()},
		SummaryMode: p.SummaryMode, MaxRowsToLLM: p.MaxRowsToLLM, RowsNeedConfirm: rowsMode && !confirmed,
		SaveGlossary: func(ctx context.Context, e store.GlossaryEntry) error {
			return s.store.UpsertGlossaryTerm(ctx, p.ID, e)
		},
		ErrorBody: func(err error) map[string]any { return s.streamErrorBody(ctx, r, err) },
		Emit:      func(event string, data any) error { return WriteEvent(w, event, data) },
	}
	out := nl.Run(ctx, env, a)
	s.recordAsk(r, p.ID, a.Question, start, out)
}

// model introspects the profile and applies the user's inferred-key decisions.
func (s *server) model(ctx context.Context, p conn.Profile, password string) (*schema.Model, error) {
	m, err := s.source.Introspect(ctx, p, password)
	if err != nil {
		return nil, err
	}
	decisions, err := s.store.InferredFKDecisions(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	infer.Apply(m, decisions)
	return m, nil
}

// streamErrorBody maps a run error onto the envelope body sent as an
// error event: the same mapping as /query.
func (s *server) streamErrorBody(ctx context.Context, r *http.Request, err error) map[string]any {
	switch {
	case errors.Is(err, store.ErrGlossaryFull):
		return map[string]any{"code": "conflict", "message": err.Error()}
	case errors.Is(err, secrets.ErrLocked):
		return map[string]any{"code": "secrets_locked", "message": "unlock the secrets vault first"}
	}
	re := mapRunError(ctx, err)
	if re.status == http.StatusInternalServerError {
		s.log.Error("ask failed", "path", r.URL.Path, "err", re.message)
		re.message = "internal error"
	}
	return re.body()
}

// recordAsk stores an ask that produced SQL, with its question; clarify
// answers and provider failures before any SQL are not recorded.
func (s *server) recordAsk(r *http.Request, profileID, question string, start time.Time, out nl.Outcome) {
	if out.SQL == "" {
		return
	}
	e := store.HistoryEntry{
		SQL: out.SQL, StartedAt: start, Status: store.HistoryOK, Source: store.SourceAsk, Question: question,
		DurationMs: math.Round(float64(out.Duration.Microseconds())/10) / 100,
	}
	var le *nl.LLMError
	switch {
	case out.Confirmation == "cost":
		e.Status, e.ErrorCode = store.HistoryRejected, "cost_confirmation_required"
	case out.Confirmation == "rows":
		e.Status, e.ErrorCode = store.HistoryRejected, "rows_confirmation_required"
	case out.Err == nil && out.Result != nil:
		e.RowCount = out.Result.RowCount
	case out.Err == nil || r.Context().Err() != nil:
		e.Status, e.ErrorCode = store.HistoryCancelled, "cancelled"
	case errors.As(out.Err, &le):
		e.Status, e.ErrorCode = store.HistoryError, "llm"
	default:
		re := mapRunError(r.Context(), out.Err)
		e.Status, e.ErrorCode = re.histStatus, re.histCode
	}
	if _, err := s.store.AddHistory(context.WithoutCancel(r.Context()), profileID, e); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("record ask history", "id", profileID, "err", err)
	}
}

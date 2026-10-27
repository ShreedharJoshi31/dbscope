// Package llm talks to the language model providers dbscope supports
// (docs/NL.md "Providers"): Anthropic (BYOK) and any OpenAI-compatible
// Chat Completions endpoint (Ollama, LM Studio, vLLM, OpenAI). Both return
// the same structured output; errors are classified into a small set of
// kinds the API reports.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Message is one conversation turn. Role is "user" or "assistant".
type Message struct {
	Role string
	Text string
}

// Request is one generation call. System holds the fixed instructions and
// Context the schema and glossary data block; both are byte-stable for a
// given schema snapshot so Anthropic's prompt cache hits.
type Request struct {
	System   string
	Context  string
	Messages []Message
}

// Response is the model's raw output text; ParseOutput decodes it.
type Response struct {
	Text string
}

// Usage counts tokens for one call.
type Usage struct {
	InputTokens     int64 `json:"inputTokens"`
	OutputTokens    int64 `json:"outputTokens"`
	CacheReadTokens int64 `json:"cacheReadTokens"`
}

// Add returns the sum of u and o.
func (u Usage) Add(o Usage) Usage {
	return Usage{u.InputTokens + o.InputTokens, u.OutputTokens + o.OutputTokens, u.CacheReadTokens + o.CacheReadTokens}
}

// SummaryRequest asks for a short plain-text summary of a result.
type SummaryRequest struct {
	System string
	Prompt string
}

// Provider is a configured model endpoint.
type Provider interface {
	// Name is "anthropic", "openai_compat" or "fake".
	Name() string
	// Model is the generation model.
	Model() string
	// Generate makes one structured-output call (schema: OutputSchemaJSON).
	Generate(ctx context.Context, req Request) (Response, Usage, error)
	// Summarize makes one plain-text call with the summary model.
	Summarize(ctx context.Context, req SummaryRequest) (string, Usage, error)
}

// ModelLister is implemented by providers that can list their models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// Config configures a provider. APIKey comes from the secrets vault only.
type Config struct {
	APIKey       string
	Model        string
	SummaryModel string
	// BaseURL is required for OpenAI-compatible endpoints; for Anthropic it
	// overrides the production URL (tests).
	BaseURL string
	// Effort is "low", "medium" or "high" (Anthropic only).
	Effort string
	// HTTPClient defaults to a client with no overall timeout (the request
	// context bounds every call).
	HTTPClient *http.Client
}

// Kind classifies a provider failure.
type Kind string

const (
	KindAuth          Kind = "auth"
	KindNetwork       Kind = "network"
	KindRefusal       Kind = "refusal"
	KindMaxTokens     Kind = "max_tokens"
	KindInvalidOutput Kind = "invalid_output"
	KindRateLimit     Kind = "rate_limit"
	KindModel         Kind = "model"
	KindOther         Kind = "other"
)

// Error is a classified provider failure. Message is safe to show: it never
// contains the API key or the request body.
type Error struct {
	Kind    Kind
	Message string
	Err     error
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the kind of a provider error, or "" for anything else
// (context cancellation included).
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// classify maps a transport or HTTP status failure onto a Kind. status is 0
// when no response arrived. Context errors are returned unchanged so the
// caller can tell cancellation from failure.
func classify(err error, status int, body string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	msg := strings.TrimSpace(body)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Kind: KindAuth, Message: withStatus(status, "the API key was rejected", msg), Err: err}
	case status == http.StatusTooManyRequests:
		return &Error{Kind: KindRateLimit, Message: withStatus(status, "rate limited by the provider", msg), Err: err}
	case status == http.StatusNotFound:
		return &Error{Kind: KindModel, Message: withStatus(status, "the model or endpoint was not found", msg), Err: err}
	case status == http.StatusBadRequest && strings.Contains(strings.ToLower(msg), "model"):
		return &Error{Kind: KindModel, Message: withStatus(status, "the provider rejected the model", msg), Err: err}
	case status >= 500:
		return &Error{Kind: KindNetwork, Message: withStatus(status, "the provider is unavailable", msg), Err: err}
	case status != 0:
		return &Error{Kind: KindOther, Message: withStatus(status, "the provider rejected the request", msg), Err: err}
	}
	var ne net.Error
	var oe *net.OpError
	if errors.As(err, &ne) || errors.As(err, &oe) {
		return &Error{Kind: KindNetwork, Message: "could not reach the provider: " + err.Error(), Err: err}
	}
	return &Error{Kind: KindNetwork, Message: "provider request failed: " + err.Error(), Err: err}
}

func withStatus(status int, what, body string) string {
	if body == "" {
		return fmt.Sprintf("%s (HTTP %d)", what, status)
	}
	return fmt.Sprintf("%s (HTTP %d): %s", what, status, body)
}

// OutputSchemaJSON is the structured-output schema of docs/NL.md, byte for
// byte. It is kept as JSON text rather than a Go map because a map would be
// marshalled with its keys sorted, and the property order (explanation
// before sql) matters to the model. Changing it invalidates the provider's
// compiled grammar and prompt caches.
const OutputSchemaJSON = `{"type":"object","additionalProperties":false,` +
	`"required":["kind","explanation_steps","assumptions","tables","clarifying_question","sql"],` +
	`"properties":{` +
	`"kind":{"type":"string","enum":["sql","clarify"]},` +
	`"explanation_steps":{"type":"array","items":{"type":"string"}},` +
	`"assumptions":{"type":"array","items":{"type":"object","additionalProperties":false,` +
	`"required":["label","sql_fragment"],` +
	`"properties":{"label":{"type":"string"},"sql_fragment":{"type":"string"}}}},` +
	`"tables":{"type":"array","items":{"type":"string"}},` +
	`"clarifying_question":{"type":"object","additionalProperties":false,"required":["question","options"],` +
	`"properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}}},` +
	`"sql":{"type":"string"}}}`

// OutputSchema returns the schema as a fresh map.
func OutputSchema() map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(OutputSchemaJSON), &m); err != nil {
		panic(err)
	}
	return m
}

// SchemaDescription describes the schema in prose for OpenAI-compatible
// endpoints, whose grammar engines may not inject it into the prompt.
const SchemaDescription = `Reply with exactly one JSON object and nothing else, with these keys in this order:
"kind": "sql" or "clarify";
"explanation_steps": array of short user-facing strings describing how the query answers the question;
"assumptions": array of {"label": string, "sql_fragment": string} where sql_fragment is copied verbatim from sql;
"tables": array of "schema.table" names the query reads;
"clarifying_question": {"question": string, "options": array of 2-4 strings} (empty strings and [] when kind is "sql");
"sql": one PostgreSQL SELECT statement (empty string when kind is "clarify").`

type Assumption struct {
	Label       string `json:"label"`
	SQLFragment string `json:"sql_fragment"`
}

type ClarifyingQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// Output is the decoded structured output.
type Output struct {
	Kind               string             `json:"kind"`
	ExplanationSteps   []string           `json:"explanation_steps"`
	Assumptions        []Assumption       `json:"assumptions"`
	Tables             []string           `json:"tables"`
	ClarifyingQuestion ClarifyingQuestion `json:"clarifying_question"`
	SQL                string             `json:"sql"`
}

var requiredKeys = []string{"kind", "explanation_steps", "assumptions", "tables", "clarifying_question", "sql"}

// ParseOutput decodes the model's text. It tolerates a Markdown code fence
// around the object (some local models add one) but requires every field.
// Kind is lower-cased.
func ParseOutput(text string) (Output, error) {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```json")
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimSuffix(strings.TrimSpace(t), "```")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(t), &keys); err != nil {
		return Output{}, fmt.Errorf("the output is not a JSON object: %w", err)
	}
	for _, k := range requiredKeys {
		if v, ok := keys[k]; !ok || bytes.Equal(v, []byte("null")) {
			return Output{}, fmt.Errorf("the output has no %q field", k)
		}
	}
	var o Output
	if err := json.Unmarshal([]byte(t), &o); err != nil {
		return Output{}, fmt.Errorf("the output does not match the schema: %w", err)
	}
	o.Kind = strings.ToLower(strings.TrimSpace(o.Kind))
	return o, nil
}

// Provider names as stored in settings.
const (
	NameAnthropic    = "anthropic"
	NameOpenAICompat = "openai_compat"
)

// New builds the named provider.
func New(name string, cfg Config) (Provider, error) {
	switch name {
	case NameAnthropic:
		return NewAnthropic(cfg), nil
	case NameOpenAICompat:
		if cfg.BaseURL == "" || cfg.Model == "" {
			return nil, errors.New("openai_compat needs a base URL and a model")
		}
		return NewOpenAICompat(cfg), nil
	}
	return nil, fmt.Errorf("no LLM provider is configured (%q)", name)
}

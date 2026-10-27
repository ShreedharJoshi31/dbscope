package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Anthropic model IDs (docs/NL.md "Providers").
const (
	ModelOpus   = "claude-opus-5-5"
	ModelSonnet = "claude-sonnet-5-5"
	ModelHaiku  = "claude-haiku-5-5"

	DefaultAnthropicModel   = ModelOpus
	DefaultAnthropicSummary = ModelHaiku
	DefaultEffort           = "medium"
)

// AnthropicModels is the supported list GET /api/llm/models returns.
var AnthropicModels = []string{ModelOpus, ModelSonnet, ModelHaiku}

const (
	generateMaxTokens = 16000
	summaryMaxTokens  = 2000
)

// fallbackModels get server-side refusal fallbacks (beta
// server-side-fallback-2026-07-01, fallbacks "default").
var fallbackModels = map[string]bool{ModelOpus: true, ModelSonnet: true}

type anthropicProvider struct {
	client              anthropic.Client
	model, summaryModel string
	effort              string
}

// NewAnthropic returns a provider for the Anthropic Messages API. It never
// reads ANTHROPIC_* environment variables or credential files: the key
// comes from cfg only.
func NewAnthropic(cfg Config) Provider {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithHTTPClient(hc),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(1),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	p := &anthropicProvider{client: anthropic.NewClient(opts...), model: cfg.Model, summaryModel: cfg.SummaryModel, effort: cfg.Effort}
	if p.model == "" {
		p.model = DefaultAnthropicModel
	}
	if p.summaryModel == "" {
		p.summaryModel = DefaultAnthropicSummary
	}
	if p.effort == "" {
		p.effort = DefaultEffort
	}
	return p
}

func (p *anthropicProvider) Name() string  { return "anthropic" }
func (p *anthropicProvider) Model() string { return p.model }

func (p *anthropicProvider) ListModels(context.Context) ([]string, error) {
	return append([]string(nil), AnthropicModels...), nil
}

func betaMessages(msgs []Message) []anthropic.BetaMessageParam {
	out := make([]anthropic.BetaMessageParam, 0, len(msgs))
	for _, m := range msgs {
		block := anthropic.NewBetaTextBlock(m.Text)
		if m.Role == "assistant" {
			out = append(out, anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{
				Content: []anthropic.BetaContentBlockParamUnion{block}}})
		} else {
			out = append(out, anthropic.NewBetaUserMessage(block))
		}
	}
	return out
}

func (p *anthropicProvider) Generate(ctx context.Context, req Request) (Response, Usage, error) {
	system := []anthropic.BetaTextBlockParam{{Text: req.System}}
	if req.Context != "" {
		// The breakpoint on the data block caches instructions + schema.
		system = append(system, anthropic.BetaTextBlockParam{Text: req.Context, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()})
	} else {
		system[0].CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
	}
	// Never temperature/top_p/top_k (rejected) and never thinking
	// disabled (rejected on Opus/Sonnet 5.5): effort controls latency.
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: generateMaxTokens,
		System:    system,
		Messages:  betaMessages(req.Messages),
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(p.effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: json.RawMessage(OutputSchemaJSON)},
		},
	}
	if fallbackModels[p.model] {
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}
	text, usage, err := p.stream(ctx, params)
	return Response{Text: text}, usage, err
}

func (p *anthropicProvider) Summarize(ctx context.Context, req SummaryRequest) (string, Usage, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(p.summaryModel),
		MaxTokens: summaryMaxTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		Messages:  betaMessages([]Message{{Role: "user", Text: req.Prompt}}),
	}
	if fallbackModels[p.summaryModel] {
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}
	return p.stream(ctx, params)
}

// stream runs one streamed call and accumulates the final message. The
// stop reason is checked before the text is used.
func (p *anthropicProvider) stream(ctx context.Context, params anthropic.BetaMessageNewParams) (string, Usage, error) {
	s := p.client.Beta.Messages.NewStreaming(ctx, params)
	defer s.Close()
	msg := anthropic.BetaMessage{}
	for s.Next() {
		if err := msg.Accumulate(s.Current()); err != nil {
			return "", Usage{}, &Error{Kind: KindInvalidOutput, Message: "could not read the streamed response: " + err.Error(), Err: err}
		}
	}
	usage := Usage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens, CacheReadTokens: msg.Usage.CacheReadInputTokens}
	if err := s.Err(); err != nil {
		if ctx.Err() != nil {
			return "", usage, ctx.Err()
		}
		return "", usage, anthropicError(err)
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return "", usage, &Error{Kind: KindRefusal, Message: "the model declined to answer this request"}
	case anthropic.BetaStopReasonMaxTokens:
		return "", usage, &Error{Kind: KindMaxTokens, Message: "the model ran out of output tokens before finishing"}
	}
	var b strings.Builder
	for _, c := range msg.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), usage, nil
}

func anthropicError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return classify(err, apiErr.StatusCode, apiMessage(apiErr.RawJSON()))
	}
	return classify(err, 0, "")
}

// apiMessage pulls error.message (or message) out of a provider error body.
func apiMessage(raw string) string {
	var body struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &body) != nil {
		return ""
	}
	if body.Error.Message != "" {
		return body.Error.Message
	}
	return body.Message
}

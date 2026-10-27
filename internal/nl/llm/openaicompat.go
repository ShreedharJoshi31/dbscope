package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

type openAIProvider struct {
	chat                openai.ChatCompletionService
	models              openai.ModelService
	model, summaryModel string
}

// NewOpenAICompat returns a provider for an OpenAI-compatible Chat
// Completions endpoint at cfg.BaseURL (e.g. http://localhost:11434/v1/).
// The services are built directly, not through openai.NewClient, so no
// OPENAI_* environment variable (keys, base URL, headers) is ever read.
func NewOpenAICompat(cfg Config) Provider {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	opts := []option.RequestOption{
		option.WithHTTPClient(hc),
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey), // empty: no Authorization header (Ollama)
		option.WithMaxRetries(1),
	}
	p := &openAIProvider{
		chat:   openai.NewChatCompletionService(opts...),
		models: openai.NewModelService(opts...),
		model:  cfg.Model, summaryModel: cfg.SummaryModel,
	}
	if p.summaryModel == "" {
		p.summaryModel = p.model
	}
	return p
}

func (p *openAIProvider) Name() string  { return "openai_compat" }
func (p *openAIProvider) Model() string { return p.model }

func chatMessages(system string, msgs []Message) []openai.ChatCompletionMessageParamUnion {
	out := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system)}
	for _, m := range msgs {
		if m.Role == "assistant" {
			out = append(out, openai.AssistantMessage(m.Text))
		} else {
			out = append(out, openai.UserMessage(m.Text))
		}
	}
	return out
}

func (p *openAIProvider) Generate(ctx context.Context, req Request) (Response, Usage, error) {
	system := req.System + "\n\n" + SchemaDescription
	if req.Context != "" {
		system += "\n\n" + req.Context
	}
	params := openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: chatMessages(system, req.Messages),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
			JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
				Name: "dbscope_answer", Strict: openai.Bool(true), Schema: json.RawMessage(OutputSchemaJSON),
			},
		}},
	}
	text, usage, err := p.stream(ctx, params)
	return Response{Text: text}, usage, err
}

func (p *openAIProvider) Summarize(ctx context.Context, req SummaryRequest) (string, Usage, error) {
	return p.stream(ctx, openai.ChatCompletionNewParams{
		Model:    p.summaryModel,
		Messages: chatMessages(req.System, []Message{{Role: "user", Text: req.Prompt}}),
	})
}

func (p *openAIProvider) stream(ctx context.Context, params openai.ChatCompletionNewParams) (string, Usage, error) {
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
	s := p.chat.NewStreaming(ctx, params)
	defer s.Close()
	acc := openai.ChatCompletionAccumulator{}
	for s.Next() {
		acc.AddChunk(s.Current())
	}
	usage := Usage{InputTokens: acc.Usage.PromptTokens, OutputTokens: acc.Usage.CompletionTokens,
		CacheReadTokens: acc.Usage.PromptTokensDetails.CachedTokens}
	if err := s.Err(); err != nil {
		if ctx.Err() != nil {
			return "", usage, ctx.Err()
		}
		return "", usage, openAIError(err)
	}
	if len(acc.Choices) == 0 {
		return "", usage, &Error{Kind: KindInvalidOutput, Message: "the endpoint returned no choices"}
	}
	c := acc.Choices[0]
	switch {
	case c.Message.Refusal != "":
		return "", usage, &Error{Kind: KindRefusal, Message: "the model declined to answer this request"}
	case c.FinishReason == "length":
		return "", usage, &Error{Kind: KindMaxTokens, Message: "the model ran out of output tokens before finishing"}
	}
	return c.Message.Content, usage, nil
}

// ListModels calls GET {baseUrl}/models.
func (p *openAIProvider) ListModels(ctx context.Context) ([]string, error) {
	page, err := p.models.List(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, openAIError(err)
	}
	out := []string{}
	for _, m := range page.Data {
		out = append(out, m.ID)
	}
	slices.Sort(out)
	return out, nil
}

func openAIError(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return classify(err, apiErr.StatusCode, apiMessage(apiErr.RawJSON()))
	}
	return classify(err, 0, "")
}

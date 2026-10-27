package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorded is one request an httptest server received.
type recorded struct {
	path   string
	header http.Header
	body   map[string]any
	raw    string
}

type stubServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

func newStub(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) *stubServer {
	t.Helper()
	s := &stubServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{path: r.URL.Path, header: r.Header.Clone(), body: body, raw: string(raw)})
		n := len(s.reqs)
		s.mu.Unlock()
		h(w, r, n)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stubServer) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.reqs...)
}

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprint(w, e)
		w.(http.Flusher).Flush()
	}
}

const validOutput = `{"kind":"sql","explanation_steps":["count users"],"assumptions":[],"tables":["app.users"],` +
	`"clarifying_question":{"question":"","options":[]},"sql":"SELECT count(*) FROM app.users"}`

func anthropicStream(text, stop string) []string {
	js, _ := json.Marshal(text)
	return []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\"," +
			"\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null," +
			"\"usage\":{\"input_tokens\":120,\"output_tokens\":1,\"cache_read_input_tokens\":100,\"cache_creation_input_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + string(js) + "}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stop + "\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":42}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
}

// poisonEnv sets every provider environment variable an SDK could read, so
// a test fails if any of them reaches the wire.
func poisonEnv(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "OPENAI_ADMIN_KEY", "OPENAI_ORG_ID", "OPENAI_PROJECT_ID"} {
		t.Setenv(k, "env-secret-"+strings.ToLower(k))
	}
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1/")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Env-Header: leaked")
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Env-Header: leaked")
}

func assertNoEnvLeak(t *testing.T, r recorded) {
	t.Helper()
	for k, vs := range r.header {
		for _, v := range vs {
			if strings.Contains(v, "env-secret") || strings.EqualFold(k, "X-Env-Header") {
				t.Fatalf("environment value reached the wire: %s: %s", k, v)
			}
		}
	}
}

func TestAnthropicGenerate(t *testing.T) {
	poisonEnv(t)
	s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		sse(w, anthropicStream(validOutput, "end_turn")...)
	})
	p := NewAnthropic(Config{APIKey: "vault-key", BaseURL: s.URL, Model: ModelOpus})
	resp, usage, err := p.Generate(context.Background(), Request{System: "instructions", Context: "schema ddl",
		Messages: []Message{{Role: "user", Text: "q1"}, {Role: "assistant", Text: "a1"}, {Role: "user", Text: "q2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != validOutput || usage.InputTokens != 120 || usage.OutputTokens != 42 || usage.CacheReadTokens != 100 {
		t.Fatalf("resp %q usage %+v", resp.Text, usage)
	}
	r := s.requests()[0]
	assertNoEnvLeak(t, r)
	// SDK v2.3.0 sends the key as a bearer token.
	if r.path != "/v1/messages" || (r.header.Get("X-Api-Key") != "vault-key" && r.header.Get("Authorization") != "Bearer vault-key") {
		t.Fatalf("path %s headers %v", r.path, r.header)
	}
	if !strings.Contains(r.header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") || r.body["fallbacks"] != "default" {
		t.Fatalf("fallbacks: beta %q body %v", r.header.Get("Anthropic-Beta"), r.body["fallbacks"])
	}
	for _, k := range []string{"temperature", "top_p", "top_k", "thinking"} {
		if _, ok := r.body[k]; ok {
			t.Fatalf("request sends %s: %s", k, r.raw)
		}
	}
	oc := r.body["output_config"].(map[string]any)
	if oc["effort"] != "medium" {
		t.Fatalf("effort %v", oc["effort"])
	}
	format := oc["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("format %v", format)
	}
	// The schema goes out byte for byte, in the documented property order.
	if !strings.Contains(r.raw, OutputSchemaJSON) {
		t.Fatalf("schema not sent verbatim: %s", r.raw)
	}
	sys := r.body["system"].([]any)
	if len(sys) != 2 || sys[0].(map[string]any)["text"] != "instructions" || sys[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("system %v", sys)
	}
	if msgs := r.body["messages"].([]any); len(msgs) != 3 || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("messages %v", msgs)
	}
	if r.body["stream"] != true || r.body["model"] != ModelOpus {
		t.Fatalf("body %s", r.raw)
	}
}

func TestAnthropicNoFallbacksForHaiku(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) { sse(w, anthropicStream("fine", "end_turn")...) })
	p := NewAnthropic(Config{APIKey: "k", BaseURL: s.URL, Model: ModelHaiku, SummaryModel: ModelHaiku, Effort: "low"})
	if _, _, err := p.Generate(context.Background(), Request{System: "s", Messages: []Message{{Role: "user", Text: "q"}}}); err != nil {
		t.Fatal(err)
	}
	text, _, err := p.Summarize(context.Background(), SummaryRequest{System: "sum", Prompt: "profile"})
	if err != nil || text != "fine" {
		t.Fatal(text, err)
	}
	reqs := s.requests()
	if _, ok := reqs[0].body["fallbacks"]; ok || strings.Contains(reqs[0].header.Get("Anthropic-Beta"), "fallback") {
		t.Fatalf("fallbacks sent for haiku: %s", reqs[0].raw)
	}
	if reqs[0].body["output_config"].(map[string]any)["effort"] != "low" {
		t.Fatalf("effort %s", reqs[0].raw)
	}
	if _, ok := reqs[1].body["output_config"]; ok || reqs[1].body["model"] != ModelHaiku {
		t.Fatalf("summary request %s", reqs[1].raw)
	}
}

func TestAnthropicStopReasons(t *testing.T) {
	for stop, kind := range map[string]Kind{"refusal": KindRefusal, "max_tokens": KindMaxTokens} {
		s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) { sse(w, anthropicStream(`{"kind":`, stop)...) })
		_, _, err := NewAnthropic(Config{APIKey: "k", BaseURL: s.URL}).Generate(context.Background(), Request{System: "s"})
		if KindOf(err) != kind {
			t.Fatalf("%s: %v", stop, err)
		}
	}
}

func TestAnthropicHTTPErrors(t *testing.T) {
	for status, kind := range map[int]Kind{401: KindAuth, 403: KindAuth, 429: KindRateLimit, 404: KindModel, 500: KindNetwork, 529: KindNetwork, 422: KindOther} {
		s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"x","message":"status %d"}}`, status)
		})
		_, _, err := NewAnthropic(Config{APIKey: "vault-key", BaseURL: s.URL}).Generate(context.Background(), Request{System: "s"})
		if KindOf(err) != kind {
			t.Fatalf("%d: kind %q, err %v", status, KindOf(err), err)
		}
		if strings.Contains(err.Error(), "vault-key") {
			t.Fatalf("error leaks the key: %v", err)
		}
	}
	// A bad model name in a 400 is a model error.
	s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model: claude-nope not found"}}`)
	})
	if _, _, err := NewAnthropic(Config{APIKey: "k", BaseURL: s.URL}).Generate(context.Background(), Request{}); KindOf(err) != KindModel {
		t.Fatalf("400 model: %v", err)
	}
}

func closedURL(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := "http://" + ln.Addr().String() + "/"
	ln.Close()
	return u
}

func TestNetworkErrors(t *testing.T) {
	u := closedURL(t)
	if _, _, err := NewAnthropic(Config{APIKey: "k", BaseURL: u}).Generate(context.Background(), Request{}); KindOf(err) != KindNetwork {
		t.Fatalf("anthropic: %v", err)
	}
	if _, _, err := NewOpenAICompat(Config{BaseURL: u + "v1/", Model: "m"}).Generate(context.Background(), Request{}); KindOf(err) != KindNetwork {
		t.Fatalf("openai: %v", err)
	}
}

// TestCancellation checks that cancelling the context ends a call that is
// waiting on the provider, and returns the context error unclassified.
func TestCancellation(t *testing.T) {
	gone := make(chan struct{}, 2)
	s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		gone <- struct{}{}
	})
	for name, p := range map[string]Provider{
		"anthropic": NewAnthropic(Config{APIKey: "k", BaseURL: s.URL}),
		"openai":    NewOpenAICompat(Config{BaseURL: s.URL + "/v1/", Model: "m"}),
	} {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		start := time.Now()
		_, _, err := p.Generate(ctx, Request{System: "s"})
		if !errors.Is(err, context.Canceled) || KindOf(err) != "" {
			t.Fatalf("%s: %v", name, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("%s: cancel took %v", name, time.Since(start))
		}
		select {
		case <-gone:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: the server never saw the disconnect", name)
		}
	}
}

func openAIStream(content, finish string) []string {
	js, _ := json.Marshal(content)
	return []string{
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":` + string(js) + `},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"` + finish + `"}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":300,"completion_tokens":50,"total_tokens":350,"prompt_tokens_details":{"cached_tokens":7}}}` + "\n\n",
		"data: [DONE]\n\n",
	}
}

func TestOpenAICompatGenerate(t *testing.T) {
	poisonEnv(t)
	s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) { sse(w, openAIStream(validOutput, "stop")...) })
	p := NewOpenAICompat(Config{BaseURL: s.URL + "/v1/", Model: "qwen2.5-coder:7b"})
	resp, usage, err := p.Generate(context.Background(), Request{System: "instructions", Context: "schema ddl",
		Messages: []Message{{Role: "user", Text: "q"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != validOutput || usage != (Usage{300, 50, 7}) {
		t.Fatalf("%q %+v", resp.Text, usage)
	}
	r := s.requests()[0]
	assertNoEnvLeak(t, r)
	if r.path != "/v1/chat/completions" || r.header.Get("Authorization") != "" || r.header.Get("OpenAI-Organization") != "" {
		t.Fatalf("path %s headers %v", r.path, r.header)
	}
	rf := r.body["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || js["name"] == "" || !strings.Contains(r.raw, OutputSchemaJSON) {
		t.Fatalf("response_format %s", r.raw)
	}
	msgs := r.body["messages"].([]any)
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || !strings.Contains(sys["content"].(string), SchemaDescription) ||
		!strings.Contains(sys["content"].(string), "schema ddl") {
		t.Fatalf("system message %v", sys)
	}
	if _, ok := r.body["temperature"]; ok {
		t.Fatal("temperature sent")
	}

	// With a key, it is sent as a bearer token.
	p = NewOpenAICompat(Config{BaseURL: s.URL + "/v1/", Model: "gpt", APIKey: "vault-key"})
	if _, _, err := p.Generate(context.Background(), Request{System: "s"}); err != nil {
		t.Fatal(err)
	}
	if got := s.requests()[1].header.Get("Authorization"); got != "Bearer vault-key" {
		t.Fatalf("authorization %q", got)
	}
}

func TestOpenAICompatFailures(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) { sse(w, openAIStream(`{"kind"`, "length")...) })
	if _, _, err := NewOpenAICompat(Config{BaseURL: s.URL + "/v1/", Model: "m"}).Generate(context.Background(), Request{}); KindOf(err) != KindMaxTokens {
		t.Fatalf("length: %v", err)
	}
	for status, kind := range map[int]Kind{401: KindAuth, 404: KindModel, 429: KindRateLimit, 503: KindNetwork} {
		s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"model \"x\" not found, try pulling it first"}}`)
		})
		_, _, err := NewOpenAICompat(Config{BaseURL: s.URL + "/v1/", Model: "m"}).Generate(context.Background(), Request{})
		if KindOf(err) != kind {
			t.Fatalf("%d: %v", status, err)
		}
	}
}

func TestOpenAICompatModels(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3","object":"model","created":0,"owned_by":"library"},{"id":"llama3","object":"model","created":0,"owned_by":"library"}]}`)
	})
	got, err := NewOpenAICompat(Config{BaseURL: s.URL + "/v1/"}).(ModelLister).ListModels(context.Background())
	if err != nil || strings.Join(got, ",") != "llama3,qwen3" || s.requests()[0].path != "/v1/models" {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := NewAnthropic(Config{}).(ModelLister).ListModels(context.Background()); len(got) != 3 || got[0] != ModelOpus {
		t.Fatalf("anthropic models %v", got)
	}
}

func TestParseOutput(t *testing.T) {
	o, err := ParseOutput("```json\n" + strings.Replace(validOutput, `"sql",`, `"SQL",`, 1) + "\n```")
	if err != nil || o.Kind != "sql" || o.SQL != "SELECT count(*) FROM app.users" || o.Tables[0] != "app.users" {
		t.Fatalf("%+v %v", o, err)
	}
	for _, bad := range []string{"", "not json", `[]`, `{"kind":"sql"}`,
		strings.Replace(validOutput, `"assumptions":[]`, `"assumptions":null`, 1),
		strings.Replace(validOutput, `"tables":["app.users"]`, `"tables":"app.users"`, 1)} {
		if _, err := ParseOutput(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if m := OutputSchema(); m["additionalProperties"] != false || len(m["required"].([]any)) != 6 {
		t.Fatalf("schema map %v", m)
	}
}

func TestFake(t *testing.T) {
	f := NewFake(Reply("a"), Fail(&Error{Kind: KindAuth, Message: "no"}))
	if r, _, err := f.Generate(context.Background(), Request{System: "s"}); err != nil || r.Text != "a" {
		t.Fatal(r, err)
	}
	if _, _, err := f.Generate(context.Background(), Request{}); KindOf(err) != KindAuth {
		t.Fatal(err)
	}
	if _, _, err := f.Generate(context.Background(), Request{}); !errors.Is(err, ErrNoStep) {
		t.Fatal(err)
	}
	if len(f.Requests()) != 3 || f.Requests()[0].System != "s" {
		t.Fatal(f.Requests())
	}
}

// Live tests: opt-in only, never run by default.

func TestLiveAnthropic(t *testing.T) {
	if os.Getenv("DBSCOPE_LIVE_ANTHROPIC") != "1" {
		t.Skip("set DBSCOPE_LIVE_ANTHROPIC=1 (and ANTHROPIC_API_KEY) to call the real API")
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY is not set")
	}
	p := NewAnthropic(Config{APIKey: key, Model: os.Getenv("DBSCOPE_LIVE_ANTHROPIC_MODEL")})
	liveCheck(t, p)
}

func TestLiveOpenAICompat(t *testing.T) {
	base := os.Getenv("DBSCOPE_LIVE_OPENAI_BASEURL")
	if base == "" {
		t.Skip("set DBSCOPE_LIVE_OPENAI_BASEURL (and DBSCOPE_LIVE_OPENAI_MODEL) to call a real endpoint")
	}
	p := NewOpenAICompat(Config{BaseURL: base, Model: os.Getenv("DBSCOPE_LIVE_OPENAI_MODEL"), APIKey: os.Getenv("DBSCOPE_LIVE_OPENAI_KEY")})
	liveCheck(t, p)
}

func liveCheck(t *testing.T, p Provider) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, usage, err := p.Generate(ctx, Request{
		System:   "You translate questions into PostgreSQL. Answer with kind sql.",
		Context:  "CREATE TABLE public.users (id bigint PRIMARY KEY, email text);",
		Messages: []Message{{Role: "user", Text: "How many users are there?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := ParseOutput(resp.Text)
	if err != nil || o.Kind != "sql" || !strings.Contains(strings.ToLower(o.SQL), "count") {
		t.Fatalf("%+v %v (%s)", o, err, resp.Text)
	}
	t.Logf("usage %+v sql %s", usage, o.SQL)
}

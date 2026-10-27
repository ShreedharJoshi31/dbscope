package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/logx"
	"github.com/dbscope/dbscope/internal/nl/llm"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/store"
)

// testAPIKey reuses testPass so env.do fails on any response containing it.
const testAPIKey = testPass

// llmEnv has a fake schema source, a fake LLM and a log buffer.
type llmEnv struct {
	*env
	src  *fakeSource
	fake *llm.Fake
	logs *bytes.Buffer
	mu   sync.Mutex
	// keys records the API key each provider was built with.
	keys []string
}

func newLLMEnv(t *testing.T) *llmEnv {
	e := &llmEnv{env: keyringEnv(t), src: &fakeSource{}, fake: llm.NewFake(), logs: &bytes.Buffer{}}
	a, err := New(Config{Port: testPort, Token: testToken, Store: e.store, Secrets: e.vault, Schema: e.src,
		Logger: logx.New(e.logs, slog.LevelDebug),
		NewProvider: func(s store.LLMSettings, key string) (llm.Provider, error) {
			e.mu.Lock()
			e.keys = append(e.keys, key)
			e.mu.Unlock()
			return e.fake, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	e.h, e.agent = a, a
	t.Cleanup(func() {
		if strings.Contains(e.logs.String(), testAPIKey) {
			t.Errorf("API key in logs:\n%s", e.logs.String())
		}
	})
	return e
}

func (e *llmEnv) configure(body string) {
	e.t.Helper()
	expect(e.t, e.do("PUT", "/api/llm/settings", body), 200, "")
}

type sseEvent struct {
	Name string
	Data json.RawMessage
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, frame := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if frame == "" {
			continue
		}
		name, data, ok := strings.Cut(frame, "\n")
		if !ok || !strings.HasPrefix(name, "event: ") || !strings.HasPrefix(data, "data: ") || strings.Contains(data, "\n") {
			t.Fatalf("bad frame %q", frame)
		}
		out = append(out, sseEvent{strings.TrimPrefix(name, "event: "), json.RawMessage(strings.TrimPrefix(data, "data: "))})
	}
	return out
}

func eventNames(evs []sseEvent) string {
	var names []string
	for _, e := range evs {
		if e.Name == "status" {
			var s struct{ Stage string }
			json.Unmarshal(e.Data, &s)
			names = append(names, "status:"+s.Stage)
		} else {
			names = append(names, e.Name)
		}
	}
	return strings.Join(names, " ")
}

func lastEvent[T any](t *testing.T, evs []sseEvent, name string) T {
	t.Helper()
	var v T
	for _, e := range evs {
		if e.Name == name {
			if err := json.Unmarshal(e.Data, &v); err != nil {
				t.Fatal(err)
			}
		}
	}
	return v
}

func TestLLMSettingsRoutes(t *testing.T) {
	e := newLLMEnv(t)
	type settings struct {
		Provider, Model, SummaryModel, BaseURL, Effort string
		HasAPIKey                                      bool
	}
	get := func() settings {
		w := e.do("GET", "/api/llm/settings", "")
		expect(t, w, 200, "")
		return decodeBody[settings](t, w)
	}
	if s := get(); s != (settings{Provider: "none"}) {
		t.Fatalf("default %+v", s)
	}
	w := e.do("PUT", "/api/llm/settings", `{"provider":"anthropic","apiKey":"`+testAPIKey+`"}`)
	expect(t, w, 200, "")
	want := settings{Provider: "anthropic", Model: "claude-opus-5-5", SummaryModel: "claude-haiku-5-5", Effort: "medium", HasAPIKey: true}
	if s := decodeBody[settings](t, w); s != want {
		t.Fatalf("put %+v", s)
	}
	if s := get(); s != want {
		t.Fatalf("get %+v", s)
	}
	if strings.Contains(w.Body.String(), "apiKey\"") {
		t.Fatalf("apiKey field returned: %s", w.Body.String())
	}
	if k, ok, _ := e.vault.Get("llm:anthropic"); !ok || k != testAPIKey {
		t.Fatal("key not in the vault under llm:anthropic")
	}
	// Omitting apiKey keeps it; a GET body can be PUT back.
	e.configure(`{"provider":"anthropic","model":"claude-sonnet-5-5","summaryModel":"claude-haiku-5-5","effort":"high","hasApiKey":true}`)
	if s := get(); !s.HasAPIKey || s.Model != "claude-sonnet-5-5" || s.Effort != "high" {
		t.Fatalf("kept %+v", s)
	}
	// openai_compat has its own key slot.
	w = e.do("PUT", "/api/llm/settings", `{"provider":"openai_compat","model":"qwen2.5-coder:14b","baseUrl":"http://localhost:11434/v1"}`)
	expect(t, w, 200, "")
	if s := decodeBody[settings](t, w); s.HasAPIKey || s.BaseURL != "http://localhost:11434/v1/" || s.SummaryModel != "qwen2.5-coder:14b" || s.Effort != "" {
		t.Fatalf("openai_compat %+v", s)
	}
	// "" clears a key.
	e.configure(`{"provider":"anthropic","apiKey":""}`)
	if s := get(); s.HasAPIKey {
		t.Fatal("key not cleared")
	}
	if _, ok, _ := e.vault.Get("llm:anthropic"); ok {
		t.Fatal("key still in vault")
	}
	e.configure(`{"provider":"none"}`)
	if s := get(); s != (settings{Provider: "none"}) {
		t.Fatalf("none %+v", s)
	}
	for _, body := range []string{
		`{"provider":"gemini"}`, `{"provider":"anthropic","model":"gpt-4"}`, `{"provider":"anthropic","effort":"max"}`,
		`{"provider":"anthropic","baseUrl":"http://x/"}`, `{"provider":"openai_compat","model":"m"}`,
		`{"provider":"openai_compat","baseUrl":"http://localhost:11434/v1/"}`,
		`{"provider":"openai_compat","model":"m","baseUrl":"ftp://x/"}`,
		`{"provider":"openai_compat","model":"m","baseUrl":"http://u:p@x/"}`,
		`{"provider":"openai_compat","model":"m","baseUrl":"http://x/","effort":"low"}`,
		`{"provider":"none","apiKey":"k"}`, `{"provider":"anthropic","apiKey":"a\nb"}`, `{"provider":"anthropic","extra":1}`,
	} {
		expect(t, e.do("PUT", "/api/llm/settings", body), 400, "validation")
	}
}

func TestLLMSettingsLocked(t *testing.T) {
	e := lockedEnv(t, "pp")
	if err := e.store.PutLLMSettings(t.Context(), store.LLMSettings{Provider: "anthropic", Model: "claude-opus-5-5", SummaryModel: "claude-haiku-5-5"}); err != nil {
		t.Fatal(err)
	}
	expect(t, e.do("GET", "/api/llm/settings", ""), 423, "secrets_locked")
	expect(t, e.do("PUT", "/api/llm/settings", `{"provider":"anthropic","apiKey":"k"}`), 423, "secrets_locked")
	expect(t, e.do("POST", "/api/llm/test", ""), 423, "secrets_locked")
}

func TestLLMNotConfigured(t *testing.T) {
	e := newLLMEnv(t)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"local"}`)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/llm/test", ""}, {"GET", "/api/llm/models", ""},
		{"POST", "/api/profiles/" + p.ID + "/ask", `{"question":"how many users?"}`},
	} {
		expect(t, e.do(c.method, c.path, c.body), 409, "llm_not_configured")
	}
	// Anthropic without a key is not configured either.
	e.configure(`{"provider":"anthropic"}`)
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/ask", `{"question":"how many users?"}`), 409, "llm_not_configured")
	if len(e.keys) != 0 {
		t.Fatal("provider built without a key")
	}
}

func TestLLMTestRoute(t *testing.T) {
	e := newLLMEnv(t)
	e.configure(`{"provider":"anthropic","apiKey":"` + testAPIKey + `"}`)
	e.fake.Push(llm.ReplyOutput(llm.Output{Kind: "sql", SQL: "SELECT 1", ExplanationSteps: []string{}, Assumptions: []llm.Assumption{}, Tables: []string{}}),
		llm.Fail(&llm.Error{Kind: llm.KindAuth, Message: "the API key was rejected (HTTP 401)"}),
		llm.Fail(&llm.Error{Kind: llm.KindRefusal, Message: "declined"}), llm.Reply("not json"))
	type result struct {
		OK              bool
		Provider, Model string
		LatencyMs       *int64
		Error           *struct{ Kind, Message string }
	}
	test := func() result {
		w := e.do("POST", "/api/llm/test", "")
		expect(t, w, 200, "")
		return decodeBody[result](t, w)
	}
	if r := test(); !r.OK || r.Provider != "anthropic" || r.Model != "claude-opus-5-5" || r.LatencyMs == nil || r.Error != nil {
		t.Fatalf("ok %+v", r)
	}
	if e.keys[0] != testAPIKey {
		t.Fatal("provider not given the vault key")
	}
	req := e.fake.Requests()[0]
	if req.Context != "" || len(req.Messages) != 1 {
		t.Fatalf("test call sent schema or data: %+v", req)
	}
	for _, want := range []string{"auth", "other", "other"} {
		if r := test(); r.OK || r.Error == nil || r.Error.Kind != want {
			t.Fatalf("want %s: %+v", want, r)
		}
	}
}

// TestLLMModelsRoute uses the real OpenAI-compatible client against a stub.
func TestLLMModelsRoute(t *testing.T) {
	var gotAuth string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3:8b","object":"model","created":0,"owned_by":"x"},{"id":"llama3.3","object":"model","created":0,"owned_by":"x"}]}`)
	}))
	defer stub.Close()
	e := keyringEnv(t)
	e.do("PUT", "/api/llm/settings", `{"provider":"openai_compat","model":"qwen3:8b","baseUrl":"`+stub.URL+`/v1/","apiKey":"`+testAPIKey+`"}`)
	w := e.do("GET", "/api/llm/models", "")
	expect(t, w, 200, "")
	if got := decodeBody[[]string](t, w); strings.Join(got, ",") != "llama3.3,qwen3:8b" {
		t.Fatalf("models %v", got)
	}
	if gotAuth != "Bearer "+testAPIKey {
		t.Fatalf("auth %q", gotAuth)
	}
	e.do("PUT", "/api/llm/settings", `{"provider":"openai_compat","model":"m","baseUrl":"`+stub.URL+`/nope/"}`)
	w = e.do("GET", "/api/llm/models", "")
	expect(t, w, 502, "upstream")
	if d := decodeBody[apiErrorBody](t, w).Error.Detail; d != "model" {
		t.Fatalf("detail %q", d)
	}
	e.do("PUT", "/api/llm/settings", `{"provider":"anthropic","apiKey":"k"}`)
	w = e.do("GET", "/api/llm/models", "")
	expect(t, w, 200, "")
	if got := decodeBody[[]string](t, w); strings.Join(got, ",") != "claude-opus-5-5,claude-sonnet-5-5,claude-haiku-5-5" {
		t.Fatalf("anthropic models %v", got)
	}
}

func TestGlossaryRoutes(t *testing.T) {
	e := newLLMEnv(t)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"local"}`)
	base := "/api/profiles/" + p.ID + "/glossary"
	w := e.do("GET", base, "")
	expect(t, w, 200, "")
	if w.Body.String() != "[]\n" {
		t.Fatalf("empty %s", w.Body.String())
	}
	body := `[{"term":"active user","definition":"signed in within 30 days","sql":"status = 'active'"},{"term":"revenue","definition":"net","sql":""}]`
	expect(t, e.do("PUT", base, body), 204, "")
	got := decodeBody[[]store.GlossaryEntry](t, e.do("GET", base, ""))
	if len(got) != 2 || got[0].SQL != "status = 'active'" || got[1].Term != "revenue" {
		t.Fatalf("%+v", got)
	}
	long := strings.Repeat("x", 1001)
	many := "[" + strings.TrimSuffix(strings.Repeat(`{"term":"t","definition":""},`, 501), ",") + "]"
	for _, bad := range []string{`[{"term":"","definition":"d"}]`, `[{"term":"` + strings.Repeat("t", 101) + `"}]`,
		`[{"term":"t","definition":"` + long + `"}]`, `[{"term":"t","sql":"` + long + `"}]`,
		`[{"term":"A"},{"term":"a"}]`, many, `{"term":"t"}`, `[{"term":"t","x":1}]`} {
		expect(t, e.do("PUT", base, bad), 400, "validation")
	}
	expect(t, e.do("GET", "/api/profiles/missing/glossary", ""), 404, "not_found")
	expect(t, e.do("PUT", "/api/profiles/missing/glossary", `[]`), 404, "not_found")
}

func sqlAnswer(sql string, tables ...string) llm.Step {
	return llm.ReplyOutput(llm.Output{Kind: "sql", SQL: sql, ExplanationSteps: []string{"Count the users."},
		Assumptions: []llm.Assumption{}, Tables: tables, ClarifyingQuestion: llm.ClarifyingQuestion{Options: []string{}}})
}

func TestAskRoute(t *testing.T) {
	e := newLLMEnv(t)
	e.configure(`{"provider":"anthropic","apiKey":"` + testAPIKey + `"}`)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"staging","schemas":["public"],"password":"pw"}`)
	base := "/api/profiles/" + p.ID + "/ask"

	e.fake.Push(sqlAnswer("SELECT count(*) FROM public.users", "public.users"))
	w := e.do("POST", base, `{"question":"How many users?","maxRows":7}`)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("Cache-Control") != "no-cache" || !w.Flushed {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
	evs := parseSSE(t, w.Body.String())
	want := "status:retrieving context status:generating answer status:checking status:running result summary done"
	if got := eventNames(evs); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	ctx := lastEvent[struct {
		Provider, Model, SchemaMode, Sends string
		Tables, OmittedColumns             []string
	}](t, evs, "context")
	if ctx.Provider != "fake" || ctx.SchemaMode != "full" || ctx.Sends != "schema" || len(ctx.Tables) != 2 || ctx.OmittedColumns == nil {
		t.Fatalf("context %+v", ctx)
	}
	res := lastEvent[exec.Result](t, evs, "result")
	if res.RowCount != 1 || res.ExecutedSQL != "SELECT count(*) FROM public.users" {
		t.Fatalf("result %+v", res)
	}
	done := lastEvent[struct {
		ConversationID string
		DurationMs     *float64
		Usage          map[string]int64
	}](t, evs, "done")
	if done.ConversationID == "" || done.DurationMs == nil || done.Usage["inputTokens"] != 100 /* profile mode: no summary call */ {
		t.Fatalf("done %+v", done)
	}
	// The run used the profile's settings and the vault password and key.
	r := e.src.requests[0]
	if r.MaxRows != 7 || r.MaxCost != 1e7 || r.Schemas[0] != "public" || e.src.passwords[0] != "pw" || e.keys[len(e.keys)-1] != testAPIKey {
		t.Fatalf("request %+v", r)
	}
	h := e.history(p.ID)
	if len(h) != 1 || h[0].Source != "ask" || h[0].Question != "How many users?" || h[0].Status != "ok" || h[0].RowCount != 1 {
		t.Fatalf("history %+v", h)
	}

	// A follow-up keeps the conversation.
	e.fake.Push(sqlAnswer("SELECT count(*) FROM public.posts", "public.posts"))
	w = e.do("POST", base, `{"question":"and posts?","conversationId":"`+done.ConversationID+`"}`)
	if got := lastEvent[struct{ ConversationID string }](t, parseSSE(t, w.Body.String()), "done"); got.ConversationID != done.ConversationID {
		t.Fatalf("conversation %s", got.ConversationID)
	}
	if msgs := e.fake.Requests()[1].Messages; len(msgs) != 3 {
		t.Fatalf("follow-up has %d messages", len(msgs))
	}

	// Provider failures end the stream with an llm error event.
	e.fake.Push(llm.Fail(&llm.Error{Kind: llm.KindRateLimit, Message: "rate limited by the provider (HTTP 429)"}))
	evs = parseSSE(t, e.do("POST", base, `{"question":"again"}`).Body.String())
	if got := eventNames(evs); got != "status:retrieving context status:generating error" {
		t.Fatalf("llm error events %s", got)
	}
	if er := lastEvent[apiError](t, evs, "error"); er.Code != "llm" || er.Detail != "rate_limit" || er.Message == "" {
		t.Fatalf("llm error %+v", er)
	}

	// Database errors: one repair, then the Phase 2 envelope as an error event.
	e.src.queryErr = &exec.DBError{Err: &pgconn.PgError{Severity: "ERROR", Code: "42703", Message: `column "nosuch" does not exist`, Position: 40}, Position: 8}
	e.fake.Push(sqlAnswer("SELECT nosuch FROM public.users", "public.users"), sqlAnswer("SELECT nosuch2 FROM public.users", "public.users"))
	evs = parseSSE(t, e.do("POST", base, `{"question":"broken"}`).Body.String())
	if got := eventNames(evs); !strings.HasSuffix(got, "status:running status:repairing answer status:checking status:running error") {
		t.Fatalf("repair events %s", got)
	}
	er := lastEvent[apiErrorBody](t, []sseEvent{{"x", json.RawMessage(`{"error":` + string(evs[len(evs)-1].Data) + `}`)}}, "x").Error
	if er.Code != "database" || er.SQLState != "42703" || er.Position != 8 {
		t.Fatalf("db error %+v", er)
	}
	if !strings.Contains(e.fake.Requests()[len(e.fake.Requests())-1].Messages[len(e.fake.Requests()[len(e.fake.Requests())-1].Messages)-1].Text,
		"SQLSTATE 42703: column <redacted> does not exist at character 8") {
		t.Fatal("repair message not redacted")
	}
	if h := e.history(p.ID)[0]; h.Status != "error" || h.ErrorCode != "42703" || h.SQL != "SELECT nosuch2 FROM public.users" || h.Source != "ask" {
		t.Fatalf("error history %+v", h)
	}

	// The cost gate becomes needs_confirmation; confirming re-runs without a model call.
	e.src.queryErr = &exec.CostError{EstimatedCost: 2e7, EstimatedRows: 1e6, Limit: 1e7}
	e.fake.Push(sqlAnswer("SELECT * FROM public.posts", "public.posts"))
	evs = parseSSE(t, e.do("POST", base, `{"question":"all posts"}`).Body.String())
	nc := lastEvent[struct {
		Reason         string
		EstimatedCost  float64
		ConversationID string
	}](t, evs, "needs_confirmation")
	if !strings.HasSuffix(eventNames(evs), "needs_confirmation") || nc.Reason != "cost" || nc.EstimatedCost != 2e7 {
		t.Fatalf("cost %s %+v", eventNames(evs), nc)
	}
	if h := e.history(p.ID)[0]; h.Status != "rejected" || h.ErrorCode != "cost_confirmation_required" {
		t.Fatalf("cost history %+v", h)
	}
	e.src.queryErr = nil
	calls := len(e.fake.Requests())
	evs = parseSSE(t, e.do("POST", base, `{"question":"all posts","confirmCost":true,"conversationId":"`+nc.ConversationID+`"}`).Body.String())
	if !strings.HasSuffix(eventNames(evs), "result summary done") || len(e.fake.Requests()) != calls {
		t.Fatalf("confirmed %s", eventNames(evs))
	}
	if !e.src.requests[len(e.src.requests)-1].ConfirmCost {
		t.Fatal("confirmCost not passed")
	}

	// Validation and lookup errors come back as JSON before the stream.
	for _, body := range []string{`{"question":""}`, `{"question":"` + strings.Repeat("x", 2001) + `"}`, `{"question":"q","maxRows":0}`,
		`{"question":"q","extra":1}`, `{"question":"q","clarification":{"question":"","answer":"a"}}`,
		`{"question":"q","assumptionOverrides":[{"label":"","sqlFragment":"x"}]}`} {
		expect(t, e.do("POST", base, body), 400, "validation")
	}
	expect(t, e.do("POST", "/api/profiles/missing/ask", `{"question":"q"}`), 404, "not_found")
}

func TestAskClarifyAndGlossary(t *testing.T) {
	e := newLLMEnv(t)
	e.configure(`{"provider":"openai_compat","model":"m","baseUrl":"http://localhost:1/v1/"}`)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"local","summaryMode":"off"}`)
	base := "/api/profiles/" + p.ID + "/ask"
	e.fake.Push(llm.ReplyOutput(llm.Output{Kind: "clarify", ExplanationSteps: []string{}, Assumptions: []llm.Assumption{}, Tables: []string{},
		ClarifyingQuestion: llm.ClarifyingQuestion{Question: "Which users count as active?", Options: []string{"Logged in this month", "Status active"}}}))
	evs := parseSSE(t, e.do("POST", base, `{"question":"How many active users?"}`).Body.String())
	if got := eventNames(evs); got != "status:retrieving context status:generating answer done" {
		t.Fatalf("clarify events %s", got)
	}
	if len(e.history(p.ID)) != 0 {
		t.Fatal("clarify recorded in history")
	}
	if c := lastEvent[struct{ Sends string }](t, evs, "context"); c.Sends != "schema" {
		t.Fatalf("summary off sends %q", c.Sends)
	}
	e.fake.Push(sqlAnswer("SELECT count(*) FROM public.users", "public.users"))
	evs = parseSSE(t, e.do("POST", base, `{"question":"How many active users?","clarification":{"question":"Which users count as active?","answer":"Status active","saveToGlossary":true}}`).Body.String())
	if got := eventNames(evs); !strings.HasSuffix(got, "result done") || strings.Contains(got, "summar") {
		t.Fatalf("events %s", got)
	}
	g, _ := e.store.Glossary(t.Context(), p.ID)
	if len(g) != 1 || g[0].Term != "Which users count as active?" || g[0].Definition != "Status active" {
		t.Fatalf("glossary %+v", g)
	}
	if !strings.Contains(e.fake.Requests()[1].Context, "Which users count as active?: Status active") {
		t.Fatal("glossary not in the prompt")
	}
}

func TestAskRowsConfirmation(t *testing.T) {
	e := newLLMEnv(t)
	e.configure(`{"provider":"anthropic","apiKey":"k"}`)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"prod-replica","summaryMode":"rows","maxRowsToLLM":3}`)
	base := "/api/profiles/" + p.ID + "/ask"
	e.fake.Push(sqlAnswer("SELECT 1 AS one", "public.users"))
	evs := parseSSE(t, e.do("POST", base, `{"question":"one"}`).Body.String())
	if got := eventNames(evs); got != "status:retrieving context status:generating answer status:checking needs_confirmation" {
		t.Fatalf("events %s", got)
	}
	if nc := lastEvent[struct{ Reason string }](t, evs, "needs_confirmation"); nc.Reason != "rows" || len(e.src.requests) != 0 {
		t.Fatalf("rows confirmation %+v, %d queries", nc, len(e.src.requests))
	}
	evs = parseSSE(t, e.do("POST", base, `{"question":"one","confirmRows":true,"conversationId":"`+
		lastEvent[struct{ ConversationID string }](t, evs, "needs_confirmation").ConversationID+`"}`).Body.String())
	if got := eventNames(evs); got != "status:retrieving context answer status:checking status:running result status:summarizing summary done" {
		t.Fatalf("confirmed events %s", got)
	}
	if s := lastEvent[struct{ Mode string }](t, evs, "summary"); s.Mode != "rows" {
		t.Fatalf("summary %+v", s)
	}
	if !strings.Contains(e.fake.Summaries()[0].Prompt, "<result_rows>") {
		t.Fatal("rows not sent after confirmation")
	}
	// Confirmed once per agent run: the next ask does not ask again.
	e.fake.Push(sqlAnswer("SELECT 2 AS two", "public.users"))
	evs = parseSSE(t, e.do("POST", base, `{"question":"two"}`).Body.String())
	if got := eventNames(evs); !strings.HasSuffix(got, "result status:summarizing summary done") {
		t.Fatalf("second ask events %s", got)
	}
}

func TestAskLockedVault(t *testing.T) {
	e := lockedEnv(t, "pp")
	pi, _ := conn.ProfileInput{Name: "a", Host: "h", Database: "d", User: "u", Env: conn.EnvLocal}.Profile()
	pi.HasPassword = true
	p, err := e.store.CreateProfile(t.Context(), pi)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/ask", `{"question":"q"}`), 423, "secrets_locked")
}

// TestAskCancel disconnects mid-generation: the provider's context ends.
func TestAskCancel(t *testing.T) {
	e := newLLMEnv(t)
	e.configure(`{"provider":"anthropic","apiKey":"k"}`)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"local"}`)
	started, cancelled := make(chan struct{}), make(chan error, 1)
	e.fake.Push(func(ctx context.Context, _ llm.Request) (llm.Response, llm.Usage, error) {
		close(started)
		<-ctx.Done()
		cancelled <- ctx.Err()
		return llm.Response{}, llm.Usage{}, ctx.Err()
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	a, err := New(Config{Port: port, Token: testToken, Store: e.store, Secrets: e.vault, Schema: e.src,
		NewProvider: func(store.LLMSettings, string) (llm.Provider, error) { return e.fake, nil }})
	if err != nil {
		t.Fatal(err)
	}
	hs := &httptest.Server{Listener: ln, Config: &http.Server{Handler: a}}
	hs.Start()
	defer a.Close()
	defer hs.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", hs.URL+"/api/profiles/"+p.ID+"/ask", strings.NewReader(`{"question":"slow"}`))
	req.Header.Set(TokenHeader, testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// Headers and the first events arrive before generation finishes.
	buf := make([]byte, 64)
	if n, err := resp.Body.Read(buf); err != nil || !strings.HasPrefix(string(buf[:n]), "event: status") {
		t.Fatalf("first read %q %v", buf[:n], err)
	}
	<-started
	cancel()
	resp.Body.Close()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("provider ctx %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider context not cancelled after the client went away")
	}
}

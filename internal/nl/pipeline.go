// Package nl answers natural-language questions about a database
// (docs/NL.md): retrieve the relevant schema, make one structured-output
// call, validate it, run the SQL through the Phase 2 guard and executor,
// repair once on a fixable error, and summarise the result. The model
// never runs anything itself.
package nl

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/nl/llm"
	"github.com/dbscope/dbscope/internal/nl/redact"
	"github.com/dbscope/dbscope/internal/nl/retrieve"
	"github.com/dbscope/dbscope/internal/nl/summarize"
	"github.com/dbscope/dbscope/internal/query/exec"
	qguard "github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/store"
)

// Assumption is an assumption chip as the API spells it.
type Assumption struct {
	Label       string `json:"label"`
	SQLFragment string `json:"sqlFragment"`
}

// Clarification is the user's answer to an earlier clarifying question.
type Clarification struct {
	Question       string `json:"question"`
	Answer         string `json:"answer"`
	SaveToGlossary bool   `json:"saveToGlossary"`
}

// Ask is one question (docs/API.md AskRequest).
type Ask struct {
	Question            string         `json:"question"`
	ConversationID      string         `json:"conversationId"`
	AssumptionOverrides []Assumption   `json:"assumptionOverrides"`
	Clarification       *Clarification `json:"clarification"`
	MaxRows             *int           `json:"maxRows"`
	ConfirmCost         bool           `json:"confirmCost"`
	ConfirmRows         bool           `json:"confirmRows"`
}

// Executor runs a guarded statement: SchemaSource.Query bound to a profile
// in the agent, exec.Run on a pool in the eval harness.
type Executor func(ctx context.Context, req exec.Request) (*exec.Result, error)

// Emitter sends one SSE event.
type Emitter func(event string, data any) error

// Env is everything one run needs. Model must already carry the inferred
// foreign keys the user did not reject.
type Env struct {
	Provider llm.Provider
	Model    *schema.Model
	// LoadModel, used when Model is nil, reads the schema after the
	// "retrieving" status is sent, so a slow introspection is visible.
	LoadModel     func(ctx context.Context) (*schema.Model, error)
	Glossary      []store.GlossaryEntry
	Retrieve      retrieve.Options
	Conversations *Conversations
	ProfileID     string
	Exec          Executor
	// Request carries the profile's execution settings (Schemas,
	// StatementTimeout, MaxRows, MaxCost); Parsed and ConfirmCost are set
	// per attempt.
	Request      exec.Request
	SummaryMode  conn.SummaryMode
	MaxRowsToLLM int
	// RowsNeedConfirm is set when rows mode sends rows off a prod-replica
	// profile that the user has not confirmed during this agent run.
	RowsNeedConfirm bool
	// SaveGlossary stores a clarification answer; nil disables saving.
	SaveGlossary func(ctx context.Context, e store.GlossaryEntry) error
	// ErrorBody renders a guard, executor or database error as the API
	// error envelope body; nil uses DefaultErrorBody.
	ErrorBody func(err error) map[string]any
	Emit      Emitter
}

// Outcome reports a finished run for history and the eval harness.
type Outcome struct {
	ConversationID string
	// Output is the last valid model answer, if any.
	Output   *llm.Output
	Repaired bool
	// SQL is the last statement the model proposed ("" for clarify).
	SQL    string
	Result *exec.Result
	// Err is the error that ended the run (also emitted), or nil.
	Err error
	// Confirmation is "cost" or "rows" when the run stopped to ask.
	Confirmation string
	Usage        llm.Usage
	Duration     time.Duration
	// Rejected counts guard rejections of model SQL (repairs included).
	Rejected int
}

// LLMError is a provider failure or unusable output that ended a run.
type LLMError struct {
	Kind    llm.Kind
	Message string
}

func (e *LLMError) Error() string { return e.Message }

// errInvalid is output that failed validation twice.
func errInvalid(problem string) *LLMError {
	return &LLMError{Kind: llm.KindInvalidOutput, Message: "the model's answer was not usable: " + problem}
}

type runner struct {
	env   *Env
	ask   Ask
	state state
	out   Outcome
	start time.Time
	// retrieving is set once the "retrieving" status was sent.
	retrieving bool
}

// Run answers ask, emitting events through env.Emit. It returns when the
// stream should end. A cancelled ctx stops it without emitting more.
func Run(ctx context.Context, env *Env, ask Ask) Outcome {
	r := &runner{env: env, ask: ask, start: time.Now()}
	if env.ErrorBody == nil {
		env.ErrorBody = DefaultErrorBody
	}
	r.state = env.Conversations.load(env.ProfileID, ask.ConversationID)
	r.out.ConversationID = r.state.id
	r.run(ctx)
	r.out.Duration = time.Since(r.start)
	return r.out
}

func (r *runner) emit(event string, data any) bool {
	return r.env.Emit(event, data) == nil
}

func (r *runner) status(stage string) bool {
	return r.emit("status", map[string]string{"stage": stage})
}

func (r *runner) fail(ctx context.Context, err error) {
	r.out.Err = err
	if ctx.Err() != nil {
		return // the client is gone
	}
	var le *LLMError
	if errors.As(err, &le) {
		r.emit("error", map[string]any{"code": "llm", "message": le.Message, "detail": string(le.Kind)})
		return
	}
	r.emit("error", r.env.ErrorBody(err))
}

func llmErr(err error) error {
	var e *llm.Error
	if errors.As(err, &e) {
		return &LLMError{Kind: e.Kind, Message: e.Message}
	}
	return err
}

func (r *runner) sends() string {
	// Profile summaries are computed locally, so only rows mode sends result data.
	if r.env.SummaryMode == conn.SummaryRows {
		return "schema+rows"
	}
	return "schema"
}

func (r *runner) terms() []retrieve.Term {
	out := make([]retrieve.Term, len(r.env.Glossary))
	for i, g := range r.env.Glossary {
		out[i] = retrieve.Term{Term: g.Term, Definition: g.Definition, SQL: g.SQL}
	}
	return out
}

func (r *runner) emitContext(sel retrieve.Result) bool {
	return r.emit("context", map[string]any{
		"provider": r.env.Provider.Name(), "model": r.env.Provider.Model(), "schemaMode": sel.Mode,
		"tables": sel.Tables, "omittedColumns": sel.OmittedColumns, "sends": r.sends(),
	})
}

func (r *runner) emitAnswer(o llm.Output, repaired bool) bool {
	assumptions := make([]Assumption, len(o.Assumptions))
	for i, a := range o.Assumptions {
		assumptions[i] = Assumption{Label: a.Label, SQLFragment: a.SQLFragment}
	}
	body := map[string]any{
		"kind": o.Kind, "sql": o.SQL, "explanationSteps": nonNil(o.ExplanationSteps), "assumptions": assumptions,
		"tables": nonNil(o.Tables), "repaired": repaired,
	}
	if o.Kind == "clarify" {
		body["clarifyingQuestion"] = map[string]any{"question": o.ClarifyingQuestion.Question, "options": nonNil(o.ClarifyingQuestion.Options)}
	}
	return r.emit("answer", body)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r *runner) run(ctx context.Context) {
	if c := r.ask.Clarification; c != nil && c.SaveToGlossary && r.env.SaveGlossary != nil {
		e := store.GlossaryEntry{Term: truncate(c.Question, MaxTermLen), Definition: truncate(c.Answer, MaxDefinitionLen)}
		if err := r.env.SaveGlossary(ctx, e); err != nil && ctx.Err() == nil {
			r.fail(ctx, err)
			return
		}
		r.env.Glossary = upsert(r.env.Glossary, e)
	}

	if r.env.Model == nil {
		if !r.status("retrieving") {
			return
		}
		m, err := r.env.LoadModel(ctx)
		if err != nil {
			r.fail(ctx, err)
			return
		}
		r.env.Model = m
		r.retrieving = true
	}

	user := userMessage(r.ask)
	var sel retrieve.Result
	var output llm.Output
	var raw string
	repaired := false
	if p := r.reusable(); p != nil {
		// A confirmation re-send: run the answer the user already saw.
		sel, output, raw, repaired, user = p.context, p.output, p.raw, p.repaired, p.user
		if !r.emitContext(sel) || !r.emitAnswer(output, repaired) {
			return
		}
	} else {
		if !r.retrieving && !r.status("retrieving") {
			return
		}
		opts := r.env.Retrieve
		opts.Glossary = r.terms()
		for _, t := range r.state.turns {
			opts.Pinned = append(opts.Pinned, t.tables...)
		}
		sel = retrieve.Select(r.env.Model, r.ask.Question, opts)
		if !r.emitContext(sel) || !r.status("generating") {
			return
		}
		var ok bool
		output, raw, ok = r.generate(ctx, sel, r.messages(user))
		if !ok {
			return
		}
		if !r.emitAnswer(output, false) {
			return
		}
	}
	r.out.Output = &output
	r.out.SQL = output.SQL
	if output.Kind == "clarify" {
		r.env.Conversations.addTurn(r.env.ProfileID, r.state.id, turn{user: user, output: raw})
		r.done()
		return
	}

	msgs := r.messages(user)
	for {
		if !r.status("checking") {
			return
		}
		res, err := r.attempt(ctx, output.SQL, sel, user, output, raw, repaired)
		if r.out.Confirmation != "" {
			return
		}
		if err == nil {
			r.out.Result = res
			r.out.Repaired = repaired
			r.env.Conversations.addTurn(r.env.ProfileID, r.state.id, turn{user: user, output: raw, tables: output.Tables})
			if !r.emit("result", res) {
				return
			}
			r.summarize(ctx, res)
			return
		}
		if ctx.Err() != nil {
			r.out.Err = ctx.Err()
			return
		}
		problem, fixable := repairable(err)
		if !fixable || repaired {
			r.fail(ctx, err)
			return
		}
		if !r.status("repairing") {
			return
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Text: raw}, llm.Message{Role: "user", Text: repairMessage(problem)})
		fixed, fixedRaw, gerr := r.call(ctx, sel, msgs)
		if gerr != nil {
			r.fail(ctx, gerr)
			return
		}
		if fixed.Kind != "sql" {
			r.fail(ctx, errInvalid("the repair did not return SQL"))
			return
		}
		output, raw, repaired = fixed, fixedRaw, true
		// Recorded even if the repaired SQL fails too, so evals can tell a failed repair from none.
		r.out.Output, r.out.SQL, r.out.Repaired = &output, output.SQL, true
		if !r.emitAnswer(output, true) {
			return
		}
	}
}

// reusable returns the pending answer when this request confirms it.
func (r *runner) reusable() *pending {
	p := r.state.pending
	if p == nil || p.question != r.ask.Question || (!r.ask.ConfirmCost && !r.ask.ConfirmRows) ||
		r.ask.Clarification != nil || len(r.ask.AssumptionOverrides) > 0 {
		return nil
	}
	return p
}

// messages is the conversation so far plus the new user turn.
func (r *runner) messages(user string) []llm.Message {
	var out []llm.Message
	for _, t := range r.state.turns {
		out = append(out, llm.Message{Role: "user", Text: t.user}, llm.Message{Role: "assistant", Text: t.output})
	}
	return append(out, llm.Message{Role: "user", Text: user})
}

// generate makes the call and, on output that fails parsing or
// validation, one corrective retry.
func (r *runner) generate(ctx context.Context, sel retrieve.Result, msgs []llm.Message) (llm.Output, string, bool) {
	o, raw, err := r.call(ctx, sel, msgs)
	var le *LLMError
	if errors.As(err, &le) && le.Kind == llm.KindInvalidOutput && raw != "" {
		msgs = append(msgs, llm.Message{Role: "assistant", Text: raw}, llm.Message{Role: "user", Text: correctiveMessage(strings.TrimPrefix(le.Message, "the model's answer was not usable: "))})
		o, raw, err = r.call(ctx, sel, msgs)
	}
	if err != nil {
		r.fail(ctx, err)
		return o, raw, false
	}
	return o, raw, true
}

// call makes one generation call and validates the output. Invalid output
// comes back as an invalid_output LLMError together with the raw text.
func (r *runner) call(ctx context.Context, sel retrieve.Result, msgs []llm.Message) (llm.Output, string, error) {
	resp, usage, err := r.env.Provider.Generate(ctx, llm.Request{
		System: SystemPrompt, Context: contextBlock(r.env.Model, sel, r.terms()), Messages: msgs,
	})
	r.out.Usage = r.out.Usage.Add(usage)
	if err != nil {
		if ctx.Err() != nil {
			return llm.Output{}, "", ctx.Err()
		}
		return llm.Output{}, "", llmErr(err)
	}
	o, err := llm.ParseOutput(resp.Text)
	if err != nil {
		return o, resp.Text, errInvalid(err.Error())
	}
	if problem := validate(&o, r.env.Model); problem != "" {
		return o, resp.Text, errInvalid(problem)
	}
	return o, resp.Text, nil
}

// attempt guards and runs sql. It sets r.out.Confirmation (and stores the
// pending answer) when the user has to confirm first.
func (r *runner) attempt(ctx context.Context, sql string, sel retrieve.Result, user string, o llm.Output, raw string, repaired bool) (*exec.Result, error) {
	parsed, err := qguard.Check(sql)
	if err != nil {
		r.out.Rejected++
		return nil, err
	}
	p := &pending{question: r.ask.Question, user: user, output: o, raw: raw, repaired: repaired, context: sel}
	if r.env.RowsNeedConfirm && !r.ask.ConfirmRows {
		r.confirm(p, map[string]any{"reason": "rows"})
		return nil, nil
	}
	if !r.status("running") {
		return nil, context.Canceled
	}
	req := r.env.Request
	req.Parsed, req.ConfirmCost = parsed, r.ask.ConfirmCost
	res, err := r.env.Exec(ctx, req)
	var ce *exec.CostError
	var ge *qguard.Error
	switch {
	case errors.As(err, &ce):
		r.confirm(p, map[string]any{"reason": "cost", "estimatedCost": ce.EstimatedCost, "estimatedRows": ce.EstimatedRows})
		return nil, nil
	case errors.As(err, &ge):
		r.out.Rejected++
	}
	return res, err
}

func (r *runner) confirm(p *pending, body map[string]any) {
	r.out.Confirmation = body["reason"].(string)
	r.env.Conversations.setPending(r.env.ProfileID, r.state.id, p)
	body["conversationId"] = r.state.id
	r.emit("needs_confirmation", body)
}

// repairable reports whether err is worth one repair call, and the
// redacted text the model gets: guard syntax and function rejections and
// errors PostgreSQL raised for the statement itself.
func repairable(err error) (string, bool) {
	var ge *qguard.Error
	if errors.As(err, &ge) {
		if ge.Reason != qguard.Syntax && ge.Reason != qguard.FunctionNotAllowed {
			return "", false
		}
		msg := "rejected by dbscope's safety check (" + string(ge.Reason) + "): " + redact.Literals(ge.Message)
		if ge.Position > 0 {
			msg += " at character " + strconv.Itoa(ge.Position)
		}
		return msg, true
	}
	var de *exec.DBError
	if errors.Is(err, exec.ErrConnect) || !errors.As(err, &de) {
		return "", false
	}
	pe := de.Err
	switch {
	case pe.Severity == "FATAL" || pe.Severity == "PANIC",
		pe.Code == "57014", pe.Code == "40001", pe.Code == "25006",
		strings.HasPrefix(pe.Code, "08"), strings.HasPrefix(pe.Code, "57P"), strings.HasPrefix(pe.Code, "25P"),
		strings.HasPrefix(pe.Code, "53"), strings.HasPrefix(pe.Code, "XX"):
		return "", false
	}
	return redact.PgError(pe, de.Position), true
}

func (r *runner) summarize(ctx context.Context, res *exec.Result) {
	var block, mode string
	switch r.env.SummaryMode {
	case conn.SummaryProfile:
		// A model given only a profile invents the row values it cannot see
		// (observed with qwen2.5-coder:7b: made-up customer names), so the
		// profile summary is written from the profile itself.
		if r.emit("summary", map[string]string{"text": summarize.Describe(summarize.Compute(res)), "mode": "profile"}) {
			r.done()
		}
		return
	case conn.SummaryRows:
		b, _ := json.Marshal(summarize.FirstRows(res, max(r.env.MaxRowsToLLM, 1)))
		block, mode = "<result_rows>\n"+string(b)+"\n</result_rows>", "rows"
	default:
		r.done()
		return
	}
	if !r.status("summarizing") {
		return
	}
	text, usage, err := r.env.Provider.Summarize(ctx, llm.SummaryRequest{
		System: SummarySystem, Prompt: summaryPrompt(r.ask.Question, res.ExecutedSQL, block),
	})
	r.out.Usage = r.out.Usage.Add(usage)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.fail(ctx, llmErr(err))
		return
	}
	if !r.emit("summary", map[string]string{"text": strings.TrimSpace(text), "mode": mode}) {
		return
	}
	r.done()
}

func (r *runner) done() {
	r.emit("done", map[string]any{
		"conversationId": r.state.id,
		"durationMs":     math.Round(float64(time.Since(r.start).Microseconds())/10) / 100,
		"usage":          r.out.Usage,
	})
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n])
	}
	return s
}

func upsert(g []store.GlossaryEntry, e store.GlossaryEntry) []store.GlossaryEntry {
	out := append([]store.GlossaryEntry(nil), g...)
	for i := range out {
		if strings.EqualFold(out[i].Term, e.Term) {
			out[i] = e
			return out
		}
	}
	return append(out, e)
}

// DefaultErrorBody renders errors without the agent's HTTP mapping.
func DefaultErrorBody(err error) map[string]any {
	var ge *qguard.Error
	var de *exec.DBError
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &ge):
		return map[string]any{"code": "rejected", "message": ge.Message, "detail": string(ge.Reason)}
	case errors.As(err, &de):
		return map[string]any{"code": "database", "message": de.Err.Message, "sqlstate": de.Err.Code}
	case errors.As(err, &pe):
		return map[string]any{"code": "database", "message": pe.Message, "sqlstate": pe.Code}
	}
	return map[string]any{"code": "internal", "message": err.Error()}
}

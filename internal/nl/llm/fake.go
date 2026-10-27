package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// Step scripts one Fake.Generate call.
type Step func(ctx context.Context, req Request) (Response, Usage, error)

// Reply is a step that returns text.
func Reply(text string) Step {
	return func(context.Context, Request) (Response, Usage, error) {
		return Response{Text: text}, Usage{InputTokens: 100, OutputTokens: 20}, nil
	}
}

// ReplyOutput is a step that returns o as JSON.
func ReplyOutput(o Output) Step {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err)
	}
	return Reply(string(b))
}

// Fail is a step that returns err.
func Fail(err error) Step {
	return func(context.Context, Request) (Response, Usage, error) { return Response{}, Usage{}, err }
}

// Fake is a scripted Provider for tests: each Generate call consumes the
// next Step and records the request. It never makes a network call.
type Fake struct {
	mu        sync.Mutex
	steps     []Step
	requests  []Request
	summaries []SummaryRequest
	// SummaryText is what Summarize returns; SummaryErr overrides it.
	SummaryText string
	SummaryErr  error
}

// NewFake returns a fake that plays steps in order.
func NewFake(steps ...Step) *Fake { return &Fake{steps: steps, SummaryText: "summary"} }

// Push appends steps.
func (f *Fake) Push(steps ...Step) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, steps...)
}

func (f *Fake) Name() string  { return "fake" }
func (f *Fake) Model() string { return "fake-model" }

// ErrNoStep is returned when Generate is called with no step left.
var ErrNoStep = errors.New("fake: no scripted step left")

func (f *Fake) Generate(ctx context.Context, req Request) (Response, Usage, error) {
	f.mu.Lock()
	f.requests = append(f.requests, cloneRequest(req))
	if len(f.steps) == 0 {
		f.mu.Unlock()
		return Response{}, Usage{}, &Error{Kind: KindOther, Message: ErrNoStep.Error(), Err: ErrNoStep}
	}
	step := f.steps[0]
	f.steps = f.steps[1:]
	f.mu.Unlock()
	return step(ctx, req)
}

func (f *Fake) Summarize(ctx context.Context, req SummaryRequest) (string, Usage, error) {
	f.mu.Lock()
	f.summaries = append(f.summaries, req)
	text, err := f.SummaryText, f.SummaryErr
	f.mu.Unlock()
	if err != nil {
		return "", Usage{}, err
	}
	return text, Usage{InputTokens: 10, OutputTokens: 5}, nil
}

// Requests returns the Generate requests received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Summaries returns the Summarize requests received so far.
func (f *Fake) Summaries() []SummaryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SummaryRequest(nil), f.summaries...)
}

// Remaining is the number of unplayed steps.
func (f *Fake) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.steps)
}

func cloneRequest(r Request) Request {
	r.Messages = append([]Message(nil), r.Messages...)
	return r
}

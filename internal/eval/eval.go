package eval

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/nl"
	"github.com/dbscope/dbscope/internal/nl/llm"
	"github.com/dbscope/dbscope/internal/nl/retrieve"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/schema"
)

// Defaults for a run.
const (
	DefaultStatementTimeout = 30 * time.Second
	// MaxResultRows bounds gold and predicted results; a truncated gold
	// result is reported as an error rather than compared.
	MaxResultRows = 5000
)

// Config is one evaluation run.
type Config struct {
	Dataset  *Dataset
	DB       exec.DB
	Model    *schema.Model
	Provider llm.Provider
	Retrieve retrieve.Options
	// Limit runs only the first Limit questions; 0 runs all.
	Limit            int
	StatementTimeout time.Duration
	// Progress, if set, is called after each question.
	Progress func(QuestionResult)
}

// QuestionResult is one question's outcome.
type QuestionResult struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	// Expected is "sql" or "clarify".
	Expected string `json:"expected"`
	// Kind is the model's answer kind, or "" when it failed.
	Kind      string    `json:"kind"`
	SQL       string    `json:"sql,omitempty"`
	Repaired  bool      `json:"repaired,omitempty"`
	Match     *Match    `json:"match,omitempty"`
	Rejected  int       `json:"rejections"`
	Error     string    `json:"error,omitempty"`
	GoldError string    `json:"goldError,omitempty"`
	LatencyMs int64     `json:"latencyMs"`
	Usage     llm.Usage `json:"usage"`
}

// Metrics summarise a run.
type Metrics struct {
	Questions int `json:"questions"`
	// Answerable is the number of questions with gold SQL.
	Answerable int     `json:"answerable"`
	SetEX      float64 `json:"setEx"`
	MultisetEX float64 `json:"multisetEx"`
	// OrderedMatch is over answerable questions whose gold has a top-level ORDER BY.
	Ordered      int     `json:"orderedQuestions"`
	OrderedMatch float64 `json:"orderedMatch"`
	// Clarify precision: clarify answers on ambiguous questions / clarify
	// answers on ambiguous or answerable questions. Recall: over ambiguous.
	Ambiguous        int     `json:"ambiguous"`
	ClarifyPrecision float64 `json:"clarifyPrecision"`
	ClarifyRecall    float64 `json:"clarifyRecall"`
	// Unanswerable questions answered with clarify (or no SQL).
	Unanswerable int `json:"unanswerable"`
	Abstained    int `json:"abstained"`
	// GuardRejections counts every rejection of model SQL (repairs
	// included); RejectedQuestions those that ended rejected.
	GuardRejections int                `json:"guardRejections"`
	Errors          int                `json:"errors"`
	MedianLatencyMs int64              `json:"medianLatencyMs"`
	Usage           llm.Usage          `json:"usage"`
	SetEXByCategory map[string]float64 `json:"setExByCategory"`
	GoldErrors      int                `json:"goldErrors"`
	GoldErrorIDs    []string           `json:"goldErrorIds,omitempty"`
}

// Report is a whole run.
type Report struct {
	Dataset   string           `json:"dataset"`
	Provider  string           `json:"provider"`
	Model     string           `json:"model"`
	Metrics   Metrics          `json:"metrics"`
	Questions []QuestionResult `json:"questions"`
}

func (c *Config) timeout() time.Duration {
	if c.StatementTimeout > 0 {
		return c.StatementTimeout
	}
	return DefaultStatementTimeout
}

func (c *Config) request(schemas []string) exec.Request {
	return exec.Request{Schemas: schemas, StatementTimeout: c.timeout(), MaxRows: MaxResultRows}
}

// Execute guards and runs one statement exactly as the agent does.
func Execute(ctx context.Context, db exec.DB, sql string, req exec.Request) (*exec.Result, error) {
	parsed, err := guard.Check(sql)
	if err != nil {
		return nil, err
	}
	req.Parsed = parsed
	return exec.Run(ctx, db, req)
}

// GoldResult is a gold query's fingerprint, for recording in questions.yaml.
type GoldResult struct {
	ID       string `json:"id"`
	Rows     int    `json:"rows"`
	Checksum string `json:"checksum"`
	Ordered  bool   `json:"ordered"`
	Error    string `json:"error,omitempty"`
}

// RunGold executes every gold query (and its alternatives) and returns
// their fingerprints. It makes no model call.
func RunGold(ctx context.Context, cfg Config) []GoldResult {
	var out []GoldResult
	for _, q := range cfg.questions() {
		if q.ExpectsClarify() {
			continue
		}
		g := GoldResult{ID: q.ID, Ordered: HasTopLevelOrderBy(q.Gold)}
		res, err := Execute(ctx, cfg.DB, q.Gold, cfg.request(cfg.Dataset.Schemas))
		switch {
		case err != nil:
			g.Error = err.Error()
		case res.Truncated:
			g.Error = fmt.Sprintf("more than %d rows", MaxResultRows)
		default:
			g.Rows, g.Checksum = res.RowCount, Checksum(res.Rows)
			if r := q.Result; r != nil && (r.Rows != g.Rows || r.Checksum != g.Checksum) {
				g.Error = fmt.Sprintf("differs from the recorded result (rows %d, checksum %s)", r.Rows, r.Checksum)
			}
		}
		for i, alt := range q.Alternatives {
			if _, err := Execute(ctx, cfg.DB, alt, cfg.request(cfg.Dataset.Schemas)); err != nil && g.Error == "" {
				g.Error = fmt.Sprintf("alternative %d: %v", i+1, err)
			}
		}
		out = append(out, g)
	}
	return out
}

func (c *Config) questions() []Question {
	qs := c.Dataset.Questions
	if c.Limit > 0 && c.Limit < len(qs) {
		qs = qs[:c.Limit]
	}
	return qs
}

// Run asks every question through the NL pipeline and scores the answers.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if cfg.Provider == nil || cfg.Model == nil || cfg.DB == nil || cfg.Dataset == nil {
		return nil, errors.New("eval: Dataset, DB, Model and Provider are required")
	}
	rep := &Report{Dataset: cfg.Dataset.Name, Provider: cfg.Provider.Name(), Model: cfg.Provider.Model()}
	for _, q := range cfg.questions() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		qr := cfg.one(ctx, q)
		rep.Questions = append(rep.Questions, qr)
		if cfg.Progress != nil {
			cfg.Progress(qr)
		}
	}
	rep.Metrics = score(cfg.Dataset, rep.Questions)
	return rep, nil
}

type goldRun struct {
	cols    int
	res     *exec.Result
	ordered bool
}

func (c *Config) one(ctx context.Context, q Question) QuestionResult {
	qr := QuestionResult{ID: q.ID, Category: q.Category, Expected: "sql"}
	if q.ExpectsClarify() {
		qr.Expected = "clarify"
	}
	var golds []goldRun
	if !q.ExpectsClarify() {
		for i, sql := range append([]string{q.Gold}, q.Alternatives...) {
			res, err := Execute(ctx, c.DB, sql, c.request(c.Dataset.Schemas))
			switch {
			case err != nil:
				qr.GoldError = err.Error()
			case res.Truncated:
				qr.GoldError = fmt.Sprintf("gold has more than %d rows", MaxResultRows)
			case i == 0 && q.Result != nil && (q.Result.Rows != res.RowCount || q.Result.Checksum != Checksum(res.Rows)):
				qr.GoldError = fmt.Sprintf("gold result drifted from the recorded one (%d rows, %s)", res.RowCount, Checksum(res.Rows))
			}
			if qr.GoldError != "" {
				break
			}
			golds = append(golds, goldRun{cols: len(res.Columns), res: res, ordered: HasTopLevelOrderBy(sql)})
		}
		if qr.GoldError != "" {
			return qr // no point asking the model: the reference is broken
		}
	}

	env := &nl.Env{
		Provider: c.Provider, Model: c.Model, Glossary: c.Dataset.Glossary, Retrieve: c.Retrieve,
		Conversations: nl.NewConversations(), ProfileID: "eval",
		Exec:        func(ctx context.Context, req exec.Request) (*exec.Result, error) { return exec.Run(ctx, c.DB, req) },
		Request:     c.request(c.Dataset.Schemas),
		SummaryMode: conn.SummaryOff,
		Emit:        func(string, any) error { return nil },
	}
	start := time.Now()
	out := nl.Run(ctx, env, nl.Ask{Question: q.Question})
	qr.LatencyMs = time.Since(start).Milliseconds()
	qr.Usage, qr.Rejected, qr.Repaired, qr.SQL = out.Usage, out.Rejected, out.Repaired, out.SQL
	if out.Output != nil {
		qr.Kind = out.Output.Kind
	}
	if out.Err != nil {
		qr.Error = out.Err.Error()
	}
	if out.Result != nil && len(golds) > 0 {
		var best Match
		bestRank := -1
		for _, g := range golds {
			m := Compare(g.cols, len(out.Result.Columns), g.res.Rows, out.Result.Rows, g.ordered)
			if r := rank(m); r > bestRank {
				best, bestRank = m, r
			}
		}
		if out.Result.Truncated {
			best = Match{}
			if golds[0].ordered {
				best.Ordered = new(bool)
			}
			qr.Error = fmt.Sprintf("prediction has more than %d rows", MaxResultRows)
		}
		qr.Match = &best
	}
	return qr
}

// rank orders matches: a full match beats a multiset match beats a set match.
func rank(m Match) int {
	r := 0
	if m.Set {
		r++
	}
	if m.Multiset {
		r++
	}
	if m.Ordered == nil || *m.Ordered {
		r++
	}
	return r
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func score(ds *Dataset, qs []QuestionResult) Metrics {
	m := Metrics{Questions: len(qs), SetEXByCategory: map[string]float64{}}
	byID := map[string]Question{}
	for _, q := range ds.Questions {
		byID[q.ID] = q
	}
	var set, multi, ordered, orderedOK, clarifyAmb, clarifyAll int
	catTotal, catOK := map[string]int{}, map[string]int{}
	var latencies []int64
	for _, r := range qs {
		q := byID[r.ID]
		latencies = append(latencies, r.LatencyMs)
		m.Usage = m.Usage.Add(r.Usage)
		m.GuardRejections += r.Rejected
		if r.Error != "" {
			m.Errors++
		}
		if r.GoldError != "" {
			m.GoldErrors++
			m.GoldErrorIDs = append(m.GoldErrorIDs, r.ID)
		}
		clarified := r.Kind == "clarify"
		switch {
		case q.Unanswerable:
			m.Unanswerable++
			if clarified || r.SQL == "" {
				m.Abstained++
			}
			continue
		case q.Ambiguous:
			m.Ambiguous++
			if clarified {
				clarifyAmb++
				clarifyAll++
			}
			continue
		}
		m.Answerable++
		catTotal[r.Category]++
		if clarified {
			clarifyAll++
		}
		if r.Match != nil {
			if r.Match.Set {
				set++
				catOK[r.Category]++
			}
			if r.Match.Multiset {
				multi++
			}
		}
		if q.Gold != "" && HasTopLevelOrderBy(q.Gold) {
			ordered++
			if r.Match != nil && r.Match.Ordered != nil && *r.Match.Ordered {
				orderedOK++
			}
		}
	}
	m.SetEX, m.MultisetEX = ratio(set, m.Answerable), ratio(multi, m.Answerable)
	m.Ordered, m.OrderedMatch = ordered, ratio(orderedOK, ordered)
	m.ClarifyPrecision, m.ClarifyRecall = ratio(clarifyAmb, clarifyAll), ratio(clarifyAmb, m.Ambiguous)
	for c, n := range catTotal {
		m.SetEXByCategory[c] = ratio(catOK[c], n)
	}
	if len(latencies) > 0 {
		slices.Sort(latencies)
		m.MedianLatencyMs = latencies[len(latencies)/2]
		if len(latencies)%2 == 0 {
			m.MedianLatencyMs = (latencies[len(latencies)/2-1] + latencies[len(latencies)/2]) / 2
		}
	}
	return m
}

// Categories returns the category names in a report, sorted.
func (m Metrics) Categories() []string {
	var out []string
	for c := range m.SetEXByCategory {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

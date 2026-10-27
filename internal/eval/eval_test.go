package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/infer"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/nl/llm"
	"github.com/dbscope/dbscope/internal/nl/retrieve"
	"github.com/dbscope/dbscope/internal/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func rows(vals ...[]string) [][]json.RawMessage {
	out := make([][]json.RawMessage, len(vals))
	for i, r := range vals {
		out[i] = make([]json.RawMessage, len(r))
		for j, v := range r {
			out[i][j] = json.RawMessage(v)
		}
	}
	return out
}

func r(v ...string) []string { return v }

func TestNormalization(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{`1`, `1.0`}, {`"12.50"`, `12.5`}, {`0.1`, `0.10000000001`}, {`"1e3"`, `1000`}, {`-0`, `0`},
		{`"NaN"`, `"NaN"`}, {`{"b":1,"a":2}`, `{"a":2,"b":1}`}, {`true`, `true`}, {`null`, `null`},
	} {
		if normValue(json.RawMessage(c.a)) != normValue(json.RawMessage(c.b)) {
			t.Errorf("%s != %s (%s vs %s)", c.a, c.b, normValue(json.RawMessage(c.a)), normValue(json.RawMessage(c.b)))
		}
	}
	for _, c := range []struct{ a, b string }{
		{`null`, `"null"`}, {`null`, `""`}, {`null`, `0`}, {`"abc"`, `"ABC"`}, {`1`, `1.001`}, {`true`, `"t"`},
		{`"0x1A"`, `26`}, {`"2026-01-01"`, `"2026-01-02"`},
	} {
		if normValue(json.RawMessage(c.a)) == normValue(json.RawMessage(c.b)) {
			t.Errorf("%s == %s", c.a, c.b)
		}
	}
}

func TestCompare(t *testing.T) {
	gold := rows(r(`1`, `"a"`), r(`2`, `"b"`), r(`2`, `"b"`))
	for _, c := range []struct {
		name                 string
		pred                 [][]json.RawMessage
		predCols             int
		ordered              bool
		set, multiset, order bool
	}{
		{"identical", gold, 2, true, true, true, true},
		{"reordered rows", rows(r(`2`, `"b"`), r(`1`, `"a"`), r(`2`, `"b"`)), 2, true, true, true, false},
		{"reordered rows, gold unordered", rows(r(`2`, `"b"`), r(`1`, `"a"`), r(`2`, `"b"`)), 2, false, true, true, false},
		{"swapped columns", rows(r(`"a"`, `1`), r(`"b"`, `2`), r(`"b"`, `2`)), 2, true, true, true, true},
		{"numeric as string", rows(r(`"1.00"`, `"a"`), r(`"2"`, `"b"`), r(`2.0`, `"b"`)), 2, true, true, true, true},
		{"duplicates lost", rows(r(`1`, `"a"`), r(`2`, `"b"`)), 2, true, true, false, false},
		{"extra column", rows(r(`1`, `"a"`, `0`), r(`2`, `"b"`, `0`), r(`2`, `"b"`, `0`)), 3, false, false, false, false},
		{"wrong value", rows(r(`1`, `"a"`), r(`2`, `"b"`), r(`3`, `"b"`)), 2, false, false, false, false},
		{"null vs empty", rows(r(`1`, `"a"`), r(`2`, `"b"`), r(`2`, `null`)), 2, false, false, false, false},
		{"empty", nil, 2, false, false, false, false},
	} {
		m := Compare(2, c.predCols, gold, c.pred, c.ordered)
		if m.Set != c.set || m.Multiset != c.multiset || (m.Ordered != nil) != c.ordered || (m.Ordered != nil && *m.Ordered != c.order) {
			t.Errorf("%s: %+v (ordered %v)", c.name, m, m.Ordered != nil && *m.Ordered)
		}
	}
	// Two empty results match whatever their columns.
	if m := Compare(1, 2, nil, nil, false); !m.Set || !m.Multiset {
		t.Fatalf("empty vs empty %+v", m)
	}
	// NULLs compare equal to NULLs.
	if m := Compare(1, 1, rows(r(`null`), r(`1`)), rows(r(`1`), r(`null`)), false); !m.Multiset {
		t.Fatalf("nulls %+v", m)
	}
	if Checksum(gold) != Checksum(rows(r(`2`, `"b"`), r(`"1.0"`, `"a"`), r(`2`, `"b"`))) || Checksum(gold) == Checksum(gold[:2]) {
		t.Fatal("checksum")
	}
}

func TestHasTopLevelOrderBy(t *testing.T) {
	for sql, want := range map[string]bool{
		"SELECT 1 ORDER BY 1":                                  true,
		"SELECT * FROM (SELECT 1 AS a ORDER BY 1) s":           false,
		"SELECT 1 UNION SELECT 2 ORDER BY 1":                   true,
		"WITH x AS (SELECT 1 AS a ORDER BY a) SELECT * FROM x": false,
		"SELECT count(*) FROM t":                               false,
		"not sql":                                              false,
	} {
		if got := HasTopLevelOrderBy(sql); got != want {
			t.Errorf("%s: %v", sql, got)
		}
	}
}

// byQuestion answers each question with a scripted output, keyed by the
// question text in the last user message.
type byQuestion struct {
	mu      sync.Mutex
	answers map[string]llm.Output
}

func (b *byQuestion) Name() string  { return "scripted" }
func (b *byQuestion) Model() string { return "scripted-1" }
func (b *byQuestion) Summarize(context.Context, llm.SummaryRequest) (string, llm.Usage, error) {
	return "", llm.Usage{}, nil
}
func (b *byQuestion) Generate(_ context.Context, req llm.Request) (llm.Response, llm.Usage, error) {
	q := strings.TrimPrefix(req.Messages[0].Text, "Question: ")
	b.mu.Lock()
	o, ok := b.answers[q]
	b.mu.Unlock()
	if !ok {
		return llm.Response{}, llm.Usage{}, &llm.Error{Kind: llm.KindOther, Message: "no answer for " + q}
	}
	js, _ := json.Marshal(o)
	return llm.Response{Text: string(js)}, llm.Usage{InputTokens: 50, OutputTokens: 10}, nil
}

func sqlOut(sql string) llm.Output {
	return llm.Output{Kind: "sql", SQL: sql, ExplanationSteps: []string{}, Assumptions: []llm.Assumption{}, Tables: []string{},
		ClarifyingQuestion: llm.ClarifyingQuestion{Options: []string{}}}
}

func clarifyOut() llm.Output {
	return llm.Output{Kind: "clarify", ExplanationSteps: []string{}, Assumptions: []llm.Assumption{}, Tables: []string{},
		ClarifyingQuestion: llm.ClarifyingQuestion{Question: "Which one?", Options: []string{"A", "B"}}}
}

const harnessFixture = `
CREATE SCHEMA shop;
CREATE TABLE shop.items (id int PRIMARY KEY, name text NOT NULL, price numeric(8,2), score float8, category text);
INSERT INTO shop.items VALUES (1, 'pen', 1.50, 0.1, 'office'), (2, 'ink', 7.25, 0.30000000000000004, 'office'),
  (3, 'mug', 4.00, NULL, 'kitchen'), (4, 'cup', 4.00, 0.5, NULL);
`

const harnessDataset = `
name: tiny
schemas: [shop]
questions:
  - id: q1
    category: aggregates
    question: How many items are there?
    gold: SELECT count(*) FROM shop.items
  - id: q2
    category: ordering
    question: Items by price, most expensive first
    gold: SELECT name, price FROM shop.items ORDER BY price DESC, name
  - id: q3
    category: nulls
    question: Items without a category
    gold: SELECT name FROM shop.items WHERE category IS NULL
  - id: q4
    category: floats
    question: Total score
    gold: SELECT sum(score) FROM shop.items
  - id: q5
    category: distinct
    question: Distinct prices
    gold: SELECT DISTINCT price FROM shop.items
  - id: q6
    category: ambiguous
    question: Best items
    ambiguous: true
  - id: q7
    category: unanswerable
    question: Supplier phone numbers
    unanswerable: true
`

func TestHarnessEndToEnd(t *testing.T) {
	srv := pgtest.Servers(t, "18")[0]
	d := srv.NewDB(t)
	d.Exec(t, harnessFixture)
	p, pw := d.Superuser(t)
	pool, err := conn.NewPool(context.Background(), p, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m, err := introspect.Introspect(context.Background(), pool, []string{"shop"}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	infer.Apply(m, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "questions.yaml"), []byte(harnessDataset), 0o600); err != nil {
		t.Fatal(err)
	}
	ds, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dataset: ds, DB: pool, Model: m, Retrieve: retrieve.AnthropicOptions()}

	gold := RunGold(context.Background(), cfg)
	if len(gold) != 5 || gold[0].Rows != 1 || gold[1].Rows != 4 || !gold[1].Ordered || gold[2].Rows != 1 || gold[4].Rows != 3 {
		t.Fatalf("gold %+v", gold)
	}
	for _, g := range gold {
		if g.Error != "" || len(g.Checksum) != 16 {
			t.Fatalf("gold %+v", g)
		}
	}

	t.Run("perfect", func(t *testing.T) {
		answers := map[string]llm.Output{}
		for _, q := range ds.Questions {
			if q.ExpectsClarify() {
				answers[q.Question] = clarifyOut()
			} else {
				answers[q.Question] = sqlOut(q.Gold)
			}
		}
		// Equivalent rewrites still match: aliases, column order, float sums in another order.
		answers["How many items are there?"] = sqlOut("SELECT count(id) AS n FROM shop.items")
		answers["Items by price, most expensive first"] = sqlOut("SELECT price AS p, name FROM shop.items ORDER BY price DESC, name")
		answers["Total score"] = sqlOut("SELECT sum(score) FROM (SELECT score FROM shop.items ORDER BY id DESC) s")
		cfg := cfg
		cfg.Provider = &byQuestion{answers: answers}
		rep, err := Run(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		mt := rep.Metrics
		if mt.SetEX != 1 || mt.MultisetEX != 1 || mt.Ordered != 1 || mt.OrderedMatch != 1 || mt.ClarifyPrecision != 1 ||
			mt.ClarifyRecall != 1 || mt.Abstained != 1 || mt.Answerable != 5 || mt.Ambiguous != 1 || mt.Errors != 0 || mt.GoldErrors != 0 {
			t.Fatalf("perfect metrics %+v\n%+v", mt, rep.Questions)
		}
		if mt.Usage.InputTokens != 7*50 || mt.SetEXByCategory["nulls"] != 1 {
			t.Fatalf("usage/categories %+v", mt)
		}
	})

	t.Run("wrong", func(t *testing.T) {
		cfg := cfg
		cfg.Provider = &byQuestion{answers: map[string]llm.Output{
			"How many items are there?":            sqlOut("SELECT count(*) + 1 FROM shop.items"),
			"Items by price, most expensive first": sqlOut("SELECT name, price FROM shop.items ORDER BY price ASC, name"),
			"Items without a category":             sqlOut("SELECT name FROM shop.items WHERE category = ''"),
			"Total score":                          sqlOut("SELECT sum(coalesce(score, 1)) FROM shop.items"),
			"Distinct prices":                      sqlOut("SELECT price FROM shop.items"),
			"Best items":                           sqlOut("SELECT name FROM shop.items ORDER BY score DESC LIMIT 1"),
			"Supplier phone numbers":               sqlOut("SELECT pg_sleep(1)"),
		}}
		rep, err := Run(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		mt := rep.Metrics
		// q2 matches as a set and multiset but not in order; q5 matches as a set only.
		if mt.SetEX != 2.0/5 || mt.MultisetEX != 1.0/5 || mt.OrderedMatch != 0 || mt.ClarifyRecall != 0 || mt.ClarifyPrecision != 0 ||
			mt.Abstained != 0 || mt.GuardRejections != 2 {
			t.Fatalf("wrong metrics %+v\n%+v", mt, rep.Questions)
		}
		q2 := rep.Questions[1]
		if !q2.Match.Set || !q2.Match.Multiset || *q2.Match.Ordered {
			t.Fatalf("q2 %+v", q2.Match)
		}
	})

	t.Run("all wrong", func(t *testing.T) {
		cfg := cfg
		cfg.Provider = &byQuestion{answers: map[string]llm.Output{}}
		cfg.Limit = 5
		rep, err := Run(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Metrics.SetEX != 0 || rep.Metrics.Questions != 5 || rep.Metrics.Errors != 5 {
			t.Fatalf("%+v", rep.Metrics)
		}
	})

	t.Run("drift", func(t *testing.T) {
		ds2 := *ds
		ds2.Questions = append([]Question(nil), ds.Questions[:1]...)
		ds2.Questions[0].Result = &Recorded{Rows: 1, Checksum: "0000000000000000"}
		cfg := cfg
		cfg.Dataset = &ds2
		cfg.Provider = &byQuestion{answers: map[string]llm.Output{}}
		rep, _ := Run(context.Background(), cfg)
		if rep.Metrics.GoldErrors != 1 || !strings.Contains(rep.Questions[0].GoldError, "drifted") {
			t.Fatalf("%+v", rep.Questions[0])
		}
	})
}

func TestLoadValidation(t *testing.T) {
	for _, bad := range []string{
		"questions:\n  - id: a\n    question: q\n",
		"questions:\n  - id: a\n    question: q\n    gold: SELECT 1\n    ambiguous: true\n",
		"questions:\n  - id: a\n    question: q\n    gold: SELECT 1\n  - id: a\n    question: r\n    gold: SELECT 1\n",
		"questions:\n  - id: a\n    question: q\n    gold: SELECT 1\n    typo: 1\n",
	} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "questions.yaml"), []byte(bad), 0o600)
		if _, err := Load(dir); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "questions.yaml"), []byte("name: x\nquestions: []\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "glossary.yaml"), []byte("glossary:\n  - term: film\n    definition: a movie\n    sql: t_flm\n"), 0o600)
	ds, err := Load(dir)
	if err != nil || len(ds.Glossary) != 1 || ds.Glossary[0].SQL != "t_flm" {
		t.Fatalf("%+v %v", ds, err)
	}
}

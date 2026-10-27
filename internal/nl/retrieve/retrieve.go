package retrieve

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dbscope/dbscope/internal/schema"
)

// Term is a glossary entry as retrieval sees it.
type Term struct {
	Term       string
	Definition string
	SQL        string
}

// Budgets per provider (docs/NL.md "Retrieval").
const (
	AnthropicFullBudget = 40000 // estimated tokens (chars/4)
	AnthropicTopK       = 20
	LocalTopK           = 10
	maxBridgeHops       = 4
	prefixWeight        = 0.5
)

// Options control selection.
type Options struct {
	// FullBudget sends the whole schema when its rendering is at most this
	// many estimated tokens; 0 never does.
	FullBudget int
	// TopK is how many relations BM25 seeds when the schema is not sent whole.
	TopK int
	// Pinned relations ("schema.name") are always seeds, e.g. the tables of
	// earlier turns in a conversation.
	Pinned   []string
	Glossary []Term
}

// AnthropicOptions and LocalOptions are the documented budgets.
func AnthropicOptions() Options { return Options{FullBudget: AnthropicFullBudget, TopK: AnthropicTopK} }
func LocalOptions() Options     { return Options{TopK: LocalTopK} }

// Result is what goes into the prompt.
type Result struct {
	// Mode is "full" or "retrieved".
	Mode string
	// Tables are the relations rendered in full, "schema.name", in model order.
	Tables []string
	// OmittedColumns are sensitive columns left out, "schema.table.column".
	OmittedColumns []string
	// DDL is the labelled data block.
	DDL string
	// EstimatedTokens is len(DDL in characters)/4.
	EstimatedTokens int
}

// Select chooses relations for question and renders them. The model must
// already carry the inferred foreign keys the user did not reject.
func Select(m *schema.Model, question string, opts Options) Result {
	if opts.FullBudget > 0 {
		all := make([]int, 0, len(m.Relations))
		for i := range m.Relations {
			if !m.Relations[i].Stub {
				all = append(all, i)
			}
		}
		r := render(m, all)
		if r.EstimatedTokens <= opts.FullBudget {
			r.Mode = "full"
			return r
		}
	}
	r := render(m, pick(m, question, opts))
	r.Mode = "retrieved"
	return r
}

// Rank returns relation names ordered by BM25 score for question, best
// first, leaving out relations that score zero.
func Rank(m *schema.Model, question string, glossary []Term) []string {
	idx, scores := rank(m, question, glossary)
	var out []string
	for _, i := range idx {
		if scores[i] > 0 {
			out = append(out, m.Relations[i].QName().String())
		}
	}
	return out
}

// rank scores every relation; stubs never score. idx is sorted by score,
// ties in model order.
func rank(m *schema.Model, question string, glossary []Term) ([]int, []float64) {
	enums := enumLabels(m)
	docs := make([][]string, len(m.Relations))
	for i := range m.Relations {
		if !m.Relations[i].Stub {
			docs[i] = document(&m.Relations[i], glossary, enums)
		}
	}
	ix := newBM25(docs)
	query := map[string]float64{}
	qtoks := queryTokens(question)
	for _, t := range qtoks {
		query[t] = 1
	}
	// Glossary terms in the question pull in their definition and SQL.
	set := map[string]bool{}
	for _, t := range qtoks {
		set[t] = true
	}
	for _, g := range glossary {
		tt := queryTokens(g.Term)
		if len(tt) == 0 || !all(tt, set) {
			continue
		}
		for _, t := range queryTokens(g.Definition + " " + g.SQL) {
			if _, ok := query[t]; !ok {
				query[t] = 1
			}
		}
	}
	// Cryptic identifiers: an indexed term of 3+ characters that prefixes
	// a question word counts at half weight.
	for _, v := range ix.vocabulary() {
		if len(v) < 3 {
			continue
		}
		if _, ok := query[v]; ok {
			continue
		}
		for _, t := range qtoks {
			if len(t) > len(v) && strings.HasPrefix(t, v) {
				query[v] = prefixWeight
				break
			}
		}
	}
	scores := ix.score(query)
	idx := make([]int, len(m.Relations))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		switch {
		case scores[a] > scores[b]:
			return -1
		case scores[a] < scores[b]:
			return 1
		}
		return 0
	})
	return idx, scores
}

func all(ts []string, set map[string]bool) bool {
	for _, t := range ts {
		if !set[t] {
			return false
		}
	}
	return true
}

var identChain = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)*`)

// enumLabels maps an enum type, as format_type may print it (qualified or
// not), to its labels' tokens.
func enumLabels(m *schema.Model) map[string][]string {
	out := map[string][]string{}
	for _, e := range m.Enums {
		var toks []string
		for _, v := range e.Values {
			toks = append(toks, Tokenize(v)...)
		}
		out[e.Schema+"."+e.Name] = toks
		if _, taken := out[e.Name]; !taken {
			out[e.Name] = toks
		}
	}
	return out
}

// document is the BM25 text of a relation: name ×3, schema, comment,
// columns ×2 with their comments and enum labels, FK targets, and glossary
// terms that mention the relation. Sensitive columns are left out.
func document(r *schema.Relation, glossary []Term, enums map[string][]string) []string {
	var d []string
	name := Tokenize(r.Name)
	d = append(d, name...)
	d = append(d, name...)
	d = append(d, name...)
	d = append(d, Tokenize(r.Schema)...)
	d = append(d, Tokenize(r.Comment)...)
	for _, c := range r.Columns {
		if Sensitive(c.Name) {
			continue
		}
		ct := Tokenize(c.Name)
		d = append(d, ct...)
		d = append(d, ct...)
		d = append(d, Tokenize(c.Comment)...)
		d = append(d, enums[strings.TrimSuffix(c.Type, "[]")]...)
	}
	for _, fk := range r.ForeignKeys {
		d = append(d, Tokenize(fk.Ref.Name)...)
	}
	lname := strings.ToLower(r.Name)
	for _, g := range glossary {
		for _, chain := range identChain.FindAllString(g.Definition+" "+g.SQL, -1) {
			if slices.Contains(strings.Split(strings.ToLower(chain), "."), lname) {
				d = append(d, Tokenize(g.Term)...)
				break
			}
		}
	}
	return d
}

// pick returns the indexes of the relations to render, in model order:
// pinned and BM25 seeds, then bridge tables on shortest FK paths between
// seeds, then 1-hop FK neighbours, up to twice the seed count in total.
func pick(m *schema.Model, question string, opts Options) []int {
	byName := map[string]int{}
	for i := range m.Relations {
		byName[m.Relations[i].QName().String()] = i
	}
	chosen := map[int]bool{}
	var seeds []int
	add := func(i int) {
		if !chosen[i] && !m.Relations[i].Stub {
			chosen[i] = true
			seeds = append(seeds, i)
		}
	}
	for _, p := range opts.Pinned {
		if i, ok := byName[p]; ok {
			add(i)
		}
	}
	idx, scores := rank(m, question, opts.Glossary)
	for _, i := range idx {
		if len(seeds) >= max(opts.TopK, len(opts.Pinned)) || scores[i] <= 0 {
			break
		}
		add(i)
	}
	if len(seeds) == 0 {
		// Nothing matched: fall back to the first relations in model order.
		for i := 0; i < len(m.Relations) && len(seeds) < opts.TopK; i++ {
			add(i)
		}
	}
	limit := 2 * len(seeds)
	adj := adjacency(m)
	selected := slices.Clone(seeds)
	include := func(i int) {
		if len(selected) < limit && !chosen[i] {
			chosen[i] = true
			selected = append(selected, i)
		}
	}
	for a := 0; a < len(seeds); a++ {
		for b := a + 1; b < len(seeds); b++ {
			path := shortestPath(adj, seeds[a], seeds[b])
			if len(path) > 2 {
				for _, i := range path[1 : len(path)-1] {
					include(i)
				}
			}
		}
	}
	for _, s := range seeds {
		for _, n := range adj[s] {
			include(n)
		}
	}
	slices.Sort(selected)
	return selected
}

// adjacency links relations joined by a declared or inferred foreign key,
// in both directions, ignoring stubs. Lists are sorted.
func adjacency(m *schema.Model) map[int][]int {
	byName := map[schema.QualifiedName]int{}
	for i := range m.Relations {
		byName[m.Relations[i].QName()] = i
	}
	set := map[int]map[int]bool{}
	link := func(a, b int) {
		if set[a] == nil {
			set[a] = map[int]bool{}
		}
		set[a][b] = true
	}
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Stub {
			continue
		}
		for _, fk := range r.ForeignKeys {
			j, ok := byName[fk.Ref]
			if !ok || j == i || m.Relations[j].Stub {
				continue
			}
			link(i, j)
			link(j, i)
		}
	}
	out := map[int][]int{}
	for a, ns := range set {
		for n := range ns {
			out[a] = append(out[a], n)
		}
		slices.Sort(out[a])
	}
	return out
}

// shortestPath is a BFS from a to b over at most maxBridgeHops edges; nil
// when there is none. Neighbour order is fixed, so the path is too.
func shortestPath(adj map[int][]int, a, b int) []int {
	prev := map[int]int{a: -1}
	frontier := []int{a}
	for hop := 0; hop < maxBridgeHops && len(frontier) > 0; hop++ {
		var next []int
		for _, n := range frontier {
			for _, x := range adj[n] {
				if _, seen := prev[x]; seen {
					continue
				}
				prev[x] = n
				if x == b {
					var path []int
					for c := b; c != -1; c = prev[c] {
						path = append(path, c)
					}
					slices.Reverse(path)
					return path
				}
				next = append(next, x)
			}
		}
		frontier = next
	}
	return nil
}

var sensitive = regexp.MustCompile(`(?i)password|passwd|secret|token|api_?key|ssn|credit_?card|card_number|cvv|salt|hash`)

// Sensitive reports whether a column is left out of prompts by name.
func Sensitive(column string) bool { return sensitive.MatchString(column) }

// estimate is the documented token estimate: characters / 4.
func estimate(s string) int { return utf8.RuneCountInString(s) / 4 }

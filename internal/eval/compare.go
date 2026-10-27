package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	pgquery "github.com/wasilibs/go-pgquery"
)

// floatSignificant is the number of significant digits numbers are
// rounded to before comparing (a relative tolerance of about 1e-9, well
// inside the 1e-6 the research notes suggest).
const floatSignificant = 9

// nullToken stands for SQL NULL in a normalised row; it cannot collide with
// a JSON-encoded string, which always starts with a quote.
const nullToken = "\x00NULL"

// normValue canonicalises one executor value: NULL as nullToken, numbers
// (including numeric, which arrives as a string) rounded to 9 significant
// digits, booleans as true/false, everything else as its JSON text.
// Timestamps are already UTC: the executor pins TimeZone.
func normValue(v json.RawMessage) string {
	s := strings.TrimSpace(string(v))
	if s == "null" || s == "" {
		return nullToken
	}
	if s[0] == '"' {
		var str string
		if json.Unmarshal(v, &str) == nil {
			if f, err := strconv.ParseFloat(str, 64); err == nil && looksNumeric(str) {
				return numToken(f)
			}
			b, _ := json.Marshal(str)
			return string(b)
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return numToken(f)
	}
	var doc any
	if json.Unmarshal(v, &doc) == nil {
		b, _ := json.Marshal(doc) // canonical JSON for json/jsonb (sorted keys)
		return string(b)
	}
	return s
}

// looksNumeric rejects strings ParseFloat accepts but that are not SQL
// numbers in a result (hex, "inf" words are kept: NaN/Infinity are numeric).
func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	switch s {
	case "NaN", "Infinity", "-Infinity":
		return true
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r == '.' || r == '-' || r == '+' || r == 'e' || r == 'E') {
			return false
		}
	}
	return true
}

func numToken(f float64) string {
	switch {
	case math.IsNaN(f):
		return "n:NaN"
	case math.IsInf(f, 1):
		return "n:Inf"
	case math.IsInf(f, -1):
		return "n:-Inf"
	case f == 0:
		return "n:0"
	}
	return "n:" + strconv.FormatFloat(f, 'g', floatSignificant, 64)
}

// table is a normalised result: rows of canonical value tokens.
type table [][]string

func normalize(rows [][]json.RawMessage) table {
	out := make(table, len(rows))
	for i, r := range rows {
		out[i] = make([]string, len(r))
		for j, v := range r {
			out[i][j] = normValue(v)
		}
	}
	return out
}

func rowKey(r []string) string { return strings.Join(r, "\x1f") }

// alignColumns reorders pred's columns to match gold's when the two have
// the same width and every gold column has a pred column with the same
// multiset of values (column order and aliases do not matter). It returns
// pred unchanged when no full alignment exists.
func alignColumns(gold, pred table, width int) table {
	if len(gold) == 0 || len(pred) == 0 || width < 2 {
		return pred
	}
	sig := func(t table, col int) string {
		vals := make([]string, len(t))
		for i, r := range t {
			vals[i] = r[col]
		}
		slices.Sort(vals)
		return strings.Join(vals, "\x1f")
	}
	gs, ps := make([]string, width), make([]string, width)
	for c := range width {
		gs[c], ps[c] = sig(gold, c), sig(pred, c)
	}
	perm := make([]int, width)
	used := make([]bool, width)
	for g := range width {
		perm[g] = -1
		if ps[g] == gs[g] && !used[g] { // prefer the same position
			perm[g], used[g] = g, true
			continue
		}
		for p := range width {
			if !used[p] && ps[p] == gs[g] {
				perm[g], used[p] = p, true
				break
			}
		}
		if perm[g] < 0 {
			return pred
		}
	}
	out := make(table, len(pred))
	for i, r := range pred {
		out[i] = make([]string, width)
		for g, p := range perm {
			out[i][g] = r[p]
		}
	}
	return out
}

// Match is how a prediction compares with gold.
type Match struct {
	Set      bool `json:"set"`
	Multiset bool `json:"multiset"`
	// Ordered is set only when gold has a top-level ORDER BY.
	Ordered *bool `json:"ordered,omitempty"`
}

// Compare compares two executor results. ordered says gold has a
// top-level ORDER BY.
func Compare(goldCols, predCols int, gold, pred [][]json.RawMessage, ordered bool) Match {
	var m Match
	if goldCols != predCols && !(len(gold) == 0 && len(pred) == 0) {
		if ordered {
			m.Ordered = new(bool)
		}
		return m
	}
	g := normalize(gold)
	p := alignColumns(g, normalize(pred), goldCols)
	gk, pk := make([]string, len(g)), make([]string, len(p))
	for i, r := range g {
		gk[i] = rowKey(r)
	}
	for i, r := range p {
		pk[i] = rowKey(r)
	}
	if ordered {
		eq := slices.Equal(gk, pk)
		m.Ordered = &eq
	}
	gs, ps := slices.Clone(gk), slices.Clone(pk)
	slices.Sort(gs)
	slices.Sort(ps)
	m.Multiset = slices.Equal(gs, ps)
	m.Set = slices.Equal(slices.Compact(gs), slices.Compact(ps))
	return m
}

// Checksum fingerprints a result independently of row order: the first 16
// hex digits of the SHA-256 of the sorted normalised rows.
func Checksum(rows [][]json.RawMessage) string {
	t := normalize(rows)
	keys := make([]string, len(t))
	for i, r := range t {
		keys[i] = rowKey(r)
	}
	slices.Sort(keys)
	h := sha256.Sum256([]byte(strings.Join(keys, "\x1e")))
	return hex.EncodeToString(h[:8])
}

// HasTopLevelOrderBy reports whether the statement's outermost SELECT
// (including a set operation's) has an ORDER BY.
func HasTopLevelOrderBy(sql string) bool {
	tree, err := pgquery.Parse(sql)
	if err != nil || len(tree.Stmts) != 1 {
		return false
	}
	s := tree.Stmts[0].GetStmt().GetSelectStmt()
	return s != nil && len(s.SortClause) > 0
}

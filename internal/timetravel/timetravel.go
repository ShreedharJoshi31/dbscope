// Package timetravel rewrites a read-only query so that every base table it
// reads is read as of one past instant, using the engine's own clause
// (docs/TIME.md). The rewrite runs before the engine's Check, which then
// judges the rewritten text; it only inserts clause text built from the
// validated time, after each table name, and never moves user text.
//
// Each engine supplies its tokens (from its own tokenizer), its dialect and
// what its catalog says about each referenced name; the walk over FROM and
// JOIN is shared.
package timetravel

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
)

// Error is a refused AS OF request; the agent sends it as a 400.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func refuse(format string, a ...any) error { return &Error{Message: fmt.Sprintf(format, a...)} }

// Refuse is a refused request with a message for the user.
func Refuse(format string, a ...any) error { return refuse(format, a...) }

// Token is one token from an engine's tokenizer.
type Token struct {
	Text string
	// Word is the lower-cased text of a bare word; empty for strings, quoted names and symbols.
	Word string
	Pos  int
}

// Part is one part of a qualified name.
type Part struct {
	Name   string // without quotes
	Quoted bool
}

// Ref is one reference to a base table: its name as written and the byte
// offset just past the name, where the clause goes.
type Ref struct {
	Parts []Part
	Text  string
	End   int
}

// Dialect is what the walk needs to know about an engine's SQL.
type Dialect struct {
	// Ident returns the identifier a quoted-name token holds; ok is false for strings.
	Ident func(text string) (name string, ok bool)
	// DottedWords is true when the tokenizer keeps "schema.table" as one word (Oracle).
	DottedWords bool
	// NotTables are one-part names a FROM may read that are not tables (DUAL).
	NotTables map[string]bool
	// ReadsTable are functions that read a named table inside their arguments
	// (Oracle ONLY and CONTAINERS, SQL Server CONTAINSTABLE): a call to one is refused.
	ReadsTable map[string]bool
	// AfterName are words that may follow a table name and change what is read
	// (PARTITION, SAMPLE): the rewrite cannot place its clause around them.
	AfterName map[string]bool
	// Hints are table hints written in parentheses straight after the name, as
	// SQL Server allows without WITH; "t (NOLOCK)" would otherwise look like a function call.
	Hints map[string]bool
	// EndsFrom are reserved words of this dialect, besides endsFrom, after which
	// a comma no longer introduces a table (MySQL's LIMIT 10, 20).
	EndsFrom map[string]bool
	// Fold is how the dialect compares names, such as Oracle upper-casing
	// unquoted ones; nil compares them without case.
	Fold func(Part) string
	// SelfNeedsRecursive is true when a WITH query's own name, used in its
	// body, names a table unless the WITH says RECURSIVE (MySQL and MariaDB).
	SelfNeedsRecursive bool
}

func (d Dialect) fold(p Part) string {
	if d.Fold != nil {
		return d.Fold(p)
	}
	return strings.ToLower(p.Name)
}

// endsFrom are words, reserved in every dialect here, that end a FROM clause
// at the same nesting level: after them a comma no longer introduces a table.
// The list is short on purpose. A word left out only makes a later comma look
// like a table, which fails to resolve and is refused; a word that could be
// an alias would hide the tables after it.
var endsFrom = map[string]bool{"where": true, "group": true, "having": true, "order": true, "for": true}

// existing are word pairs that already read history.
var existing = [][2]string{{"as", "of"}, {"for", "system_time"}, {"versions", "between"}, {"versions", "period"}}

// level is the state of one nesting level of parentheses.
type level struct {
	sel, from bool
	ctes      []string // folded CTE names defined at this level
	// pending CTE names become visible once their body is read (see SelfNeedsRecursive).
	pending   []string
	recursive bool
	// cteName is true where the next identifier names a CTE (after WITH, RECURSIVE or a comma of the WITH list).
	inWith, cteName bool
}

// Refs finds every base-table reference in one SELECT or WITH statement, at
// every nesting level. It refuses text that already reads history and any
// FROM or JOIN it cannot account for.
func Refs(toks []Token, d Dialect) ([]Ref, error) {
	for i := 0; i+1 < len(toks); i++ {
		for _, p := range existing {
			if toks[i].Word == p[0] && toks[i+1].Word == p[1] {
				return nil, refuse("The query already reads history (%s %s); remove that clause or the as-of time",
					strings.ToUpper(p[0]), strings.ToUpper(p[1]))
			}
		}
	}
	first := slices.IndexFunc(toks, func(t Token) bool { return t.Word != "" })
	if first < 0 || (toks[first].Word != "select" && toks[first].Word != "with") {
		return nil, refuse("Only a SELECT, with or without WITH, can be read as of a past time")
	}
	stack := []*level{{}}
	var refs []Ref
	cte := func(p Part) bool {
		for _, l := range stack {
			if slices.Contains(l.ctes, d.fold(p)) {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		top := stack[len(stack)-1]
		if top.cteName {
			top.cteName = false
			if t.Word == "recursive" {
				top.cteName, top.recursive = true, true
				continue
			}
			name, ok := ident(t, d)
			if !ok || i+1 == len(toks) || (toks[i+1].Text != "(" && toks[i+1].Word != "as") {
				return nil, refuse("Cannot find the name of a WITH query near %q", t.Text)
			}
			folded := d.fold(Part{Name: name, Quoted: t.Word == ""})
			if d.SelfNeedsRecursive && !top.recursive {
				top.pending = append(top.pending, folded)
			} else {
				top.ctes = append(top.ctes, folded)
			}
			continue
		}
		switch {
		case t.Text == "(":
			stack = append(stack, &level{})
		case t.Text == ")":
			if len(stack) == 1 {
				return nil, refuse("The parentheses do not balance")
			}
			stack = stack[:len(stack)-1]
		case t.Word == "with" && (i == 0 || toks[i-1].Text == "(" || toks[i-1].Text == ";"):
			top.inWith, top.cteName = true, true
		case t.Word == "select":
			top.sel, top.from, top.inWith = true, false, false
			top.ctes, top.pending = append(top.ctes, top.pending...), nil
		case top.inWith && !top.sel && t.Text == ",":
			top.cteName = true
			top.ctes, top.pending = append(top.ctes, top.pending...), nil
		case t.Word == "from" && top.sel && (i == 0 || toks[i-1].Word != "distinct"), // not IS DISTINCT FROM
			top.from && (t.Word == "join" || t.Word == "apply" || t.Word == "straight_join" || t.Text == ","):
			top.from = true
			ref, err := tableRef(toks, i+1, d, cte)
			if err != nil {
				return nil, err
			}
			if ref != nil {
				refs = append(refs, *ref)
			}
		case t.Word == "for" && i > 0 && (toks[i-1].Word == "index" || toks[i-1].Word == "key"):
			// MySQL's USE INDEX FOR JOIN | ORDER BY | GROUP BY (...): a hint, the FROM clause goes on.
			switch {
			case i+1 < len(toks) && toks[i+1].Word == "join":
				i++
			case i+2 < len(toks) && (toks[i+1].Word == "order" || toks[i+1].Word == "group") && toks[i+2].Word == "by":
				i += 2
			default:
				return nil, refuse("Cannot read the index hint near %q", t.Text)
			}
		case top.from && (endsFrom[t.Word] || d.EndsFrom[t.Word]):
			top.from = false
		}
	}
	if len(stack) != 1 {
		return nil, refuse("The parentheses do not balance")
	}
	return refs, nil
}

// tableRef reads the table reference starting at toks[i]: a base table, or
// nil for a subquery, a table function, a CTE or DUAL.
func tableRef(toks []Token, i int, d Dialect, cte func(Part) bool) (*Ref, error) {
	if i < len(toks) && toks[i].Word == "lateral" {
		i++
	}
	if i == len(toks) {
		return nil, refuse("The query ends where a table name should be")
	}
	if toks[i].Text == "(" {
		j := i
		for j < len(toks) && toks[j].Text == "(" {
			j++
		}
		if j < len(toks) && (toks[j].Word == "select" || toks[j].Word == "with" || toks[j].Word == "values") {
			return nil, nil // the walk reads the subquery's own FROM
		}
		return nil, refuse("Cannot read a parenthesized join as of a past time; write the joins without the parentheses")
	}
	var parts []Part
	j := i
	for {
		if j == len(toks) {
			return nil, refuse("The query ends where a table name should be")
		}
		t := toks[j]
		switch {
		case t.Word != "" && d.DottedWords:
			for _, p := range strings.Split(t.Text, ".") {
				parts = append(parts, Part{Name: p})
			}
		case t.Word != "":
			parts = append(parts, Part{Name: t.Text})
		default:
			name, ok := ident(t, d)
			if !ok {
				return nil, refuse("Cannot find the table this query reads near %q", t.Text)
			}
			parts = append(parts, Part{Name: name, Quoted: true})
		}
		j++
		if j+1 < len(toks) && toks[j].Text == "." {
			j++
			continue
		}
		break
	}
	last := toks[j-1]
	ref := &Ref{Parts: parts, End: last.Pos + len(last.Text)}
	ref.Text = name(parts)
	lower := strings.ToLower(parts[len(parts)-1].Name)
	next := Token{}
	if j < len(toks) {
		next = toks[j]
	}
	switch {
	case next.Text == "(":
		if d.ReadsTable[lower] && len(parts) == 1 {
			return nil, refuse("%s reads a table inside its arguments, which cannot be read as of a past time", strings.ToUpper(lower))
		}
		if j+1 < len(toks) && d.Hints[toks[j+1].Word] {
			return nil, refuse("Write the table hint on %s as WITH (%s) so the as-of clause can go before it", ref.Text, toks[j+1].Text)
		}
		return nil, nil // a table function
	case len(parts) == 1 && !parts[0].Quoted && d.NotTables[lower]:
		return nil, nil
	case len(parts) == 1 && cte(parts[0]):
		return nil, nil
	case next.Text == "@" || next.Word != "" && d.AfterName[next.Word]:
		return nil, refuse("Cannot read %s as of a past time with %s after its name", ref.Text, strings.ToUpper(next.Text))
	}
	return ref, nil
}

func ident(t Token, d Dialect) (string, bool) {
	if t.Word != "" {
		return t.Text, true
	}
	if t.Text == "" || d.Ident == nil {
		return "", false
	}
	return d.Ident(t.Text)
}

func name(parts []Part) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = p.Name
	}
	return strings.Join(s, ".")
}

// Table is what an engine's catalog says about one reference.
type Table struct {
	Ref Ref
	// Rel is the relation the name resolves to; nil when it resolves to none.
	Rel *schema.Relation
	// Why says why Rel is nil, such as "is a synonym".
	Why string
	// DDL is the relation's last definition change (Oracle), zero when unknown.
	DDL time.Time
}

// Rewrite checks that every referenced table can be read as of a past time
// (allowed returns the reason one cannot, or "") and inserts clause after each name.
func Rewrite(sqlText string, tables []Table, clause string, allowed func(*schema.Relation) string) (string, error) {
	var problems []string
	seen := map[string]bool{}
	for _, t := range tables {
		why := t.Why
		if t.Rel != nil {
			why = allowed(t.Rel)
		} else if why == "" {
			why = "is not a table or view this user can see"
		}
		if p := t.Ref.Text + " " + why; why != "" && !seen[p] {
			seen[p] = true
			problems = append(problems, p)
		}
	}
	if len(problems) > 0 {
		return "", refuse("Cannot read as of a past time: %s", strings.Join(problems, "; "))
	}
	return Insert(sqlText, tables, clause), nil
}

// Insert puts clause after each reference's name.
func Insert(sqlText string, tables []Table, clause string) string {
	ends := make([]int, len(tables))
	for i, t := range tables {
		ends[i] = t.Ref.End
	}
	slices.Sort(ends)
	ends = slices.Compact(ends)
	var b strings.Builder
	prev := 0
	for _, e := range ends {
		b.WriteString(sqlText[prev:e])
		b.WriteString(" " + clause)
		if e < len(sqlText) && !strings.ContainsRune(" \t\r\n),;", rune(sqlText[e])) {
			b.WriteByte(' ')
		}
		prev = e
	}
	b.WriteString(sqlText[prev:])
	return b.String()
}

// Bounds is how far back each requested table can be read.
type Bounds struct {
	Now    time.Time     `json:"now"`
	Tables []TableBounds `json:"tables"`
	Note   string        `json:"note,omitempty"`
}

// Bound sources.
const (
	SourceUndoRetention      = "undo_retention"
	SourceTunedUndoRetention = "tuned_undo_retention"
	SourceAssumedDefault     = "assumed_default"
	SourceHistoryRetention   = "history_retention"
	SourceNone               = "none"
)

// TableBounds is one table's oldest readable time; Oldest is nil when the
// engine sets no lower limit or the table cannot be read as of a time.
type TableBounds struct {
	Table     string     `json:"table"`
	Kind      *string    `json:"kind"`
	Available bool       `json:"available"`
	Oldest    *time.Time `json:"oldest"`
	Source    string     `json:"source"`
	Note      string     `json:"note,omitempty"`
}

// MaxTables caps the tables one bounds request may name.
const MaxTables = 200

// ParseTables reads "schema.a,schema.b" into exact catalog names, as the schema model spells them.
func ParseTables(s string) ([]schema.QualifiedName, error) {
	var out []schema.QualifiedName
	for _, t := range strings.Split(s, ",") {
		sch, n, ok := strings.Cut(strings.TrimSpace(t), ".")
		if !ok || sch == "" || n == "" {
			return nil, refuse("tables must be a comma-separated list of schema.table names")
		}
		out = append(out, schema.QualifiedName{Schema: sch, Name: n})
	}
	if len(out) > MaxTables {
		return nil, refuse("tables may name at most %d tables", MaxTables)
	}
	return out, nil
}

// ExactRefs are references to catalog names spelled exactly, for resolving bounds.
func ExactRefs(names []schema.QualifiedName) []Ref {
	refs := make([]Ref, len(names))
	for i, n := range names {
		refs[i] = Ref{Parts: []Part{{Name: n.Schema, Quoted: true}, {Name: n.Name, Quoted: true}}, Text: n.Schema + "." + n.Name}
	}
	return refs
}

// Kind returns a pointer for TableBounds.Kind, nil for a relation without time travel.
func Kind(r *schema.Relation) *string {
	if r == nil || r.TimeTravel == nil {
		return nil
	}
	k := r.TimeTravel.Kind
	return &k
}

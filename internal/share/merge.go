package share

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dbscope/dbscope/internal/store"
)

// Mode is how an import combines the file with what the connection has.
type Mode string

const (
	// Merge takes every entry in the file and keeps local entries the file
	// does not mention.
	Merge Mode = "merge"
	// Replace makes the connection's state exactly the file's.
	Replace Mode = "replace"
)

// Changes an import makes to one entry.
const (
	Added     = "added"
	Changed   = "changed"
	Unchanged = "unchanged"
	Kept      = "kept"    // local only; merge keeps it
	Removed   = "removed" // local only; replace removes it
)

// Existing is what the connected database has, to flag entries that name
// something it does not; nil means the schema was not available.
type Existing struct {
	// Schemas were read; entries in other schemas are not judged.
	Schemas     map[string]bool
	Tables      map[string]bool // "schema.table"
	InferredFKs map[string]bool // infer.Key of every inferred candidate
}

func schemaOf(qualified string) string {
	s, _, _ := strings.Cut(qualified, ".")
	return s
}

func (ex *Existing) missingTable(t string) bool {
	return ex != nil && ex.Schemas[schemaOf(t)] && !ex.Tables[t]
}

// missingFK judges a key only when both its tables' schemas were read.
func (ex *Existing) missingFK(key string) bool {
	from, to, _ := strings.Cut(key, "->")
	return ex != nil && ex.Schemas[schemaOf(from)] && ex.Schemas[schemaOf(to)] && !ex.InferredFKs[key]
}

// Item is one entry of the preview.
type Item struct {
	// Layout is the layout key, for table positions.
	Layout string `json:"layout,omitempty"`
	Key    string `json:"key"`
	Change string `json:"change"`
	// Detail says what changes, e.g. "accepted → rejected".
	Detail string `json:"detail,omitempty"`
	// Orphan marks a table or foreign key this database does not have.
	Orphan bool `json:"orphan,omitempty"`
}

// Section previews one kind of entry.
type Section struct {
	Items   []Item         `json:"items"`
	Counts  map[string]int `json:"counts"`
	Orphans int            `json:"orphans"`
}

// Preview is what an import would do.
type Preview struct {
	Mode          Mode    `json:"mode"`
	SchemaChecked bool    `json:"schemaChecked"`
	ForeignKeys   Section `json:"foreignKeys"`
	Glossary      Section `json:"glossary"`
	Tables        Section `json:"tables"`
}

func (s *Section) add(it Item) {
	s.Items = append(s.Items, it)
	s.Counts[it.Change]++
	if it.Orphan {
		s.Orphans++
	}
}

func newSection() Section { return Section{Items: []Item{}, Counts: map[string]int{}} }

// Plan previews importing incoming over local and returns the state to apply.
// Any mode other than Merge replaces.
func Plan(local, incoming File, mode Mode, ex *Existing) (Preview, store.Shared) {
	if mode != Merge {
		mode = Replace
	}
	p := Preview{Mode: mode, SchemaChecked: ex != nil, ForeignKeys: newSection(), Glossary: newSection(), Tables: newSection()}
	out := store.Shared{Decisions: map[string]string{}, Glossary: []store.GlossaryEntry{}, Layouts: []store.Layout{}}
	localOnly := Kept
	if mode == Replace {
		localOnly = Removed
	}

	for _, k := range unionKeys(local.ForeignKeys, incoming.ForeignKeys) {
		mine, inLocal := local.ForeignKeys[k]
		theirs, inFile := incoming.ForeignKeys[k]
		it := Item{Key: k, Orphan: inFile && ex.missingFK(k)}
		switch {
		case !inFile:
			it.Change = localOnly
		case !inLocal:
			it.Change = Added
		case mine != theirs:
			it.Change, it.Detail = Changed, mine+" → "+theirs
		default:
			it.Change = Unchanged
		}
		p.ForeignKeys.add(it)
		switch {
		case inFile:
			out.Decisions[k] = theirs
		case mode == Merge:
			out.Decisions[k] = mine
		}
	}

	planGlossary(&p, &out, local, incoming, mode, localOnly)

	for _, key := range unionKeys(local.Layouts, incoming.Layouts) {
		mine, theirs := local.Layouts[key], incoming.Layouts[key]
		result := store.Layout{Key: key, Positions: map[string]store.Position{}, Collapsed: []string{}}
		for _, t := range tablesOf(mine, theirs) {
			before, after := stateOf(mine, t), stateOf(theirs, t)
			it := Item{Layout: key, Key: t, Orphan: after.mentioned && ex.missingTable(t)}
			switch {
			case !after.mentioned:
				it.Change, after = localOnly, before
			default:
				// A merge keeps a local position the file does not give (it may only collapse the table).
				if mode == Merge && !after.placed && before.placed {
					after.pos, after.placed = before.pos, true
				}
				switch {
				case !before.mentioned:
					it.Change = Added
				case before != after:
					it.Change, it.Detail = Changed, before.String()+" → "+after.String()
				default:
					it.Change = Unchanged
				}
			}
			p.Tables.add(it)
			if it.Change == Removed {
				continue
			}
			if after.placed {
				result.Positions[t] = store.Position{X: float64(after.pos[0]), Y: float64(after.pos[1])}
			}
			if after.collapsed {
				result.Collapsed = append(result.Collapsed, t)
			}
		}
		if len(result.Positions) > 0 || len(result.Collapsed) > 0 {
			out.Layouts = append(out.Layouts, result)
		}
	}
	return p, out
}

// CheckError is a result the store would not accept as a whole.
type CheckError struct{ msg string }

func (e *CheckError) Error() string { return e.msg }

// Check refuses a result the store would not accept as a whole.
func Check(sh store.Shared) error {
	if n := len(sh.Glossary); n > store.MaxGlossary {
		return &CheckError{fmt.Sprintf("the import would leave %d glossary terms; a connection holds at most %d (import with replace, or remove terms first)", n, store.MaxGlossary)}
	}
	return nil
}

// planGlossary matches terms case-insensitively, as the glossary editor does.
// Merge keeps the local order, updates matching terms in place and appends
// new ones in the file's order; replace uses the file's order.
func planGlossary(p *Preview, out *store.Shared, local, incoming File, mode Mode, localOnly string) {
	byFold := func(f File) map[string]store.GlossaryEntry {
		r := map[string]store.GlossaryEntry{}
		for _, g := range f.Glossary {
			r[fold(g.Term)] = g
		}
		return r
	}
	mine, theirs := byFold(local), byFold(incoming)
	for _, k := range unionKeys(mine, theirs) {
		l, inLocal := mine[k]
		f, inFile := theirs[k]
		it := Item{Key: f.Term}
		switch {
		case !inFile:
			it.Key, it.Change = l.Term, localOnly
		case !inLocal:
			it.Change = Added
		case l != f:
			it.Change = Changed
			if l.Term != f.Term {
				it.Detail = "spelled " + l.Term + " here"
			}
		default:
			it.Change = Unchanged
		}
		p.Glossary.add(it)
	}
	if mode == Replace {
		for _, term := range incoming.glossaryOrder() {
			out.Glossary = append(out.Glossary, incoming.Glossary[term])
		}
		return
	}
	seen := map[string]bool{}
	for _, term := range local.glossaryOrder() {
		k := fold(term)
		seen[k] = true
		if f, ok := theirs[k]; ok {
			out.Glossary = append(out.Glossary, f)
		} else {
			out.Glossary = append(out.Glossary, mine[k])
		}
	}
	for _, term := range incoming.glossaryOrder() {
		if !seen[fold(term)] {
			out.Glossary = append(out.Glossary, incoming.Glossary[term])
		}
	}
}

// tableState is one table's place in one layout.
type tableState struct {
	mentioned, placed, collapsed bool
	pos                          [2]int
}

func stateOf(l Layout, t string) tableState {
	p, placed := l.Positions[t]
	collapsed := slices.Contains(l.Collapsed, t)
	return tableState{mentioned: placed || collapsed, placed: placed, collapsed: collapsed, pos: p}
}

func (s tableState) String() string {
	out := "not placed"
	if s.placed {
		out = fmt.Sprintf("at %d, %d", s.pos[0], s.pos[1])
	}
	if s.collapsed {
		out += ", collapsed"
	}
	return out
}

func tablesOf(a, b Layout) []string {
	set := map[string]bool{}
	for _, l := range []Layout{a, b} {
		for t := range l.Positions {
			set[t] = true
		}
		for _, t := range l.Collapsed {
			set[t] = true
		}
	}
	return sortedKeys(set)
}

func unionKeys[V any](a, b map[string]V) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	return sortedKeys(set)
}

// Package share exports and imports the per-connection state teammates can
// keep in their repository: canvas layouts, decisions on inferred foreign
// keys, and the assistant glossary (docs/SHARING.md).
package share

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dbscope/dbscope/internal/nl"
	"github.com/dbscope/dbscope/internal/store"
)

const (
	Kind          = "dbscope-shared-state"
	FormatVersion = 1
	// MaxBytes bounds a file the agent or CLI will read.
	MaxBytes = 8 << 20
)

// File is the shared state, keyed only by names that mean the same thing on
// every teammate's machine.
type File struct {
	// ForeignKeys maps an inferred foreign key (infer.Key) to "accepted" or "rejected".
	ForeignKeys map[string]string
	// Glossary maps a term to its entry; Term repeats the key.
	Glossary map[string]store.GlossaryEntry
	// Layouts maps a layout key (the sorted schema list) to its layout.
	Layouts map[string]Layout

	// order is the glossary's order: the connection's, or the file's. The
	// glossary is written in it, so a replace import (and Undo) keeps it.
	order []string
}

// Layout is one canvas layout: integer positions per "schema.table" and the
// tables shown collapsed.
type Layout struct {
	Positions map[string][2]int
	Collapsed []string
}

// FromStore converts a profile's state. Positions are rounded so a sub-pixel
// drag does not change the file.
func FromStore(sh store.Shared) File {
	f := File{ForeignKeys: map[string]string{}, Glossary: map[string]store.GlossaryEntry{}, Layouts: map[string]Layout{}}
	for k, v := range sh.Decisions {
		f.ForeignKeys[k] = v
	}
	for _, g := range sh.Glossary {
		g.Term = strings.TrimSpace(g.Term)
		f.Glossary[g.Term] = g
		f.order = append(f.order, g.Term)
	}
	for _, l := range sh.Layouts {
		out := Layout{Positions: map[string][2]int{}, Collapsed: sortedSet(l.Collapsed)}
		for t, p := range l.Positions {
			out.Positions[t] = [2]int{int(math.Round(p.X)), int(math.Round(p.Y))}
		}
		f.Layouts[l.Key] = out
	}
	return f
}

// Marshal writes f deterministically: sorted keys (the glossary keeps its own
// order, which a teammate set on purpose), two-space indent, one table
// position per line, multi-line text as an array of lines, and a trailing
// newline, so a change shows up as a small diff.
func (f File) Marshal() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "{\n  \"kind\": %s,\n  \"formatVersion\": %d,\n", q(Kind), FormatVersion)

	b.WriteString("  \"foreignKeys\": {")
	for i, k := range sortedKeys(f.ForeignKeys) {
		fmt.Fprintf(&b, "%s\n    %s: %s", comma(i), q(k), q(f.ForeignKeys[k]))
	}
	b.WriteString(closing(len(f.ForeignKeys), "  ", "}") + ",\n")

	b.WriteString("  \"glossary\": {")
	for i, term := range f.glossaryOrder() {
		g := f.Glossary[term]
		fmt.Fprintf(&b, "%s\n    %s: {\n      \"definition\": %s", comma(i), q(term), text(g.Definition, "      "))
		if g.SQL != "" {
			fmt.Fprintf(&b, ",\n      \"sql\": %s", text(g.SQL, "      "))
		}
		b.WriteString("\n    }")
	}
	b.WriteString(closing(len(f.Glossary), "  ", "}") + ",\n")

	b.WriteString("  \"layouts\": {")
	for i, key := range sortedKeys(f.Layouts) {
		l := f.Layouts[key]
		fmt.Fprintf(&b, "%s\n    %s: {\n      \"collapsed\": [", comma(i), q(key))
		for j, t := range l.Collapsed {
			fmt.Fprintf(&b, "%s\n        %s", comma(j), q(t))
		}
		b.WriteString(closing(len(l.Collapsed), "      ", "]") + ",\n      \"positions\": {")
		for j, t := range sortedKeys(l.Positions) {
			p := l.Positions[t]
			fmt.Fprintf(&b, "%s\n        %s: [%d, %d]", comma(j), q(t), p[0], p[1])
		}
		b.WriteString(closing(len(l.Positions), "      ", "}") + "\n    }")
	}
	b.WriteString(closing(len(f.Layouts), "  ", "}") + "\n}\n")
	return b.Bytes()
}

// Parse reads and validates a file. Unknown top-level fields (such as
// "$schema") are ignored; a newer formatVersion and repeated keys are refused.
func Parse(data []byte) (File, error) {
	if len(data) > MaxBytes {
		return File{}, fmt.Errorf("the file is larger than %d MiB", MaxBytes>>20)
	}
	if err := checkDuplicateKeys(data); err != nil {
		return File{}, fmt.Errorf("not a dbscope shared-state file: %w", err)
	}
	var raw struct {
		Kind          string            `json:"kind"`
		FormatVersion *int              `json:"formatVersion"`
		ForeignKeys   map[string]string `json:"foreignKeys"`
		Glossary      json.RawMessage   `json:"glossary"`
		Layouts       map[string]struct {
			Collapsed []string             `json:"collapsed"`
			Positions map[string][]float64 `json:"positions"`
		} `json:"layouts"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return File{}, fmt.Errorf("not a dbscope shared-state file: %w", err)
	}
	switch {
	case raw.Kind != Kind:
		return File{}, fmt.Errorf("not a dbscope shared-state file (kind is %q, want %q)", raw.Kind, Kind)
	case raw.FormatVersion == nil || *raw.FormatVersion < 1:
		return File{}, errors.New("formatVersion is missing or invalid")
	case *raw.FormatVersion > FormatVersion:
		return File{}, fmt.Errorf("the file uses format %d, which needs a newer dbscope (this one reads up to %d)", *raw.FormatVersion, FormatVersion)
	}
	f := File{ForeignKeys: map[string]string{}, Glossary: map[string]store.GlossaryEntry{}, Layouts: map[string]Layout{}}
	for k, v := range raw.ForeignKeys {
		if v != store.DecisionAccepted && v != store.DecisionRejected {
			return File{}, fmt.Errorf("foreignKeys[%q] is %q; want accepted or rejected", k, v)
		}
		// The same shape the inferred-FK API accepts.
		if !validName(k) || !strings.Contains(k, ")->") || !strings.HasSuffix(k, ")") {
			return File{}, fmt.Errorf("foreignKeys key %q must look like schema.table(col)->schema.table(col)", k)
		}
		f.ForeignKeys[k] = v
	}
	if err := parseGlossary(raw.Glossary, &f); err != nil {
		return File{}, err
	}
	for key, l := range raw.Layouts {
		if !validName(key) {
			return File{}, fmt.Errorf("layout key %q must be 1 to %d bytes", key, maxName)
		}
		out := Layout{Positions: map[string][2]int{}, Collapsed: sortedSet(l.Collapsed)}
		for _, t := range out.Collapsed {
			if !validName(t) {
				return File{}, fmt.Errorf("layouts[%q].collapsed has an empty or overlong table name", key)
			}
		}
		for t, p := range l.Positions {
			switch {
			case !validName(t):
				return File{}, fmt.Errorf("layouts[%q].positions has an empty or overlong table name", key)
			case len(p) != 2 || math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.Abs(p[0]) > 1e7 || math.Abs(p[1]) > 1e7:
				return File{}, fmt.Errorf("layouts[%q].positions[%q] must be [x, y] within ±10,000,000", key, t)
			}
			out.Positions[t] = [2]int{int(math.Round(p[0])), int(math.Round(p[1]))}
		}
		f.Layouts[key] = out
	}
	return f, nil
}

const maxName = 4096

func validName(s string) bool { return s != "" && len(s) <= maxName }

// parseGlossary reads the glossary object in document order, so the order a
// teammate gave their terms survives.
func parseGlossary(raw json.RawMessage, f *File) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("glossary must be an object of terms")
	}
	folded := map[string]string{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("glossary: %w", err)
		}
		term := strings.TrimSpace(tok.(string))
		var e struct {
			Definition json.RawMessage `json:"definition"`
			SQL        json.RawMessage `json:"sql"`
		}
		if err := dec.Decode(&e); err != nil {
			return fmt.Errorf("glossary[%q]: %w", term, err)
		}
		// The glossary treats terms case-insensitively.
		if other, ok := folded[fold(term)]; ok {
			return fmt.Errorf("glossary terms %q and %q are the same term", other, term)
		}
		folded[fold(term)] = term
		def, err := untext(e.Definition)
		if err != nil {
			return fmt.Errorf("glossary[%q].definition: %w", term, err)
		}
		sql, err := untext(e.SQL)
		if err != nil {
			return fmt.Errorf("glossary[%q].sql: %w", term, err)
		}
		g := store.GlossaryEntry{Term: term, Definition: def, SQL: sql}
		if err := checkGlossary(g); err != nil {
			return err
		}
		f.Glossary[term] = g
		f.order = append(f.order, term)
		if len(f.order) > store.MaxGlossary {
			return fmt.Errorf("the glossary has more than %d terms", store.MaxGlossary)
		}
	}
	return nil
}

// fold is how glossary terms are compared, matching store.UpsertGlossaryTerm.
func fold(term string) string { return strings.ToLower(term) }

// checkDuplicateKeys refuses an object with the same key twice, which
// encoding/json would otherwise resolve silently by keeping the last.
func checkDuplicateKeys(data []byte) error {
	type frame struct {
		keys      map[string]bool
		expectKey bool
	}
	var stack []*frame // nil entries are arrays
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1] != nil {
			stack[n-1].expectKey = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if n := len(stack); n > 0 && stack[n-1] != nil && stack[n-1].expectKey {
			if k, ok := tok.(string); ok {
				if stack[n-1].keys[k] {
					return fmt.Errorf("the key %q appears twice in one object", k)
				}
				stack[n-1].keys[k], stack[n-1].expectKey = true, false
				continue
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{keys: map[string]bool{}, expectKey: true})
		case json.Delim('['):
			stack = append(stack, nil)
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
}

// checkGlossary applies the limits the glossary API enforces.
func checkGlossary(g store.GlossaryEntry) error {
	switch {
	case g.Term == "" || g.Term != strings.TrimSpace(g.Term) || utf8.RuneCountInString(g.Term) > nl.MaxTermLen:
		return fmt.Errorf("glossary term %q must be 1 to %d characters without surrounding spaces", g.Term, nl.MaxTermLen)
	case utf8.RuneCountInString(g.Definition) > nl.MaxDefinitionLen:
		return fmt.Errorf("glossary[%q].definition is longer than %d characters", g.Term, nl.MaxDefinitionLen)
	case utf8.RuneCountInString(g.SQL) > nl.MaxTermSQLLen:
		return fmt.Errorf("glossary[%q].sql is longer than %d characters", g.Term, nl.MaxTermSQLLen)
	}
	return nil
}

// ToStore converts f to the store's form.
func (f File) ToStore() store.Shared {
	sh := store.Shared{Decisions: map[string]string{}, Glossary: []store.GlossaryEntry{}, Layouts: []store.Layout{}}
	for k, v := range f.ForeignKeys {
		sh.Decisions[k] = v
	}
	for _, term := range f.glossaryOrder() {
		sh.Glossary = append(sh.Glossary, f.Glossary[term])
	}
	for _, key := range sortedKeys(f.Layouts) {
		l := f.Layouts[key]
		out := store.Layout{Key: key, Positions: map[string]store.Position{}, Collapsed: append([]string{}, l.Collapsed...)}
		for t, p := range l.Positions {
			out.Positions[t] = store.Position{X: float64(p[0]), Y: float64(p[1])}
		}
		sh.Layouts = append(sh.Layouts, out)
	}
	return sh
}

// glossaryOrder is the terms in order; a File built by hand has none, so its terms sort.
func (f File) glossaryOrder() []string {
	if len(f.order) == len(f.Glossary) {
		return f.order
	}
	return sortedKeys(f.Glossary)
}

// q quotes s as JSON without escaping <, > and &, which SQL snippets use.
func q(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// text writes a single line as a string and several lines as an array of
// lines, so editing one line of a long definition is a one-line diff.
func text(s, indent string) string {
	lines := strings.Split(s, "\n")
	if len(lines) == 1 {
		return q(s)
	}
	var b strings.Builder
	b.WriteString("[")
	for i, l := range lines {
		fmt.Fprintf(&b, "%s\n%s  %s", comma(i), indent, q(l))
	}
	b.WriteString("\n" + indent + "]")
	return b.String()
}

func untext(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return "", errors.New("must be a string or an array of lines")
	}
	return strings.Join(lines, "\n"), nil
}

func comma(i int) string {
	if i == 0 {
		return ""
	}
	return ","
}

// closing ends an object or array of n items opened at the end of the
// previous line; an empty one closes on that line ({} or []).
func closing(n int, indent, end string) string {
	if n == 0 {
		return end
	}
	return "\n" + indent + end
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedSet(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

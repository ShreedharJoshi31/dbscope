// Package infer guesses foreign keys that a schema does not declare, from
// column naming: posts.user_id most likely references users.id.
package infer

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dbscope/dbscope/internal/schema"
)

// Decision statuses stored per inferred key.
const (
	Accepted = "accepted"
	Rejected = "rejected"
	Unset    = "unset"
)

const (
	baseConfidence  = 0.6
	indexedBonus    = 0.2
	exactNameBonus  = 0.1
	maxConfidence   = 0.95
	maxIdentifierLn = 63 // NAMEDATALEN - 1
)

// Key renders a relationship as the API identifies it:
// <schema>.<table>(<col>[,<col>])-><schema>.<table>(<col>[,<col>]).
func Key(from schema.QualifiedName, cols []string, to schema.QualifiedName, refCols []string) string {
	return from.String() + "(" + strings.Join(cols, ",") + ")->" + to.String() + "(" + strings.Join(refCols, ",") + ")"
}

// Candidate is one inferred foreign key on relation From.
type Candidate struct {
	From schema.QualifiedName
	FK   schema.ForeignKey
}

// Infer returns inferred foreign keys for columns no declared foreign key
// covers, ordered by relation and then column position. The model must be
// sorted by schema and name, as introspect returns it.
func Infer(m *schema.Model) []Candidate {
	byName := map[string][]*schema.Relation{} // lower-cased table name -> tables in model order
	for i := range m.Relations {
		r := &m.Relations[i]
		if isTable(r) && !r.Stub {
			k := strings.ToLower(r.Name)
			byName[k] = append(byName[k], r)
		}
	}
	var out []Candidate
	for i := range m.Relations {
		r := &m.Relations[i]
		if !isTable(r) || r.Stub {
			continue
		}
		for _, c := range r.Columns {
			if fk, ok := inferColumn(m, r, c, byName); ok {
				out = append(out, Candidate{From: r.QName(), FK: fk})
			}
		}
	}
	return out
}

// Apply adds the inferred foreign keys to m. decisions maps keys to
// Accepted or Rejected: rejected keys are left out and accepted ones get
// confidence 1.
func Apply(m *schema.Model, decisions map[string]string) {
	for _, c := range Infer(m) {
		switch decisions[c.FK.Name] {
		case Rejected:
			continue
		case Accepted:
			c.FK.Confidence = 1
		}
		r := m.Relation(c.From)
		r.ForeignKeys = append(r.ForeignKeys, c.FK)
	}
}

func isTable(r *schema.Relation) bool {
	return r.Kind == schema.KindTable || r.Kind == schema.KindPartitionedTable || r.Kind == schema.KindForeignTable
}

func inferColumn(m *schema.Model, r *schema.Relation, c schema.Column, byName map[string][]*schema.Relation) (schema.ForeignKey, bool) {
	stem, typeCol, ok := splitIDColumn(c.Name)
	if !ok || declared(r, c.Name) {
		return schema.ForeignKey{}, false
	}
	// Rails-style polymorphic association: commentable_id + commentable_type.
	for _, sib := range r.Columns {
		if strings.EqualFold(sib.Name, typeCol) {
			return schema.ForeignKey{}, false
		}
	}
	target, refCol, exact := findTarget(m, r, c, stem, byName)
	if target == nil {
		return schema.ForeignKey{}, false
	}
	conf := baseConfidence
	if indexed(r, c.Name) {
		conf += indexedBonus
	}
	if exact {
		conf += exactNameBonus
	}
	conf = math.Round(min(conf, maxConfidence)*100) / 100
	return schema.ForeignKey{
		Name:       Key(r.QName(), []string{c.Name}, target.QName(), []string{refCol}),
		Columns:    []string{c.Name},
		Ref:        target.QName(),
		RefCols:    []string{refCol},
		Inferred:   true,
		Confidence: conf,
	}, true
}

// splitIDColumn recognises user_id and userId. It returns the stem in snake
// case ("user", "order_item" for orderItemId) and the name of the sibling
// column that would make the pair polymorphic.
func splitIDColumn(name string) (stem, typeCol string, ok bool) {
	if len(name) > maxIdentifierLn {
		return "", "", false
	}
	if s, found := cutSuffixFold(name, "_id"); found && s != "" && !strings.HasSuffix(s, "_") {
		return strings.ToLower(s), s + name[len(s):len(s)+1] + "type", true
	}
	if s, found := strings.CutSuffix(name, "Id"); found && s != "" {
		last, _ := utf8.DecodeLastRuneInString(s)
		if unicode.IsLower(last) || unicode.IsDigit(last) {
			return camelToSnake(s), s + "Type", true
		}
	}
	return "", "", false
}

func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s[:len(s)-len(suffix)], true
	}
	return s, false
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tableNames lists the table names a stem may refer to, the exact stem first.
func tableNames(stem string) []string {
	names := []string{stem, stem + "s", stem + "es"}
	if y, ok := strings.CutSuffix(stem, "y"); ok && y != "" && !strings.ContainsRune("aeiou", rune(y[len(y)-1])) {
		names = append(names, y+"ies")
	}
	return names
}

// findTarget looks for the referenced table in r's schema first, then in the
// other selected schemas in order. exact reports a match without pluralising.
func findTarget(m *schema.Model, r *schema.Relation, c schema.Column, stem string, byName map[string][]*schema.Relation) (*schema.Relation, string, bool) {
	schemas := []string{r.Schema}
	for _, s := range m.Schemas {
		if s != r.Schema {
			schemas = append(schemas, s)
		}
	}
	names := tableNames(stem)
	for _, s := range schemas {
		for i, n := range names {
			for _, t := range byName[n] {
				if t.Schema != s {
					continue
				}
				refCol, ok := targetColumn(t, c)
				if !ok || (t == r && refCol == c.Name) {
					continue
				}
				return t, refCol, i == 0
			}
		}
	}
	return nil, "", false
}

// targetColumn picks the column of t that c would reference: a single-column
// primary key, or else a single-column unique key named id or like c. Its
// type must be compatible with c's.
func targetColumn(t *schema.Relation, c schema.Column) (string, bool) {
	var options []string
	if len(t.PrimaryKey) == 1 {
		options = append(options, t.PrimaryKey[0])
	} else {
		for _, name := range []string{"id", c.Name} {
			if unique(t, name) {
				options = append(options, name)
			}
		}
	}
	for _, name := range options {
		if tc := t.Column(name); tc != nil && compatible(c.Type, tc.Type) {
			return name, true
		}
	}
	return "", false
}

func unique(t *schema.Relation, col string) bool {
	for _, u := range t.Uniques {
		if len(u) == 1 && u[0] == col {
			return true
		}
	}
	for _, ix := range t.Indexes {
		if ix.Unique && ix.Valid && ix.Partial == "" && len(ix.Columns) == 1 && ix.Columns[0] == col {
			return true
		}
	}
	return false
}

var integerTypes = []string{"smallint", "integer", "bigint"}

// compatible reports whether two format_type() strings can be joined as a
// key: the same type ignoring modifiers such as varchar length, or both
// integers.
func compatible(a, b string) bool {
	a, b = baseType(a), baseType(b)
	if a == b {
		return true
	}
	return slices.Contains(integerTypes, a) && slices.Contains(integerTypes, b)
}

// baseType drops type modifiers: "character varying(255)" -> "character varying",
// "timestamp(3) with time zone" -> "timestamp with time zone".
func baseType(t string) string {
	for {
		i := strings.IndexByte(t, '(')
		j := strings.IndexByte(t, ')')
		if i < 0 || j < i {
			return t
		}
		t = strings.TrimRight(t[:i], " ") + t[j+1:]
	}
}

// declared reports whether a foreign key already covers col. Earlier
// inferred keys count too, so Apply is idempotent.
func declared(r *schema.Relation, col string) bool {
	for _, fk := range r.ForeignKeys {
		if slices.Contains(fk.Columns, col) {
			return true
		}
	}
	return false
}

func indexed(r *schema.Relation, col string) bool {
	if len(r.PrimaryKey) > 0 && r.PrimaryKey[0] == col {
		return true
	}
	for _, ix := range r.Indexes {
		if len(ix.Columns) > 0 && ix.Columns[0] == col {
			return true
		}
	}
	return false
}

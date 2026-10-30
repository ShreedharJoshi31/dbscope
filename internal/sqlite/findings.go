package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/dbscope/dbscope/internal/schema"
)

// addFindings records design risks read from the catalog alone (pragmas and
// sqlite_stat1), never from rows. A check whose read fails is skipped, so a
// finding is never guessed and introspection never fails because of one.
func addFindings(ctx context.Context, tx *sql.Tx, m *schema.Model, strict map[string]bool) {
	statTables, statExists, statErr := stat1Tables(ctx, tx)
	var fkTables []*schema.Relation
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Kind != schema.KindTable {
			continue
		}
		for _, fk := range r.ForeignKeys {
			addFKIndexFinding(ctx, tx, m, r, fk)
		}
		if len(r.ForeignKeys) > 0 {
			fkTables = append(fkTables, r)
		}
		if statErr == nil && len(r.Indexes) > 0 && !statTables[r.Name] {
			if statExists {
				// Observed: ANALYZE writes no row for a table that is empty when it runs.
				r.AddFinding("DBS-SQLITE-NO-STATS", "info", "The table has indexes but sqlite_stat1 has no row for it, "+
					"so the query planner has no statistics for it. ANALYZE writes none for a table that was empty when it ran, "+
					"or that was created after it; ANALYZE or PRAGMA optimize after loading data writes them", "")
			} else {
				r.AddFinding("DBS-SQLITE-NO-STATS", "info", "The table has indexes but the database has no sqlite_stat1 table, "+
					"so the query planner has no statistics for it; ANALYZE or PRAGMA optimize writes them", "")
			}
		}
		if !strict[r.Name] {
			for _, c := range r.Columns {
				if msg := affinityTrap(c.Type); msg != "" {
					r.AddFinding("DBS-SQLITE-TYPE-AFFINITY", "warn", c.Name+" is declared "+msg, c.Name)
				}
			}
		}
	}
	if len(fkTables) > 0 {
		// https://www.sqlite.org/foreignkeys.html section 2: foreign key constraints are
		// disabled by default and must be enabled separately for each connection.
		who := "This table declares foreign keys"
		if len(fkTables) > 1 {
			who = fmt.Sprintf("%d tables declare foreign keys (shown once, on the first)", len(fkTables))
		}
		fkTables[0].AddFinding("DBS-SQLITE-FK-OFF", "info", who+". SQLite enforces them only on connections that run "+
			"PRAGMA foreign_keys = ON, which is off by default for every new connection. dbscope cannot see whether the "+
			"application's connections turn it on; on those that do not, rows can reference missing parents and ON DELETE actions do not run", "")
	}
}

func stat1Tables(ctx context.Context, tx *sql.Tx) (map[string]bool, bool, error) {
	out := map[string]bool{}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name = 'sqlite_stat1'`).Scan(&exists); err != nil || exists == 0 {
		return out, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT tbl FROM sqlite_stat1`)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	for rows.Next() {
		var tbl sql.NullString
		if err := rows.Scan(&tbl); err != nil {
			return nil, true, err
		}
		out[tbl.String] = true
	}
	return out, true, rows.Err()
}

// addFKIndexFinding flags a foreign key whose child columns no index leads with.
// Deleting a parent row, or changing its key, looks up child rows with
// "child column = parent value" under the parent key's collation, so an index
// serves the lookup when its leading columns are the key's columns in any
// order, each with that collation, and it is not partial with a predicate
// the lookup does not imply. Each case is checked against the planner in findings_test.go.
func addFKIndexFinding(ctx context.Context, tx *sql.Tx, m *schema.Model, r *schema.Relation, fk schema.ForeignKey) {
	colls, ok := parentCollations(ctx, tx, m, fk)
	if !ok {
		return // the parent key, and so the lookup's collation, cannot be read
	}
	n := len(fk.Columns)
	if n == 1 {
		if c := columnFold(r, fk.Columns[0]); c != nil && c.Identity == "by_default" {
			return // the rowid alias
		}
	}
	var best []string // the longest usable leading part of an index, for a composite key
	note := ""
	for _, ix := range r.Indexes {
		if ix.Partial != "" && !impliedByLookup(ix.Partial, fk.Columns) {
			if note == "" && leadsWith(ix.Columns, fk.Columns) {
				note = fmt.Sprintf(" (%s has these columns but is partial, WHERE %s, which the lookup does not imply)", ix.Name, ix.Partial)
			}
			continue
		}
		ixColls, err := indexCollations(ctx, tx, ix.Name)
		if err != nil || len(ixColls) != len(ix.Columns) {
			return
		}
		k := 0
		for ; k < len(ix.Columns) && k < n; k++ {
			i := indexFold(fk.Columns, ix.Columns[k])
			if i < 0 || !strings.EqualFold(ixColls[k], colls[i]) {
				if i >= 0 && note == "" && leadsWith(ix.Columns, fk.Columns) {
					note = fmt.Sprintf(" (%s has these columns, but its %s uses collation %s and the parent key %s)",
						ix.Name, ix.Columns[k], ixColls[k], colls[i])
				}
				break
			}
		}
		if k == n {
			return
		}
		if k > len(best) {
			best = ix.Columns[:k]
		}
	}
	cols := strings.Join(fk.Columns, ", ")
	var msg string
	if len(best) == 0 {
		msg = fmt.Sprintf("No index can serve lookups on %s: when foreign keys are enforced, each delete from %s, "+
			"and each change to a key of %s, scans all of %s for referencing rows", cols, fk.Ref.Name, fk.Ref.Name, r.Name)
	} else {
		msg = fmt.Sprintf("Only (%s) of the foreign key (%s) leads an index that can serve its lookups: when foreign keys are enforced, "+
			"each delete from %s, and each change to a key of %s, finds referencing rows in %s by that part alone",
			strings.Join(best, ", "), cols, fk.Ref.Name, fk.Ref.Name, r.Name)
	}
	column := ""
	if n == 1 {
		column = fk.Columns[0]
	}
	r.AddFinding("DBS-SQLITE-FK-NO-INDEX", "warn", msg+note, column)
}

// parentCollations returns the collation of each referenced column, read from
// the parent's primary key or unique index on exactly those columns, which
// SQLite requires and whose collations match the columns'.
func parentCollations(ctx context.Context, tx *sql.Tx, m *schema.Model, fk schema.ForeignKey) ([]string, bool) {
	if len(fk.RefCols) != len(fk.Columns) || slices.Contains(fk.RefCols, "") {
		return nil, false
	}
	var parent *schema.Relation
	for i := range m.Relations {
		if strings.EqualFold(m.Relations[i].Name, fk.Ref.Name) && m.Relations[i].Kind == schema.KindTable {
			parent = &m.Relations[i]
			break
		}
	}
	if parent == nil {
		return nil, false
	}
	if len(fk.RefCols) == 1 && len(parent.PrimaryKey) == 1 && strings.EqualFold(parent.PrimaryKey[0], fk.RefCols[0]) {
		if c := columnFold(parent, fk.RefCols[0]); c != nil && c.Identity == "by_default" {
			return []string{"BINARY"}, true // the rowid alias, an integer
		}
	}
	for _, ix := range parent.Indexes {
		if !ix.Unique || ix.Partial != "" || len(ix.Columns) != len(fk.RefCols) || !leadsWith(ix.Columns, fk.RefCols) {
			continue
		}
		ixColls, err := indexCollations(ctx, tx, ix.Name)
		if err != nil || len(ixColls) != len(ix.Columns) {
			return nil, false
		}
		out := make([]string, len(fk.RefCols))
		for i, c := range fk.RefCols {
			out[i] = ixColls[indexFold(ix.Columns, c)]
		}
		return out, true
	}
	return nil, false
}

func indexCollations(ctx context.Context, tx *sql.Tx, index string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT coalesce(coll, 'BINARY') FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, index)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// leadsWith reports whether the first len(cols) index columns are cols in any order.
func leadsWith(index, cols []string) bool {
	if len(index) < len(cols) {
		return false
	}
	for _, c := range index[:len(cols)] {
		if indexFold(cols, c) < 0 {
			return false
		}
	}
	return true
}

var notNullTerm = regexp.MustCompile(`(?i)^\(*\s*(?:"([^"]+)"|\x60([^\x60]+)\x60|\[([^\]]+)\]|(\w+))\s*\)*\s+(?:IS\s+NOT\s+NULL|NOT\s+NULL|NOTNULL)\s*\)*$`)
var andSplit = regexp.MustCompile(`(?i)\s+AND\s+`)

// impliedByLookup reports whether "col = value" on the foreign key's columns
// implies a partial index's predicate. SQLite's planner proves "col IS NOT NULL"
// from it and nothing else such a lookup could imply; any other predicate,
// or one this does not parse, counts as not implied.
func impliedByLookup(pred string, cols []string) bool {
	for _, term := range andSplit.Split(strings.TrimSpace(pred), -1) {
		g := notNullTerm.FindStringSubmatch(strings.TrimSpace(term))
		if g == nil || indexFold(cols, g[1]+g[2]+g[3]+g[4]) < 0 {
			return false
		}
	}
	return true
}

func indexFold(list []string, s string) int {
	return slices.IndexFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}

func columnFold(r *schema.Relation, name string) *schema.Column {
	for i := range r.Columns {
		if strings.EqualFold(r.Columns[i].Name, name) {
			return &r.Columns[i]
		}
	}
	return nil
}

// Affinity rules of https://www.sqlite.org/datatype3.html section 3.1, in
// order: the first rule whose substring the declared type contains wins; no
// type is BLOB (rule 3) and anything else NUMERIC (rule 5).
var affinityRules = []struct {
	affinity string
	keywords []string
}{
	{"INTEGER", []string{"INT"}},
	{"TEXT", []string{"CHAR", "CLOB", "TEXT"}},
	{"BLOB", []string{"BLOB"}},
	{"REAL", []string{"REAL", "FLOA", "DOUB"}},
}

// binaryNames are other databases' byte-string types, which fall to NUMERIC.
var binaryNames = []string{"BINARY", "VARBINARY", "BYTEA", "BYTES"}

var typeWord = regexp.MustCompile(`[A-Z0-9_]+`)

// affinityTrap describes a declared type whose affinity is not what its name
// says, or "" when there is none. Flagged, each observed in findings_test.go:
//   - a name matching more than one rule, where the earlier rule wins
//     silently: "FLOATING POINT" is INTEGER, not REAL, because "POINT" contains "INT";
//   - a name containing STRING, which matches no rule and is NUMERIC, not TEXT;
//   - BINARY, VARBINARY, BYTEA or BYTES, which are NUMERIC, not BLOB;
//   - ANY outside a STRICT table, which is NUMERIC there, not "keep as given".
//
// NUMERIC for dates, times, BOOLEAN or DECIMAL is what those names intend and
// is not flagged, nor are names like INTERVAL or POINT that only reach INTEGER,
// which converts like NUMERIC.
func affinityTrap(decl string) string {
	u := strings.ToUpper(decl)
	type hit struct{ affinity, keyword string }
	var hits []hit
	for _, rule := range affinityRules {
		for _, kw := range rule.keywords {
			if strings.Contains(u, kw) {
				hits = append(hits, hit{rule.affinity, kw})
				break
			}
		}
	}
	q := fmt.Sprintf("%q", decl)
	switch {
	case len(hits) > 1:
		msg := fmt.Sprintf("%s, which SQLite gives %s affinity, not %s: the name contains %q, and that rule is checked before %q",
			q, hits[0].affinity, hits[1].affinity, hits[0].keyword, hits[1].keyword)
		if hits[0].affinity == "INTEGER" && hits[1].affinity == "REAL" {
			msg += "; whole numbers are stored as integers, so dividing two of them truncates"
		}
		return msg
	case len(hits) == 1 || strings.TrimSpace(u) == "":
		return ""
	case strings.Contains(u, "STRING"):
		return q + `, which SQLite gives NUMERIC affinity, not TEXT (the name has no "CHAR", "CLOB" or "TEXT"): ` +
			`text that looks like a number is stored as a number, so '007' becomes 7`
	case strings.TrimSpace(u) == "ANY":
		return `ANY, which keeps values as given only in STRICT tables; in this table it gets NUMERIC affinity, so '007' becomes 7`
	}
	for _, w := range typeWord.FindAllString(u, -1) {
		if slices.Contains(binaryNames, w) {
			return q + `, which SQLite gives NUMERIC affinity, not BLOB (the name has no "BLOB"): ` +
				`blobs are kept, but text that looks like a number is stored as a number, so '007' becomes 7`
		}
	}
	return ""
}

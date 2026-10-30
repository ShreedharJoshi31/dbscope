package neo4j

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// MigrationBase is the database Cypher migrations are checked against.
type MigrationBase struct {
	Server Server
	// Labels and Types count nodes per label and relationships per type (count store).
	Labels map[string]int64
	Types  map[string]int64
	// Constraints and Indexes are the existing names, lower-cased.
	Constraints map[string]bool
	Indexes     map[string]bool
	// TxMemoryMax is db.memory.transaction.max as the server reports it, "" when unread.
	TxMemoryMax string
	client      *Client
	timeout     time.Duration
}

// Bulk-write thresholds: nodes or relationships one unbatched statement touches.
const (
	bulkMedium = 10_000
	bulkHigh   = 100_000
)

// ReadBase reads counts, constraint and index names and the transaction memory
// limit; constraint checks then query the data live, each under timeout.
func ReadBase(ctx context.Context, c *Client, timeout time.Duration) (*MigrationBase, error) {
	srv, err := describe(ctx, c)
	if err != nil {
		return nil, err
	}
	b := &MigrationBase{Server: srv, Labels: map[string]int64{}, Types: map[string]int64{}, Constraints: map[string]bool{},
		Indexes: map[string]bool{}, client: c, timeout: timeout}
	labels, err := c.strings(ctx, "CALL db.labels() YIELD label RETURN label")
	if err != nil {
		return nil, err
	}
	for _, l := range labels {
		if n, err := c.count(ctx, "MATCH (n:"+quote(l)+") RETURN count(n)"); err == nil {
			b.Labels[l] = n
		}
	}
	types, err := c.strings(ctx, "CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType")
	if err != nil {
		return nil, err
	}
	for _, t := range types {
		if n, err := c.count(ctx, "MATCH ()-[r:"+quote(t)+"]->() RETURN count(r)"); err == nil {
			b.Types[t] = n
		}
	}
	for q, into := range map[string]map[string]bool{"SHOW CONSTRAINTS YIELD name RETURN name": b.Constraints, "SHOW INDEXES YIELD name RETURN name": b.Indexes} {
		names, err := c.strings(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			into[strings.ToLower(n)] = true
		}
	}
	// Community has no privileges, so SHOW SETTINGS works there; an Enterprise user may lack it.
	if v, err := c.strings(ctx, "SHOW SETTINGS YIELD name, value WHERE name = 'db.memory.transaction.max' RETURN value"); err == nil && len(v) == 1 {
		b.TxMemoryMax = v[0]
	}
	return b, nil
}

// Analyze rates Cypher migrations. Neo4j changes schema without long locks,
// so the risks are statements the server refuses (checked on 2026.10
// Community and Enterprise and 5.26), unbatched writes held in one
// transaction's memory, lost data, and indexes queries cannot use until
// they are built (docs/ENGINES.md, "Neo4j").
func Analyze(ctx context.Context, base *MigrationBase, files []ingest.Input) *migrate.Report {
	rep := &migrate.Report{Engine: "neo4j", PGVersion: versionNum(base.Server.Version), Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: []string{}, Base: "live",
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	a := &cypherAnalysis{base: base, rep: rep}
	for _, f := range files {
		for _, p := range splitCypher(f.SQL) {
			st := a.statement(ctx, f.Name, p)
			rep.Statements = append(rep.Statements, st)
			rep.Rating = score.Max(rep.Rating, st.Rating)
		}
	}
	sort.Strings(rep.Diff.Added)
	sort.Strings(rep.Diff.Dropped)
	return rep
}

type cypherPiece struct {
	text          string
	toks          []token
	line, endLine int
}

// splitCypher divides a script at semicolons outside strings, quoted names and comments.
func splitCypher(s string) []cypherPiece {
	lineAt := func(pos int) int { return 1 + strings.Count(s[:pos], "\n") }
	var out []cypherPiece
	var cur []token
	flush := func() {
		if len(cur) > 0 {
			first, last := cur[0], cur[len(cur)-1]
			end := last.pos + len(last.text)
			out = append(out, cypherPiece{text: s[first.pos:end], toks: cur, line: lineAt(first.pos), endLine: lineAt(end)})
		}
		cur = nil
	}
	for _, t := range tokens(s) {
		if t.text == ";" {
			flush()
			continue
		}
		cur = append(cur, t)
	}
	flush()
	return out
}

type cypherAnalysis struct {
	base *MigrationBase
	rep  *migrate.Report
}

func (a *cypherAnalysis) statement(ctx context.Context, file string, p cypherPiece) (out migrate.Statement) {
	out = migrate.Statement{File: file, Line: p.line, Column: 1, EndLine: p.endLine, SQL: p.text,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	add := func(id string, r score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
		out.Rating = score.Max(out.Rating, r)
	}
	defer func() {
		out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
	}()
	toks := p.toks
	at := func(i int) string {
		if i < len(toks) {
			return toks[i].word
		}
		return ""
	}
	has := func(w string) bool { return wordIndex(toks, w, 0) >= 0 }
	fails := func(msg, alt string) { add("DBS-NEO4J-FAILS", score.High, msg, alt) }
	ifNot := has("if") && has("not") && has("exists")
	ifExists := has("if") && has("exists") && !has("not")
	out.Kind = strings.ToUpper(strings.TrimSpace(at(0) + " " + at(1)))

	switch {
	case at(0) == "create" && at(1) == "constraint":
		a.constraint(ctx, toks, ifNot, add, fails)
	case at(0) == "create" && (at(1) == "index" || wordIndex(toks, "index", 1) == 2):
		out.Kind, out.Work = "CREATE INDEX", "index_build"
		name := schemaName(toks)
		if name != "" && a.base.Indexes[strings.ToLower(name)] && !ifNot {
			fails("An index named "+name+" already exists, and CREATE INDEX without IF NOT EXISTS is refused", "Add IF NOT EXISTS, or choose another name")
			return out
		}
		label := patternName(toks, wordIndex(toks, "for", 0))
		rows := a.base.Labels[label] + a.base.Types[label]
		r := score.Low
		if rows >= bulkHigh {
			r = score.Medium
		}
		add("DBS-NEO4J-INDEX-POPULATING", r, fmt.Sprintf("The index is built in the background over %d %s entries; queries plan without it until SHOW INDEXES reports it ONLINE", rows, label),
			"Follow it with CALL db.awaitIndexes() (or db.awaitIndex) so later migrations and releases use it")
		a.rep.Diff.Added = append(a.rep.Diff.Added, "index "+name)
	case at(0) == "drop" && (at(1) == "constraint" || at(1) == "index"):
		name := ""
		if len(toks) > 2 {
			name = strings.Trim(toks[2].text, "`")
		}
		known := a.base.Constraints
		if at(1) == "index" {
			known = a.base.Indexes
		}
		if !known[strings.ToLower(name)] && !ifExists {
			fails(fmt.Sprintf("No %s named %s exists, and DROP %s without IF EXISTS is refused", at(1), name, strings.ToUpper(at(1))), "Add IF EXISTS")
			return out
		}
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, at(1)+" "+name)
		msg := "Queries that seek through the index fall back to scanning the label"
		if at(1) == "constraint" {
			msg = "The database stops rejecting duplicate or missing values, and the constraint's index goes with it"
		}
		add("DBS-NEO4J-DROPS-SCHEMA", score.Medium, msg, "")
	default:
		a.dataChange(toks, add)
	}
	return out
}

// constraint checks a CREATE CONSTRAINT against the edition and, for
// uniqueness, existence and keys, against the data it must already satisfy.
func (a *cypherAnalysis) constraint(ctx context.Context, toks []token, ifNot bool, add func(string, score.Rating, string, string), fails func(string, string)) {
	name := schemaName(toks)
	if name != "" && a.base.Constraints[strings.ToLower(name)] && !ifNot {
		fails("A constraint named "+name+" already exists, and CREATE CONSTRAINT without IF NOT EXISTS is refused", "Add IF NOT EXISTS")
		return
	}
	forAt, req := wordIndex(toks, "for", 0), wordIndex(toks, "require", 0)
	if forAt < 0 || req < 0 {
		return
	}
	label := patternName(toks, forAt)
	rel := false
	for _, t := range toks[forAt:req] {
		rel = rel || t.text == "["
	}
	props := requiredProps(toks[req+1:])
	is := wordIndex(toks, "is", req)
	kind := "type"
	switch {
	case wordIndex(toks, "unique", is) >= 0:
		kind = "unique"
	case wordIndex(toks, "key", is) >= 0:
		kind = "key"
	case wordIndex(toks, "null", is) >= 0:
		kind = "existence"
	}
	if kind != "unique" && a.base.Server.Community {
		fails(fmt.Sprintf("Property %s constraints are an Enterprise Edition feature; Community refuses them", kind),
			"Enforce it in the application, or use a uniqueness constraint where that is what is meant")
		return
	}
	entries := a.base.Labels[label]
	if rel {
		entries = a.base.Types[label]
	}
	if entries == 0 || len(props) == 0 {
		return
	}
	match := "MATCH (n:" + quote(label) + ")"
	if rel {
		match = "MATCH ()-[n:" + quote(label) + "]->()"
	}
	var all, any []string
	for _, p := range props {
		all = append(all, "n."+quote(p)+" IS NOT NULL")
		any = append(any, "n."+quote(p)+" IS NULL")
	}
	what := label + "." + strings.Join(props, ", ")
	if kind == "unique" || kind == "key" {
		vals := make([]string, len(props))
		for i, p := range props {
			vals[i] = "n." + quote(p)
		}
		q := match + " WHERE " + strings.Join(all, " AND ") + " WITH [" + strings.Join(vals, ", ") + "] AS v, count(*) AS c WHERE c > 1 RETURN count(*)"
		dups, err := a.base.client.count(ctx, q)
		switch {
		case err != nil:
			add("DBS-NEO4J-UNCHECKED", score.Medium, fmt.Sprintf("Could not count duplicate %s values in time (%d entries); creation fails if any exist", what, entries), "")
		case dups > 0:
			fails(fmt.Sprintf("%s of %s %s more than once, so creating the constraint fails", plural(dups, "value"), what, map[bool]string{true: "occurs", false: "occur"}[dups == 1]),
				"Merge or remove the duplicates first; find them with MATCH ... WITH value, count(*) AS c WHERE c > 1")
			return
		}
	}
	if kind == "key" || kind == "existence" {
		missing, err := a.base.client.count(ctx, match+" WHERE "+strings.Join(any, " OR ")+" RETURN count(*)")
		switch {
		case err != nil:
			add("DBS-NEO4J-UNCHECKED", score.Medium, fmt.Sprintf("Could not count %s entries missing %s in time", label, strings.Join(props, ", ")), "")
		case missing > 0:
			fails(fmt.Sprintf("%s %s %s, so creating the constraint fails", plural(missing, label+" entry"), map[bool]string{true: "lacks", false: "lack"}[missing == 1], strings.Join(props, ", ")),
				"Set the property on every entry first")
			return
		}
	}
	r := score.Low
	if entries >= bulkHigh {
		r = score.Medium
	}
	add("DBS-NEO4J-CONSTRAINT-SCAN", r, fmt.Sprintf("Creating the constraint reads all %d %s entries to validate them", entries, label), "")
}

// dataChange rates statements that write data: an unbatched write over many
// entries runs as one transaction whose changes are all held in memory.
func (a *cypherAnalysis) dataChange(toks []token, add func(string, score.Rating, string, string)) {
	write := ""
	for i, t := range toks {
		if i > 0 && toks[i-1].text == "." {
			continue
		}
		switch t.word {
		case "set", "remove", "delete", "merge", "create", "foreach":
			if write == "" || t.word == "delete" || t.word == "remove" {
				write = t.word
			}
		}
	}
	if write == "" {
		return
	}
	batched := false
	for i := 0; i+1 < len(toks); i++ {
		batched = batched || toks[i].word == "in" && toks[i+1].word == "transactions"
	}
	// The entries a statement touches: the largest label or type it matches.
	touched, over := int64(0), ""
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].text != ":" {
			continue
		}
		name := strings.Trim(toks[i+1].text, "`")
		if n := max(a.base.Labels[name], a.base.Types[name]); n > touched {
			touched, over = n, name
		}
	}
	if write == "delete" || write == "remove" {
		add("DBS-NEO4J-DROPS-DATA", score.High, fmt.Sprintf("%s removes data (up to %d %s entries) with no way back but a backup", strings.ToUpper(write), touched, over),
			"Take a backup or export first, and check the count with the same MATCH ... RETURN count(*)")
	}
	if batched || touched < bulkMedium {
		return
	}
	r := score.Medium
	if touched >= bulkHigh {
		r = score.High
	}
	limit := ""
	if a.base.TxMemoryMax != "" {
		limit = fmt.Sprintf(" (db.memory.transaction.max is %s here)", a.base.TxMemoryMax)
	}
	add("DBS-NEO4J-UNBATCHED", r, fmt.Sprintf("Writes up to %d %s entries in one transaction, which holds every change in memory until it commits and fails past the server's transaction memory limit%s",
		touched, over, limit), "Batch it: CALL { ... } IN TRANSACTIONS OF 10000 ROWS (it must run in an implicit, auto-commit transaction)")
}

// plural is "1 value" or "3 values" ("entry" becomes "entries").
func plural(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(noun, "y"))
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func wordIndex(toks []token, w string, from int) int {
	if from < 0 {
		return -1
	}
	for i := from; i < len(toks); i++ {
		if toks[i].word == w {
			return i
		}
	}
	return -1
}

// schemaName is the name after CREATE CONSTRAINT / CREATE [kind] INDEX, or "" when unnamed.
func schemaName(toks []token) string {
	i := wordIndex(toks, "constraint", 0)
	if i < 0 {
		i = wordIndex(toks, "index", 0)
	}
	if i < 0 || i+1 >= len(toks) {
		return ""
	}
	n := toks[i+1]
	if n.word == "if" || n.word == "for" || n.word == "on" {
		return ""
	}
	return strings.Trim(n.text, "`")
}

// patternName is the label or type in FOR (n:Label) or FOR ()-[r:TYPE]-().
func patternName(toks []token, from int) string {
	if from < 0 {
		return ""
	}
	for i := from; i+1 < len(toks); i++ {
		if toks[i].text == ":" {
			return strings.Trim(toks[i+1].text, "`")
		}
		if toks[i].word == "require" || toks[i].word == "options" {
			break
		}
	}
	return ""
}

// requiredProps reads n.p or (n.a, n.b) after REQUIRE.
func requiredProps(toks []token) []string {
	var out []string
	for i := 0; i+2 < len(toks) && toks[i].word != "is"; i++ {
		if toks[i+1].text == "." {
			out = append(out, strings.Trim(toks[i+2].text, "`"))
			i += 2
		}
	}
	return out
}

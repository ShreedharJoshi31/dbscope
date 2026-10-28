package clickhouse

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// Base is the server and tables migrations are checked against.
type Base struct {
	Version string
	Num     int
	// Database is what unqualified names resolve to.
	Database string
	// Tables is keyed by lower-case "database.table".
	Tables map[string]*TableInfo
}

// TableInfo is what the rules need to know about one table.
type TableInfo struct {
	Engine     string
	Replicated bool
	Rows       uint64
	Bytes      uint64
	// Columns is keyed by lower-case name.
	Columns map[string]ColumnInfo
	// PendingMutations are mutations not yet done; a new one waits behind them.
	PendingMutations uint64
}

// ColumnInfo is one column's type, compressed size and key roles.
type ColumnInfo struct {
	Type  string
	Bytes uint64
	// Key is true for columns of the sorting, primary or partition key.
	Key bool
}

// ReadBase reads table and column sizes, engines, key columns and pending
// mutations of the client's database (system.tables and system.columns are
// readable by every user; system.mutations needs its grant and is skipped
// without it).
func ReadBase(ctx context.Context, c *Client) (*Base, error) {
	var version string
	if err := c.Conn.QueryRow(ctx, `SELECT version()`).Scan(&version); err != nil {
		return nil, err
	}
	b := &Base{Version: version, Num: versionNum(version), Database: c.Database, Tables: map[string]*TableInfo{}}
	rows, err := c.Conn.Query(ctx, `SELECT database, name, engine, coalesce(total_rows, 0), coalesce(total_bytes, 0)
		FROM system.tables WHERE database = ? AND NOT is_temporary`, c.Database)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var db, name, engine string
		var n, bytes uint64
		if err := rows.Scan(&db, &name, &engine, &n, &bytes); err != nil {
			rows.Close()
			return nil, err
		}
		b.Tables[strings.ToLower(db+"."+name)] = &TableInfo{Engine: engine, Replicated: strings.HasPrefix(engine, "Replicated") || strings.HasPrefix(engine, "Shared"),
			Rows: n, Bytes: bytes, Columns: map[string]ColumnInfo{}}
	}
	rows.Close()
	rows, err = c.Conn.Query(ctx, `SELECT database, table, name, type, data_compressed_bytes,
			is_in_sorting_key OR is_in_primary_key OR is_in_partition_key
		FROM system.columns WHERE database = ?`, c.Database)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var db, table, name, typ string
		var bytes uint64
		var key bool
		if err := rows.Scan(&db, &table, &name, &typ, &bytes, &key); err != nil {
			rows.Close()
			return nil, err
		}
		if t := b.Tables[strings.ToLower(db+"."+table)]; t != nil {
			t.Columns[strings.ToLower(name)] = ColumnInfo{Type: typ, Bytes: bytes, Key: key}
		}
	}
	rows.Close()
	if rows, err := c.Conn.Query(ctx, `SELECT database, table, count() FROM system.mutations
		WHERE database = ? AND NOT is_done GROUP BY database, table`, c.Database); err == nil {
		for rows.Next() {
			var db, table string
			var n uint64
			if rows.Scan(&db, &table, &n) == nil {
				if t := b.Tables[strings.ToLower(db+"."+table)]; t != nil {
					t.PendingMutations = n
				}
			}
		}
		rows.Close()
	}
	return b, nil
}

// Analyze rates migration files against base. MergeTree ALTERs take no lock
// that stops reads or inserts, so the risks are the work a mutation does
// (measured on 26.8 and 25.8: a type change rewrites the column in every
// part, ALTER DELETE and MODIFY TTL rewrite whole parts, DROP and RENAME
// COLUMN replace every part without reading data), data that is dropped,
// and statements the server refuses (docs/ENGINES.md, "ClickHouse").
func Analyze(base *Base, files []ingest.Input) *migrate.Report {
	rep := &migrate.Report{Engine: "clickhouse", PGVersion: base.Num, Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: []string{}, Base: "live",
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	if len(base.Tables) == 0 {
		rep.Base = "none"
		rep.Warnings = append(rep.Warnings, "no tables in the base database: sizes are unknown, so rewrites are rated as medium")
	}
	a := &chAnalysis{base: base, rep: rep, altered: map[string]bool{}}
	for _, f := range files {
		for _, p := range splitScript(f.SQL) {
			st := a.statement(f.Name, p)
			rep.Statements = append(rep.Statements, st)
			rep.Rating = score.Max(rep.Rating, st.Rating)
		}
	}
	for k := range a.altered {
		rep.Diff.Altered = append(rep.Diff.Altered, k)
	}
	sort.Strings(rep.Diff.Added)
	sort.Strings(rep.Diff.Dropped)
	sort.Strings(rep.Diff.Altered)
	return rep
}

type chAnalysis struct {
	base    *Base
	rep     *migrate.Report
	altered map[string]bool
}

// piece is one statement of a script with where it starts and ends.
type piece struct {
	SQL                   string
	Line, Column, EndLine int
}

// splitScript divides a script at semicolons outside strings, quoted names and comments.
func splitScript(s string) []piece {
	var out []piece
	start, line, col := 0, 1, 1
	startLine, startCol := 1, 1
	begun := false
	flush := func(end int) {
		text := strings.TrimSpace(s[start:end])
		if begun && len(tokens(text)) > 0 {
			out = append(out, piece{SQL: text, Line: startLine, Column: startCol, EndLine: line})
		}
	}
	advance := func(i int) {
		if s[i] == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		comment := strings.HasPrefix(s[i:], "--") || c == '#' || strings.HasPrefix(s[i:], "/*")
		if !begun && !comment && !(c == ' ' || c == '\t' || c == '\n' || c == '\r') {
			begun, start, startLine, startCol = true, i, line, col
		}
		switch {
		case strings.HasPrefix(s[i:], "--") || c == '#':
			for i < len(s) && s[i] != '\n' {
				advance(i)
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			stop := len(s)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			for ; i < stop; i++ {
				advance(i)
			}
		case c == '\'' || c == '"' || c == '`':
			advance(i)
			i++
			for i < len(s) && s[i] != c {
				if s[i] == '\\' && i+1 < len(s) {
					advance(i)
					i++
				}
				advance(i)
				i++
			}
			if i < len(s) {
				advance(i)
				i++
			}
		case c == ';':
			flush(i)
			advance(i)
			i++
			start, begun = i, false
		default:
			advance(i)
			i++
		}
	}
	flush(len(s))
	return out
}

func (a *chAnalysis) statement(file string, p piece) (out migrate.Statement) {
	out = migrate.Statement{File: file, Line: p.Line, Column: p.Column, EndLine: p.EndLine, SQL: p.SQL,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	add := func(id string, r score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
		out.Rating = score.Max(out.Rating, r)
	}
	toks := tokens(p.SQL)
	w := make([]string, len(toks))
	for i, t := range toks {
		w[i] = t.word
	}
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	defer func() {
		out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
	}()

	switch {
	case at(0) == "alter" && at(1) == "table":
		display, key, next := a.tableName(toks, 2)
		out.Kind = "ALTER TABLE"
		next = skipOnCluster(toks, next)
		tbl := a.base.Tables[key]
		if tbl != nil && tbl.Replicated {
			add("DBS-CH-REPLICATED", score.Low, display+" is replicated: the change and any mutation run on every replica", "")
		}
		mutations := 0
		for _, clause := range splitClauses(toks[next:]) {
			if a.clause(&out, add, display, key, tbl, clause) {
				mutations++
			}
		}
		if mutations > 0 && tbl != nil && tbl.PendingMutations > 0 {
			add("DBS-CH-MUTATION-QUEUE", score.Medium, fmt.Sprintf("%s already has %d unfinished mutations; mutations run in order, so this one waits behind them",
				display, tbl.PendingMutations), "Check system.mutations and let them finish, or kill stuck ones, first")
		}
		if mutations > 0 && !strings.Contains(strings.ToLower(p.SQL), "mutations_sync") {
			add("DBS-CH-ASYNC-MUTATION", score.Low, "The ALTER returns before its mutation finishes; the migration tool reports success while parts are still being rewritten",
				"Add SETTINGS mutations_sync = 2 to wait, or watch system.mutations")
		}
	case at(0) == "delete" && at(1) == "from":
		display, key, _ := a.tableName(toks, 2)
		out.Kind, out.Work = "DELETE", "rewrite"
		a.altered[display] = true
		tbl := a.base.Tables[key]
		add("DBS-CH-LIGHTWEIGHT-DELETE", a.tier(tableBytes(tbl), tbl != nil), fmt.Sprintf("Lightweight DELETE masks matching rows in %s (%s); the rows still occupy disk until merges drop them, "+
			"and every query filters the mask until then", display, sizeText(tableBytes(tbl), tbl != nil)), "")
	case at(0) == "truncate":
		i := 1
		if at(1) == "table" {
			i = 2
		}
		display, _, _ := a.tableName(toks, skipIfExists(toks, i))
		out.Kind = "TRUNCATE"
		add("DBS-CH-DROPS-DATA", score.High, "TRUNCATE deletes every row of "+display, "")
	case at(0) == "drop" && (at(1) == "table" || at(1) == "view" || at(1) == "dictionary" || at(1) == "database"):
		display, _, _ := a.tableName(toks, skipIfExists(toks, 2))
		out.Kind = "DROP " + strings.ToUpper(at(1))
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, display)
		add("DBS-CH-DROPS-DATA", score.High, fmt.Sprintf("DROP %s %s removes it and its data", strings.ToUpper(at(1)), display), "")
	case at(0) == "optimize":
		display, key, _ := a.tableName(toks, 2)
		out.Kind, out.Work = "OPTIMIZE", "rewrite"
		tbl := a.base.Tables[key]
		if contains(w, "final") {
			add("DBS-CH-OPTIMIZE-FINAL", score.Max(score.Medium, a.tier(tableBytes(tbl), tbl != nil)), fmt.Sprintf("OPTIMIZE ... FINAL merges every partition of %s (%s) into one part each, "+
				"reading and writing all of it", display, sizeText(tableBytes(tbl), tbl != nil)), "Let background merges do it, or OPTIMIZE one PARTITION at a time")
		}
	case at(0) == "create":
		i := 1
		for i < len(toks) && (toks[i].word == "or" || toks[i].word == "replace" || toks[i].word == "materialized" || toks[i].word == "view" ||
			toks[i].word == "table" || toks[i].word == "dictionary" || toks[i].word == "if" || toks[i].word == "not" || toks[i].word == "exists") {
			i++
		}
		out.Kind = "CREATE " + strings.ToUpper(strings.Join(slices.DeleteFunc(slices.Clone(w[1:i]), func(s string) bool {
			return s == "or" || s == "replace" || s == "if" || s == "not" || s == "exists"
		}), " "))
		display, _, _ := a.tableName(toks, i)
		a.rep.Diff.Added = append(a.rep.Diff.Added, display)
		if contains(w, "populate") {
			add("DBS-CH-POPULATE", score.High, "POPULATE fills the view from the existing data, and rows inserted while it runs are missed by the view",
				"Create the view without POPULATE, then INSERT INTO its target SELECT ... for the old data, bounded by a time filter")
		}
	default:
		out.Kind = strings.ToUpper(strings.Join(w[:min(2, len(w))], " "))
	}
	return out
}

// clause rates one ALTER TABLE clause and reports whether it starts a mutation.
func (a *chAnalysis) clause(out *migrate.Statement, add func(string, score.Rating, string, string), display, key string, tbl *TableInfo, c []token) bool {
	w := make([]string, len(c))
	for i, t := range c {
		w[i] = t.word
	}
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	colAt := func(i int) (string, int) {
		i = skipIfExists(c, i)
		if i < len(c) {
			return unquote(c[i].text), i + 1
		}
		return "", i
	}
	column := func(name string) (ColumnInfo, bool) {
		if tbl == nil {
			return ColumnInfo{}, false
		}
		col, ok := tbl.Columns[strings.ToLower(name)]
		return col, ok
	}
	a.altered[display] = true
	rewrite := func(id, what string, bytes uint64, known bool, alt string) {
		out.Work = "rewrite"
		add(id, a.tier(bytes, known), fmt.Sprintf("%s: a mutation rewrites it in every part of %s (%s to rewrite)", what, display, sizeText(bytes, known)), alt)
	}
	switch {
	case at(0) == "add" && at(1) == "column":
		name, _ := colAt(2)
		a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: name, Change: "added"})
	case at(0) == "add" && (at(1) == "index" || at(1) == "projection"):
		add("DBS-CH-NOT-MATERIALIZED", score.Low, fmt.Sprintf("ADD %s applies only to parts written from now on; existing parts are not covered until MATERIALIZE %s, "+
			"which rewrites them", strings.ToUpper(at(1)), strings.ToUpper(at(1))), "")
	case at(0) == "drop" && at(1) == "column":
		name, _ := colAt(2)
		a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: name, Change: "dropped"})
		if col, ok := column(name); ok && col.Key {
			add("DBS-CH-KEY-COLUMN", score.High, "DROP COLUMN "+name+" fails: the column is part of the table's sorting, primary or partition key", "")
			return false
		}
		out.Work = "rewrite"
		add("DBS-CH-DROPS-DATA", score.High, fmt.Sprintf("DROP COLUMN %s removes the column and its data; a mutation replaces every part of %s without reading data", name, display),
			"Stop reading and writing the column first, and keep a backup of it if it may be needed")
		return true
	case at(0) == "clear" && at(1) == "column":
		name, _ := colAt(2)
		add("DBS-CH-DROPS-DATA", score.High, fmt.Sprintf("CLEAR COLUMN %s resets the column to its default in the partition, discarding its values", name), "")
		return true
	case at(0) == "rename" && at(1) == "column":
		name, next := colAt(2)
		to := ""
		if next+1 < len(c) {
			to = unquote(c[next+1].text)
		}
		a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: name, Change: "renamed", To: to})
		if col, ok := column(name); ok && col.Key {
			add("DBS-CH-KEY-COLUMN", score.High, "RENAME COLUMN "+name+" fails: the column is part of the table's sorting, primary or partition key", "")
			return false
		}
		add("DBS-CH-RENAME", score.Medium, fmt.Sprintf("RENAME COLUMN %s breaks queries and inserts that use the old name, and a mutation replaces every part of %s (no data is read)", name, display),
			"Add the new column as ALIAS of the old one, move clients over, then drop the old name")
		return true
	case at(0) == "modify" && at(1) == "column":
		name, next := colAt(2)
		typ := typeTokens(c[next:])
		col, known := column(name)
		if typ == "" || known && normalizeType(typ) == normalizeType(col.Type) {
			return false // default, comment, codec or TTL only: metadata
		}
		a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: name, Change: "altered", To: typ})
		if known && col.Key {
			add("DBS-CH-KEY-COLUMN", score.High, fmt.Sprintf("MODIFY COLUMN %s %s is refused unless the new type stores the same values: the column is in a key", name, typ),
				"Add a new column, backfill it, and rebuild the table with the new key")
			return false
		}
		rewrite("DBS-CH-COLUMN-REWRITE", "MODIFY COLUMN "+name+" "+typ+" converts the column", col.Bytes, known, "Run it when the table is quiet, with disk space for a second copy of the column while parts are rewritten")
		return true
	case at(0) == "modify" && at(1) == "order" && at(2) == "by":
		add("DBS-CH-ORDER-BY", score.Medium, "MODIFY ORDER BY may only append columns added in the same ALTER; any existing column in the new expressions makes it fail", "")
	case at(0) == "modify" && at(1) == "ttl":
		rewrite("DBS-CH-TTL-REWRITE", "MODIFY TTL recalculates TTL", tableBytes(tbl), tbl != nil,
			"SET materialize_ttl_after_modify = 0 for this ALTER, then MATERIALIZE TTL IN PARTITION one partition at a time")
		return true
	case at(0) == "materialize":
		what := strings.ToUpper(at(1))
		bytes := tableBytes(tbl)
		if at(1) == "column" {
			name, _ := colAt(2)
			col, _ := column(name)
			bytes = col.Bytes
		}
		rewrite("DBS-CH-MATERIALIZE", "MATERIALIZE "+what+" builds it for existing parts", bytes, tbl != nil, "Add IN PARTITION to do one partition at a time")
		return true
	case at(0) == "update":
		rewrite("DBS-CH-UPDATE", "ALTER UPDATE changes rows with a mutation, not a row update", tableBytes(tbl), tbl != nil,
			"Prefer modelling updates as inserts (ReplacingMergeTree), or bound the WHERE to few partitions")
		return true
	case at(0) == "delete":
		rewrite("DBS-CH-ALTER-DELETE", "ALTER DELETE removes rows by rewriting whole parts", tableBytes(tbl), tbl != nil,
			"Use lightweight DELETE FROM, which writes only a row mask, or DROP PARTITION when whole partitions go")
		return true
	case (at(0) == "drop" || at(0) == "detach") && (at(1) == "partition" || at(1) == "part"):
		add("DBS-CH-DROPS-DATA", score.High, fmt.Sprintf("%s %s removes that data from %s at once", strings.ToUpper(at(0)), strings.ToUpper(at(1)), display), "")
	}
	return false
}

// typeTokens returns the type text in a MODIFY COLUMN clause after the
// column name, or "" when the clause sets only a default, comment, codec, TTL or position.
func typeTokens(c []token) string {
	stop := map[string]bool{"default": true, "materialized": true, "alias": true, "ephemeral": true, "comment": true, "codec": true,
		"ttl": true, "settings": true, "remove": true, "first": true, "after": true, "modify": true, "reset": true}
	var parts []string
	depth := 0
	for _, t := range c {
		if depth == 0 && stop[t.word] {
			break
		}
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		}
		parts = append(parts, t.text)
	}
	return strings.Join(parts, "")
}

func normalizeType(t string) string {
	return strings.ToLower(strings.ReplaceAll(t, " ", ""))
}

// splitClauses divides the clauses of an ALTER TABLE at top-level commas.
func splitClauses(toks []token) [][]token {
	var out [][]token
	depth, start := 0, 0
	for i, t := range toks {
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				out = append(out, toks[start:i])
				start = i + 1
			}
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// tableName reads a possibly database-qualified name at toks[i].
func (a *chAnalysis) tableName(toks []token, i int) (display, key string, next int) {
	if i >= len(toks) {
		return "", "", i
	}
	db, t := a.base.Database, unquote(toks[i].text)
	next = i + 1
	if next+1 < len(toks) && toks[next].text == "." {
		db, t = t, unquote(toks[next+1].text)
		next += 2
	}
	return db + "." + t, strings.ToLower(db + "." + t), next
}

func skipOnCluster(toks []token, i int) int {
	if i+2 < len(toks) && toks[i].word == "on" && toks[i+1].word == "cluster" {
		return i + 3
	}
	return i
}

func skipIfExists(toks []token, i int) int {
	if i+1 < len(toks) && toks[i].word == "if" && toks[i+1].word == "exists" {
		return i + 2
	}
	return i
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '`' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func contains(w []string, x string) bool {
	for _, s := range w {
		if s == x {
			return true
		}
	}
	return false
}

func tableBytes(t *TableInfo) uint64 {
	if t == nil {
		return 0
	}
	return t.Bytes
}

// tier rates rewriting bytes of compressed data: under 1 GiB low, under 50 GiB medium, above high.
func (a *chAnalysis) tier(bytes uint64, known bool) score.Rating {
	switch {
	case !known:
		return score.Medium
	case bytes >= 50<<30:
		return score.High
	case bytes >= 1<<30:
		return score.Medium
	}
	return score.Low
}

func sizeText(bytes uint64, known bool) string {
	if !known {
		return "size unknown"
	}
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GiB compressed", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MiB compressed", float64(bytes)/(1<<20))
	}
	return fmt.Sprintf("%d bytes compressed", bytes)
}

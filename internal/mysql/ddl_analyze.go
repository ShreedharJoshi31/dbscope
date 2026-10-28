package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/schema"
)

// Base is the server and schema migrations are checked against.
type Base struct {
	Server Server
	// Schema is the database unqualified names resolve to.
	Schema string
	// Tables is keyed by lower-case "schema.table"; empty without a live base.
	Tables map[string]*TableInfo
	// Source is "live", or "none" when only a server version was given.
	Source string
}

// ReadBase reads table sizes, row formats and column types for the rules.
func ReadBase(ctx context.Context, db *sql.DB, schemas []string, fallback string) (*Base, error) {
	m, err := Introspect(ctx, db, schemas, fallback)
	if err != nil {
		return nil, err
	}
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	b := &Base{Server: srv, Schema: fallback, Tables: map[string]*TableInfo{}, Source: "live"}
	key := func(s, t string) string { return strings.ToLower(s + "." + t) }
	for _, r := range m.Relations {
		if r.Kind != schema.KindTable || r.Stub {
			continue
		}
		t := &TableInfo{Columns: map[string]ColumnInfo{}, PrimaryKey: len(r.PrimaryKey) > 0}
		for _, c := range r.Columns {
			t.Columns[strings.ToLower(c.Name)] = ColumnInfo{Type: c.Type, Nullable: c.Nullable, BytesPerChar: 1}
		}
		for _, i := range r.Indexes {
			t.HasFulltext = t.HasFulltext || i.Method == "fulltext"
		}
		for _, fk := range r.ForeignKeys {
			t.ForeignKeys++
			t.CascadingFKs = t.CascadingFKs || fk.OnDelete == "cascade" || fk.OnUpdate == "cascade" || fk.OnDelete == "set null" || fk.OnUpdate == "set null"
		}
		b.Tables[key(r.Schema, r.Name)] = t
	}
	for _, r := range m.Relations {
		for _, fk := range r.ForeignKeys {
			if t := b.Tables[key(fk.Ref.Schema, fk.Ref.Name)]; t != nil {
				t.ForeignKeys++
			}
		}
	}
	if len(schemas) == 0 {
		schemas = []string{fallback}
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(schemas)), ",") + ")"
	args := make([]any, len(schemas))
	for i, s := range schemas {
		args[i] = s
	}
	rows, err := db.QueryContext(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_ROWS, coalesce(DATA_LENGTH, 0) + coalesce(INDEX_LENGTH, 0), coalesce(ROW_FORMAT, '')
		FROM information_schema.TABLES WHERE TABLE_TYPE = 'BASE TABLE' AND TABLE_SCHEMA IN `+in, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s, t, format string
		var n sql.NullInt64
		var bytes int64
		if err := rows.Scan(&s, &t, &n, &bytes, &format); err != nil {
			rows.Close()
			return nil, err
		}
		if ti := b.Tables[key(s, t)]; ti != nil {
			ti.Rows, ti.Bytes, ti.Sized, ti.RowFormat = n.Int64, bytes, n.Valid, strings.ToLower(format)
		}
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, CHARACTER_MAXIMUM_LENGTH, CHARACTER_OCTET_LENGTH
		FROM information_schema.COLUMNS WHERE CHARACTER_MAXIMUM_LENGTH > 0 AND TABLE_SCHEMA IN `+in, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s, t, c string
		var chars, octets int64
		if err := rows.Scan(&s, &t, &c, &chars, &octets); err != nil {
			return nil, err
		}
		if ti := b.Tables[key(s, t)]; ti != nil {
			if col, ok := ti.Columns[strings.ToLower(c)]; ok {
				col.BytesPerChar = int(max(1, octets/chars))
				ti.Columns[strings.ToLower(c)] = col
			}
		}
	}
	return b, rows.Err()
}

// Analyze rates migration files against base: the algorithm each ALTER
// gets, whether writes continue, and how much work it does on tables of the
// base's sizes (docs/ENGINES.md, "MySQL and MariaDB").
func Analyze(base *Base, files []ingest.Input) *migrate.Report {
	srv := base.Server
	rep := &migrate.Report{Engine: srv.Flavor, PGVersion: srv.Num, Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: []string{}, Base: base.Source,
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	label := map[string]string{"mysql": "MySQL", "mariadb": "MariaDB"}[srv.Flavor]
	if base.Source == "none" {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("no base schema: rules assume %s %s, and table sizes and column types are unknown, so type changes are assumed to copy the table", label, srv.Version))
	}
	a := &ddlAnalysis{base: base, sess: Session{ForeignKeyChecks: true}, altered: map[string]bool{}, rep: rep}
	for _, f := range files {
		for _, p := range Split(f.SQL) {
			st := a.statement(f.Name, p)
			rep.Statements = append(rep.Statements, st)
			rep.Rating = score.Max(rep.Rating, st.Rating)
		}
	}
	sort.Strings(rep.Diff.Added)
	sort.Strings(rep.Diff.Dropped)
	for k := range a.altered {
		rep.Diff.Altered = append(rep.Diff.Altered, k)
	}
	sort.Strings(rep.Diff.Altered)
	return rep
}

type ddlAnalysis struct {
	base    *Base
	sess    Session
	altered map[string]bool
	rep     *migrate.Report
}

// tableName reads a possibly schema-qualified name starting at toks[i] and
// returns it, the "schema.table" key and the index after it.
func (a *ddlAnalysis) tableName(toks []Token, i int) (display, key string, next int) {
	if i >= len(toks) {
		return "", "", i
	}
	s, t := a.base.Schema, name(toks[i])
	next = i + 1
	if next+1 < len(toks) && toks[next].Text == "." {
		s, t = t, name(toks[next+1])
		next += 2
	}
	if s == "" {
		return t, strings.ToLower(t), next // no base, so no default database
	}
	return s + "." + t, strings.ToLower(s + "." + t), next
}

func (a *ddlAnalysis) statement(file string, p Piece) migrate.Statement {
	out := migrate.Statement{File: file, Line: p.Line, Column: p.Column, EndLine: p.EndLine, SQL: p.SQL,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	add := func(id string, r score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
		out.Rating = score.Max(out.Rating, r)
	}
	toks := Tokens(p.SQL)
	w := words(toks)
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	finish := func() migrate.Statement {
		out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
		return out
	}

	switch at(0) {
	case "set":
		a.session(w, toks)
		out.Kind = "SET"
		return finish()
	case "alter":
		i := 1
		for i < len(toks) && (toks[i].Word == "online" || toks[i].Word == "ignore") {
			i++
		}
		if i >= len(toks) || toks[i].Word != "table" {
			out.Kind = "ALTER " + strings.ToUpper(at(1))
			return finish()
		}
		display, key, next := a.tableName(toks, i+1)
		wait := false
		if next < len(toks) && (toks[next].Word == "wait" || toks[next].Word == "nowait") {
			wait = true
			next++
			if next < len(toks) && toks[next].Text[0] >= '0' && toks[next].Text[0] <= '9' {
				next++
			}
		}
		out.Kind = "ALTER TABLE"
		a.alter(&out, add, display, key, clauseSplit(toks[next:]), wait)
		return finish()
	case "create":
		kind := ""
		switch {
		case at(1) == "index":
			kind = "add index"
		case at(1) == "unique" && at(2) == "index":
			kind = "add unique"
		case at(1) == "fulltext":
			kind = "add fulltext"
		case at(1) == "spatial":
			kind = "add spatial"
		}
		if kind == "" {
			out.Kind = "CREATE " + strings.ToUpper(at(1))
			if at(1) == "table" {
				i := 2
				for i < len(toks) && (toks[i].Word == "if" || toks[i].Word == "not" || toks[i].Word == "exists") {
					i++
				}
				display, _, _ := a.tableName(toks, i)
				a.rep.Diff.Added = append(a.rep.Diff.Added, display)
			}
			return finish()
		}
		on := indexOf(toks, "on")
		if on < 0 {
			out.Kind = "CREATE INDEX"
			return finish()
		}
		display, key, next := a.tableName(toks, on+1)
		out.Kind = strings.ToUpper(strings.Replace(kind, "add", "CREATE", 1)) + " INDEX"
		if kind == "add index" {
			out.Kind = "CREATE INDEX"
		}
		// CREATE INDEX ... ALGORITHM=x LOCK=y: the options follow the column list.
		clauses := [][]Token{toks[:on]}
		clauses[0] = append([]Token{{Text: "add", Word: "add"}}, toks[1:on]...)
		clauses = append(clauses, optionClauses(toks[next:])...)
		a.alter(&out, add, display, key, clauses, hasWord(toks, "wait", "nowait"))
		return finish()
	case "drop":
		switch at(1) {
		case "index":
			on := indexOf(toks, "on")
			if on > 0 {
				display, key, next := a.tableName(toks, on+1)
				out.Kind = "DROP INDEX"
				clauses := append([][]Token{toks[:on]}, optionClauses(toks[next:])...)
				a.alter(&out, add, display, key, clauses, hasWord(toks, "wait", "nowait"))
				return finish()
			}
		case "table":
			out.Kind = "DROP TABLE"
			i := 2
			for i < len(toks) && (toks[i].Word == "if" || toks[i].Word == "exists" || toks[i].Word == "temporary") {
				i++
			}
			for i < len(toks) {
				display, key, next := a.tableName(toks, i)
				a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, display)
				out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: "METADATA EXCLUSIVE"})
				_ = key
				i = next
				if i < len(toks) && toks[i].Text == "," {
					i++
				} else {
					break
				}
			}
			a.mdl(&out, add, hasWord(toks, "wait", "nowait"))
			return finish()
		}
		out.Kind = "DROP " + strings.ToUpper(at(1))
		return finish()
	case "rename":
		out.Kind = "RENAME TABLE"
		display, _, _ := a.tableName(toks, 2)
		out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: "METADATA EXCLUSIVE"})
		a.mdl(&out, add, hasWord(toks, "wait", "nowait"))
		return finish()
	case "optimize", "truncate":
		i := 1
		if at(1) == "table" || at(1) == "no_write_to_binlog" || at(1) == "local" {
			i = 2
		}
		display, key, _ := a.tableName(toks, i)
		out.Kind = strings.ToUpper(at(0)) + " TABLE"
		if at(0) == "truncate" {
			out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: "METADATA EXCLUSIVE"})
			add("DBS-MYSQL-TRUNCATE", score.High, "TRUNCATE deletes every row of "+display+"; it is not a schema-only change", "")
			a.mdl(&out, add, false)
			return finish()
		}
		a.alter(&out, add, display, key, [][]Token{{{Text: "force", Word: "force"}}}, false)
		return finish()
	case "update", "delete", "insert", "replace":
		out.Kind = strings.ToUpper(at(0))
		return finish()
	}
	out.Kind = strings.ToUpper(at(0))
	return finish()
}

// session follows SET statements that change how later DDL runs.
func (a *ddlAnalysis) session(w []string, toks []Token) {
	val := ""
	if len(toks) > 0 {
		val = strings.ToLower(strings.Trim(toks[len(toks)-1].Text, "'\""))
	}
	for _, x := range w {
		switch strings.TrimPrefix(strings.TrimPrefix(x, "@@session."), "@@") {
		case "foreign_key_checks":
			a.sess.ForeignKeyChecks = !(val == "0" || val == "off" || val == "false")
		case "lock_wait_timeout":
			n, _ := strconv.Atoi(val)
			a.sess.LockWaitTimeout = n
		}
	}
}

// mdl adds the metadata-lock finding to a DDL statement that may queue.
func (a *ddlAnalysis) mdl(out *migrate.Statement, add func(string, score.Rating, string, string), wait bool) {
	if wait || (a.sess.LockWaitTimeout > 0 && a.sess.LockWaitTimeout <= 60) {
		out.LockTimeout = true
		return
	}
	alt := "SET SESSION lock_wait_timeout = 5 before it and retry on timeout"
	if a.base.Server.Flavor == "mariadb" {
		alt = "Use ALTER TABLE ... WAIT 5 (or SET SESSION lock_wait_timeout = 5) and retry on timeout"
	}
	add("DBS-MYSQL-LOCK-WAIT", score.Medium,
		"DDL needs an exclusive metadata lock, at least briefly; behind a long-running transaction it waits, and every later query on the table queues behind it. lock_wait_timeout defaults to one year", alt)
}

func (a *ddlAnalysis) alter(out *migrate.Statement, add func(string, score.Rating, string, string), display, key string, clauses [][]Token, wait bool) {
	srv := a.base.Server
	maria := srv.Flavor == "mariadb"
	tbl := a.base.Tables[key]
	a.altered[display] = true
	var kinds []string
	for _, c := range clauses {
		if len(c) > 0 {
			kinds = append(kinds, clauseKind(words(c)))
		}
	}
	plan := Plan{Algo: Instant, DML: true}
	reqAlgo, reqLock := "", ""
	fails := false // the server will reject the statement, so it does no work
	var whats []string
	for i, c := range clauses {
		if len(c) == 0 {
			continue
		}
		k := kinds[min(i, len(kinds)-1)]
		switch k {
		case "algorithm", "lock":
			v := optionValue(c)
			if k == "algorithm" {
				reqAlgo = v
			} else {
				reqLock = v
			}
			continue
		}
		p := planClause(srv, tbl, a.sess, k, c, kinds)
		a.diff(display, k, c)
		for _, n := range p.Notes {
			r := score.Medium
			switch {
			case n.Severe:
				r = score.High
			case n.Rule == "DBS-MYSQL-ROW-VERSIONS":
				r = score.Low
			}
			add(n.Rule, r, n.Message, "")
		}
		whats = append(whats, p.What)
		plan = worse(plan, p)
	}

	// An explicit ALGORITHM is exact on MySQL and a floor on MariaDB (COPY is exact on both).
	if reqAlgo != "" && reqAlgo != "default" {
		want := map[string]Algo{"instant": Instant, "nocopy": Nocopy, "inplace": Inplace, "copy": Copy}[reqAlgo]
		switch {
		case reqAlgo == "nocopy" && !maria:
			fails = true
			add("DBS-MYSQL-ALGORITHM", score.High, "MySQL has no ALGORITHM=NOCOPY (it is MariaDB's); the statement fails", "Use ALGORITHM=INPLACE or INSTANT")
		case want == Copy:
			plan.Algo, plan.Rebuild, plan.DML = Copy, true, maria && srv.Num >= 110200 && (tbl == nil || !tbl.CascadingFKs)
		case plan.Algo > want:
			fails = true
			add("DBS-MYSQL-ALGORITHM", score.High,
				fmt.Sprintf("ALGORITHM=%s is not possible here (it needs %s), so the statement fails", strings.ToUpper(reqAlgo), plan.Algo),
				"Leave ALGORITHM out to let the server pick, or ask for "+plan.Algo.String())
		case !maria && plan.Algo < want:
			// MySQL uses exactly what was asked: INPLACE for an instant change rebuilds the table.
			if want >= Inplace && plan.Algo == Instant && containsAnyKind(kinds, "add column", "drop column") {
				plan.Rebuild = true
			}
			plan.Algo = want
		}
	}
	switch reqLock {
	case "none":
		if !plan.DML {
			fails = true
			add("DBS-MYSQL-LOCK-NONE", score.High, "LOCK=NONE is not possible here, because the operation blocks writes, so the statement fails", "Leave LOCK out, or use an online schema change tool")
		}
	case "shared", "exclusive":
		plan.DML = false
	}

	if fails {
		a.mdl(out, add, wait)
		return
	}
	mode := "METADATA EXCLUSIVE"
	if !plan.DML {
		mode = "SHARED (BLOCKS WRITES)"
	}
	out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: mode})
	switch {
	case plan.Rebuild:
		out.Work = "rewrite"
	case plan.Index:
		out.Work = "index_build"
	}
	if tbl != nil && tbl.Sized {
		b := tbl.Bytes
		out.SizeBytes = &b
	}
	a.sizeRules(out, add, plan, display, tbl, strings.Join(whats, ", "))
	a.mdl(out, add, wait)
}

// sizeRules rates the work an ALTER does by the table's size.
func (a *ddlAnalysis) sizeRules(out *migrate.Statement, add func(string, score.Rating, string, string), plan Plan, display string, tbl *TableInfo, what string) {
	if out.Work == "none" && (plan.DML || plan.Algo == Instant) {
		return // metadata only; the metadata lock rule covers the brief exclusive lock
	}
	size := "of unknown size"
	tier := 1 // 0 small, 1 unknown or medium, 2 large
	if tbl != nil && tbl.Sized {
		size = fmt.Sprintf("%d rows, %s", tbl.Rows, bytesText(tbl.Bytes))
		switch {
		case tbl.Rows >= 1_000_000 || tbl.Bytes >= 1<<30:
			tier = 2
		case tbl.Rows >= 100_000 || tbl.Bytes >= 100<<20:
			tier = 1
		default:
			tier = 0
		}
	}
	verb := map[string]string{"rewrite": "rebuilds", "index_build": "builds an index on", "none": "changes"}[out.Work]
	alt := "Run it when writes can pause, or use " + a.oscTool(tbl)
	if !plan.DML {
		r := []score.Rating{score.Medium, score.High, score.Critical}[tier]
		if tbl == nil || !tbl.Sized {
			r = score.High
		}
		add("DBS-MYSQL-BLOCKS-WRITES", r,
			fmt.Sprintf("%s (ALGORITHM=%s) %s %s (%s) and blocks writes to it for the whole time", what, plan.Algo, verb, display, size), alt)
		return
	}
	r := []score.Rating{score.Low, score.Medium, score.High}[tier]
	add("DBS-MYSQL-TABLE-WORK", r,
		fmt.Sprintf("%s (ALGORITHM=%s) %s %s (%s); writes continue, but replicas apply it only after it finishes, so they fall behind by its duration", what, plan.Algo, verb, display, size), alt)
}

// oscTool names the online schema change tool that fits the table.
func (a *ddlAnalysis) oscTool(tbl *TableInfo) string {
	switch {
	case tbl != nil && !tbl.PrimaryKey:
		return "an online schema change tool after adding a primary key (gh-ost and pt-online-schema-change both need one)"
	case tbl != nil && tbl.ForeignKeys > 0:
		return "pt-online-schema-change with --alter-foreign-keys-method (gh-ost does not support foreign keys); neither tool works on tables with triggers"
	}
	return "gh-ost (needs ROW binlogs) or pt-online-schema-change; neither works on tables with triggers"
}

// diff records the column-level change a clause makes.
func (a *ddlAnalysis) diff(display, kind string, toks []Token) {
	col, to, _ := columnOf(kind, toks)
	change := ""
	switch kind {
	case "add column":
		change = "added"
	case "drop column":
		change = "dropped"
	case "modify column":
		change = "altered"
	case "change column", "rename column":
		change = "altered"
		if !strings.EqualFold(col, to) {
			change = "renamed"
		}
	default:
		return
	}
	cc := apply.ColumnChange{Relation: display, Column: col, Change: change}
	if change == "renamed" {
		cc.To = to
	}
	a.rep.Diff.Columns = append(a.rep.Diff.Columns, cc)
}

func containsAnyKind(kinds []string, want ...string) bool {
	for _, w := range want {
		if containsKind(kinds, w) {
			return true
		}
	}
	return false
}

// optionValue is the value of an ALGORITHM or LOCK option: its first word after the keyword.
func optionValue(toks []Token) string {
	for _, t := range toks[1:] {
		if t.Word != "" {
			return t.Word
		}
	}
	return ""
}

func indexOf(toks []Token, word string) int {
	for i, t := range toks {
		if t.Word == word {
			return i
		}
	}
	return -1
}

func hasWord(toks []Token, ws ...string) bool {
	for _, t := range toks {
		for _, w := range ws {
			if t.Word == w {
				return true
			}
		}
	}
	return false
}

// optionClauses picks ALGORITHM= and LOCK= out of CREATE/DROP INDEX's tail.
func optionClauses(toks []Token) [][]Token {
	var out [][]Token
	for i, t := range toks {
		if t.Word == "algorithm" || t.Word == "lock" {
			end := min(i+3, len(toks))
			out = append(out, toks[i:end])
		}
	}
	return out
}

func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

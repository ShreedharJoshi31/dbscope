package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/schema"
)

// Base is the schema migrations are checked against (docs/ENGINES.md).
type Base struct {
	// DDL rebuilds the schema in a scratch database, in creation order.
	DDL []string
	// Rows is a row count or estimate per table; a missing table is unknown.
	Rows map[string]int64
	// Source is "live" for a database file and "dump" for a schema script.
	Source string
}

// countBudget bounds how long ReadBase spends counting rows of tables that
// have no ANALYZE estimate; the rest stay unknown.
const countBudget = 2 * time.Second

// ReadBase reads the schema and table sizes of an open database file.
func ReadBase(ctx context.Context, db *sql.DB) (*Base, error) {
	m, err := Introspect(ctx, db)
	if err != nil {
		return nil, err
	}
	// Shadow tables are created by their virtual table; replaying them would collide.
	rows, err := db.QueryContext(ctx, `SELECT s.sql FROM sqlite_schema s
		LEFT JOIN pragma_table_list l ON l.schema = 'main' AND l.name = s.name
		WHERE s.sql IS NOT NULL AND s.name NOT LIKE 'sqlite\_%' ESCAPE '\' AND coalesce(l.type, '') <> 'shadow'
		ORDER BY s.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	b := &Base{Rows: map[string]int64{}, Source: "live"}
	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			return nil, err
		}
		b.DDL = append(b.DDL, ddl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(countBudget)
	for _, r := range m.Relations {
		switch {
		case r.Kind != schema.KindTable:
		case r.RowEstimate != nil:
			b.Rows[r.Name] = *r.RowEstimate
		case time.Now().Before(deadline):
			cctx, cancel := context.WithDeadline(ctx, deadline)
			var n int64
			err := db.QueryRowContext(cctx, `SELECT count(*) FROM "`+strings.ReplaceAll(r.Name, `"`, `""`)+`"`).Scan(&n)
			cancel()
			if err == nil {
				b.Rows[r.Name] = n
			}
		}
	}
	return b, nil
}

// BaseFromDump takes a schema script, such as the sqlite3 shell's .schema
// output. Its tables have no sizes.
func BaseFromDump(script string) *Base {
	b := &Base{Rows: map[string]int64{}, Source: "dump"}
	for _, p := range Split(script) {
		b.DDL = append(b.DDL, p.SQL)
	}
	return b
}

// statement is one migration statement with what classify learned about it.
type statement struct {
	file  string
	piece Piece
	kind  string
	words []string // lower-cased words outside strings and comments
	table string   // the table it acts on, unquoted, without "main."
	inTx  bool     // inside an explicit BEGIN or SAVEPOINT of the file
}

// Analyze checks migration files against base. Statements run, in order,
// against a scratch in-memory copy of the base schema with no rows, so
// SQLite itself decides what is valid; rules then rate what is valid by
// the work it does on tables of the base's sizes.
func Analyze(ctx context.Context, base *Base, files []ingest.Input) (*migrate.Report, error) {
	scratch, err := openScratch(ctx)
	if err != nil {
		return nil, err
	}
	defer scratch.Close()

	rep := &migrate.Report{Engine: "sqlite", Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: []string{}, Base: base.Source}
	for _, ddl := range base.DDL {
		if _, err := scratch.ExecContext(ctx, ddl); err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("the base schema statement %q could not be replayed (%v); statements that depend on it may be reported as rejected", firstLine(ddl), err))
		}
	}
	before, err := Introspect(ctx, scratch.conn)
	if err != nil {
		return nil, err
	}
	if v, n, err := Version(ctx, scratch.conn); err == nil {
		rep.PGVersion = n
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("checked with SQLite %s; the version your application links decides which ALTER forms it accepts", v))
	}
	if len(base.Rows) == 0 {
		rep.Warnings = append(rep.Warnings, "table sizes are unknown, so the cost of rewrites and index builds is not rated by size")
	}

	a := &analysis{base: base, before: before, scratch: scratch, ctx: ctx}
	for _, f := range files {
		renamedAway := map[string]bool{}
		for _, p := range Split(f.SQL) {
			st := classify(f.Name, p, a.inTx)
			a.trackTx(st)
			out := a.check(st, renamedAway)
			rep.Statements = append(rep.Statements, out)
			rep.Rating = score.Max(rep.Rating, out.Rating)
		}
		if a.inTx {
			rep.Warnings = append(rep.Warnings, f.Name+" ends inside a transaction; dbscope committed it before the next file")
			scratch.exec(ctx, "COMMIT")
			a.inTx = false
		}
	}
	after, err := Introspect(ctx, scratch.conn)
	if err != nil {
		return nil, err
	}
	rep.Diff = diff(before, after)
	return rep, nil
}

type analysis struct {
	ctx     context.Context
	base    *Base
	before  *schema.Model
	scratch *scratchDB
	inTx    bool
	fkOff   bool // the migration turned foreign keys off outside a transaction
}

func (a *analysis) trackTx(st *statement) {
	switch st.kind {
	case "BEGIN", "SAVEPOINT":
		a.inTx = true
	case "COMMIT", "END", "ROLLBACK":
		if !(st.kind == "ROLLBACK" && slices.Contains(st.words, "to")) {
			a.inTx = false
		}
	}
}

func (a *analysis) check(st *statement, renamedAway map[string]bool) migrate.Statement {
	out := migrate.Statement{File: st.file, Line: st.piece.Line, Column: st.piece.Column, EndLine: st.piece.EndLine,
		SQL: st.piece.SQL, Kind: st.kind, Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	if st.inTx {
		out.Tx = "explicit"
	}
	add := func(id string, r score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
	}
	rows, sized := a.base.Rows[st.table]

	// Rules that need the statement before it runs: what it replaces or drops.
	switch st.kind {
	case "DROP TABLE":
		a.dropTableRules(st, add)
	case "CREATE TABLE":
		if renamedAway[st.table] {
			add("DBS-SQLITE-RENAME-FIRST", score.High,
				fmt.Sprintf("%s was renamed away earlier in this file and is now created again: the rename-first order rewrites references in views, triggers and foreign keys to the old copy", st.table),
				"Create the new table under a temporary name, copy the rows, drop the old table, then rename the new one (the 12-step procedure in sqlite.org/lang_altertable.html)")
		}
	}

	rejected := false
	if skip := a.notRunnable(st); skip != "" {
		add("DBS-SQLITE-NOT-RUN", score.Medium, skip, "")
	} else if err := a.scratch.exec(a.ctx, st.piece.SQL); err != nil {
		rejected = true
		add("DBS-SQLITE-REJECTED", score.High, "SQLite rejects this statement: "+err.Error(), rejectedAlternative(st))
	}

	switch st.kind {
	case "ALTER TABLE RENAME TO":
		renamedAway[st.table] = true
	case "ALTER TABLE RENAME COLUMN":
		add("DBS-SQLITE-VERSION", score.Low, "RENAME COLUMN needs SQLite 3.25.0 or later", "")
	case "ALTER TABLE ADD COLUMN":
		if slices.Contains(st.words, "check") || (slices.Contains(st.words, "generated") && slices.Contains(st.words, "not")) {
			out.Work = "scan"
		}
		// SQLite checks this against the rows, which the scratch copy does not have.
		if notNullWithoutDefault(st.words) && (!sized || rows > 0) && a.before.Relation(schema.QualifiedName{Schema: Schema, Name: st.table}) != nil {
			add("DBS-SQLITE-NOT-NULL-NO-DEFAULT", score.High,
				"SQLite refuses to add a NOT NULL column without a non-NULL default to a table that has rows",
				"Give the column a DEFAULT, or add it nullable, backfill it, and rebuild the table with NOT NULL")
		}
	case "ALTER TABLE DROP COLUMN":
		out.Work = "rewrite"
		add("DBS-SQLITE-VERSION", score.Medium, "DROP COLUMN needs SQLite 3.35.0 or later, and 3.35.5 fixed bugs that could corrupt the database", "Make sure the SQLite your application links is 3.35.5 or newer")
	case "ALTER TABLE ALTER COLUMN", "ALTER TABLE ADD CONSTRAINT", "ALTER TABLE DROP CONSTRAINT":
		add("DBS-SQLITE-VERSION", score.High, "this ALTER form arrived in SQLite 3.53.0 (April 2026); older versions reject it", "Check the SQLite version your application links, or use the 12-step table rebuild")
	case "CREATE INDEX", "CREATE UNIQUE INDEX":
		if _, existed := a.base.Rows[st.table]; existed {
			out.Work = "index_build"
		}
		if st.kind == "CREATE UNIQUE INDEX" && sized && rows > 0 {
			add("DBS-SQLITE-UNIQUE-DATA", score.Medium, "a unique index fails if existing rows have duplicates, which a schema-only check cannot see", "Check for duplicates first with GROUP BY ... HAVING count(*) > 1")
		}
	case "INSERT", "REPLACE":
		if src := selectSource(st); src != "" {
			st.table, rows, sized = src, a.base.Rows[src], hasKey(a.base.Rows, src)
			out.Work = "rewrite"
		}
	case "UPDATE", "DELETE":
		if !slices.Contains(st.words, "where") {
			out.Work = "rewrite"
		}
	case "VACUUM":
		out.Work = "rewrite"
		add("DBS-SQLITE-VACUUM", score.High, "VACUUM rewrites the whole file: it needs up to twice the file size in free disk, cannot run inside a transaction, and may renumber rowids of tables without an INTEGER PRIMARY KEY", "Run it separately, outside the migration, when the application is stopped")
	case "PRAGMA":
		a.pragmaRules(st, add)
	}

	if rejected {
		out.Work = "none" // it never runs
	} else if out.Work != "none" || isWrite(st.kind) {
		out.Locks = append(out.Locks, migrate.Lock{Relation: "main", Mode: "DATABASE WRITE"})
	}
	if out.Work != "none" && st.kind != "VACUUM" {
		a.sizeRule(st, &out, rows, sized, add)
	}
	for _, f := range out.Findings {
		out.Rating = score.Max(out.Rating, f.Rating)
	}
	out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
	return out
}

// notNullWithoutDefault reports a column definition with NOT NULL and no
// DEFAULT, or DEFAULT NULL.
func notNullWithoutDefault(words []string) bool {
	notNull := false
	for i := 0; i+1 < len(words); i++ {
		if words[i] == "not" && words[i+1] == "null" {
			notNull = true
		}
	}
	d := slices.Index(words, "default")
	return notNull && (d < 0 || (d+1 < len(words) && words[d+1] == "null"))
}

func hasKey(m map[string]int64, k string) bool { _, ok := m[k]; return ok }

// sizeRule rates a statement that reads or rewrites a whole table by the
// table's size: SQLite holds the database write lock for all of it.
func (a *analysis) sizeRule(st *statement, out *migrate.Statement, rows int64, sized bool, add func(string, score.Rating, string, string)) {
	verb := map[string]string{"scan": "checks every row of", "index_build": "builds an index over", "rewrite": "rewrites"}[out.Work]
	if !sized {
		if _, existed := a.base.Rows[st.table]; !existed && st.table != "" && a.before.Relation(schema.QualifiedName{Schema: Schema, Name: st.table}) == nil {
			return // a table this migration created: empty when the statement runs
		}
		add("DBS-SQLITE-TABLE-WORK", score.Medium,
			fmt.Sprintf("this %s %s, whose size is unknown, while holding the database write lock, so every other writer waits", verb, nameOr(st.table, "the table")), "")
		return
	}
	r := score.Low
	switch {
	case rows >= 1_000_000:
		r = score.High
	case rows >= 100_000:
		r = score.Medium
	}
	if rows == 0 {
		return
	}
	add("DBS-SQLITE-TABLE-WORK", r,
		fmt.Sprintf("this %s %s (%d rows) while holding the database write lock, so every other writer waits; in rollback-journal mode readers are blocked too once the change outgrows the page cache", verb, nameOr(st.table, "the table"), rows),
		"Run it when writes can pause, or in batches if the table is large")
}

func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

func (a *analysis) dropTableRules(st *statement, add func(string, score.Rating, string, string)) {
	var cascades, refs []string
	for _, r := range a.before.Relations {
		for _, fk := range r.ForeignKeys {
			if !strings.EqualFold(fk.Ref.Name, st.table) || strings.EqualFold(r.Name, st.table) {
				continue
			}
			refs = append(refs, r.Name)
			if fk.OnDelete == "cascade" || fk.OnDelete == "set null" || fk.OnDelete == "set default" {
				cascades = append(cascades, fmt.Sprintf("%s (ON DELETE %s)", r.Name, strings.ToUpper(fk.OnDelete)))
			}
		}
	}
	if a.fkOff {
		return
	}
	switch {
	case len(cascades) > 0:
		add("DBS-SQLITE-DROP-CASCADE", score.High,
			"with foreign keys on, DROP TABLE first deletes every row, which fires the foreign key actions of "+strings.Join(cascades, ", "),
			"Turn foreign keys off with PRAGMA foreign_keys=OFF before the transaction starts (it does nothing inside one), and check with PRAGMA foreign_key_check afterwards")
	case len(refs) > 0:
		add("DBS-SQLITE-DROP-REFERENCED", score.Medium,
			"with foreign keys on, DROP TABLE fails while rows of "+strings.Join(refs, ", ")+" still reference this table", "")
	}
}

var riskyPragmas = map[string]string{
	"writable_schema":    "editing sqlite_schema directly corrupts the database on a single mistake",
	"schema_version":     "changing schema_version by hand can make connections run statements against a stale schema, giving wrong results or corruption",
	"journal_mode":       "journal_mode OFF or MEMORY can corrupt the database if the process crashes mid-migration",
	"defer_foreign_keys": "setting defer_foreign_keys to 0 mid-transaction resets foreign key tracking, so violations can commit",
}

func (a *analysis) pragmaRules(st *statement, add func(string, score.Rating, string, string)) {
	if len(st.words) < 2 {
		return
	}
	name := st.words[1]
	value := ""
	if len(st.words) > 2 {
		value = st.words[len(st.words)-1]
	}
	if name == "foreign_keys" && slices.Contains([]string{"off", "0", "false", "no"}, value) {
		if st.inTx {
			add("DBS-SQLITE-FK-PRAGMA-IN-TX", score.High, "PRAGMA foreign_keys=OFF does nothing inside a transaction and raises no error, so foreign keys stay on for what follows", "Turn foreign keys off before BEGIN, and back on after COMMIT")
		} else {
			a.fkOff = true
			add("DBS-SQLITE-FK-PRAGMA-IN-TX", score.Medium, "if your migration runner wraps each file in a transaction, as many do, this PRAGMA does nothing and raises no error", "Check how the runner executes files; turn foreign keys off outside its transaction")
		}
		return
	}
	why, risky := riskyPragmas[name]
	switch {
	case !risky:
	case name == "journal_mode" && value != "off" && value != "memory":
	case name == "defer_foreign_keys" && value != "0" && value != "off" && value != "false":
	case name == "writable_schema" && (value == "off" || value == "0" || value == "false"):
	case len(st.words) == 2: // reading the value
	default:
		add("DBS-SQLITE-UNSAFE-PRAGMA", score.High, why, "")
	}
}

// notRunnable explains why a statement is not executed on the scratch
// database: it would reach files on this machine.
func (a *analysis) notRunnable(st *statement) string {
	switch {
	case st.kind == "ATTACH" || st.kind == "DETACH":
		return "ATTACH and DETACH open other database files, so dbscope does not run them; statements on attached schemas are reported as rejected"
	case st.kind == "VACUUM" && slices.Contains(st.words, "into"):
		return "VACUUM INTO writes a new file, so dbscope does not run it"
	case slices.Contains(st.words, "load_extension"):
		return "load_extension loads code, so dbscope does not run it"
	}
	return ""
}

func rejectedAlternative(st *statement) string {
	if strings.HasPrefix(st.kind, "ALTER TABLE") {
		return "Rebuild the table: create it under a new name with the new shape, copy the rows, drop the old table and rename the new one (sqlite.org/lang_altertable.html, section 7)"
	}
	return ""
}

func isWrite(kind string) bool {
	switch {
	case strings.HasPrefix(kind, "CREATE"), strings.HasPrefix(kind, "DROP"), strings.HasPrefix(kind, "ALTER"),
		kind == "INSERT", kind == "REPLACE", kind == "UPDATE", kind == "DELETE", kind == "REINDEX", kind == "ANALYZE":
		return true
	}
	return false
}

// selectSource is the table an INSERT ... SELECT reads, the copy step of a table rebuild.
func selectSource(st *statement) string {
	i := slices.Index(st.words, "select")
	if i < 0 {
		return ""
	}
	j := slices.Index(st.words[i:], "from")
	if j < 0 || i+j+1 >= len(st.words) {
		return ""
	}
	return st.words[i+j+1]
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return line
}

// classify names the statement's kind and target from its leading words.
func classify(file string, p Piece, inTx bool) *statement {
	st := &statement{file: file, piece: p, inTx: inTx}
	var names []string // words with their original case, quotes removed
	for i := 0; i < len(p.SQL); {
		j, tok := nextToken(p.SQL, i)
		text := p.SQL[i:j]
		switch {
		case tok == tkWS || tok == tkSemi:
		case isWord(rune(text[0])):
			st.words = append(st.words, strings.ToLower(text))
			names = append(names, text)
		case text[0] == '"' || text[0] == '`' || text[0] == '[':
			name := strings.ReplaceAll(text[1:len(text)-1], `""`, `"`)
			st.words = append(st.words, strings.ToLower(name))
			names = append(names, name)
		case text == ".":
			// "main"."t": drop the schema written before the dot
			if n := len(names); n > 0 && strings.EqualFold(names[n-1], Schema) {
				st.words, names = st.words[:n-1], names[:n-1]
			}
		}
		i = j
	}
	w := st.words
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	name := func(i int) string {
		for at(i) == "if" || at(i) == "not" || at(i) == "exists" {
			i++
		}
		if i < len(names) {
			return names[i]
		}
		return ""
	}
	switch at(0) {
	case "create":
		i := 1
		if at(i) == "temp" || at(i) == "temporary" {
			i++
		}
		switch {
		case at(i) == "unique" && at(i+1) == "index":
			st.kind = "CREATE UNIQUE INDEX"
			st.table = name(slices.Index(w, "on") + 1)
		case at(i) == "index":
			st.kind = "CREATE INDEX"
			st.table = name(slices.Index(w, "on") + 1)
		case at(i) == "virtual":
			st.kind, st.table = "CREATE VIRTUAL TABLE", name(i+2)
		case at(i) == "table" && slices.Contains(w, "as") && slices.Contains(w, "select"):
			st.kind, st.table = "CREATE TABLE AS", name(i+1)
		default:
			st.kind, st.table = "CREATE "+strings.ToUpper(at(i)), name(i+1)
		}
	case "drop":
		st.kind, st.table = "DROP "+strings.ToUpper(at(1)), name(2)
	case "alter":
		st.table = name(2)
		rest := w[min(3, len(w)):]
		for len(rest) > 0 && (rest[0] == "if" || rest[0] == "exists" || strings.EqualFold(rest[0], st.table)) {
			rest = rest[1:]
		}
		switch {
		case len(rest) >= 2 && rest[0] == "rename" && rest[1] == "to":
			st.kind = "ALTER TABLE RENAME TO"
		case len(rest) >= 1 && rest[0] == "rename":
			st.kind = "ALTER TABLE RENAME COLUMN"
		case len(rest) >= 2 && rest[0] == "add" && rest[1] == "constraint":
			st.kind = "ALTER TABLE ADD CONSTRAINT"
		case len(rest) >= 2 && rest[0] == "drop" && rest[1] == "constraint":
			st.kind = "ALTER TABLE DROP CONSTRAINT"
		case len(rest) >= 1 && rest[0] == "add":
			st.kind = "ALTER TABLE ADD COLUMN"
		case len(rest) >= 1 && rest[0] == "drop":
			st.kind = "ALTER TABLE DROP COLUMN"
		case len(rest) >= 1 && rest[0] == "alter":
			st.kind = "ALTER TABLE ALTER COLUMN"
		default:
			st.kind = "ALTER TABLE"
		}
	case "insert", "replace":
		st.kind = strings.ToUpper(at(0))
		st.table = name(slices.Index(w, "into") + 1)
	case "update":
		i := 1
		if at(i) == "or" {
			i += 2
		}
		st.kind, st.table = "UPDATE", name(i)
	case "delete":
		st.kind, st.table = "DELETE", name(slices.Index(w, "from")+1)
	case "begin", "commit", "end", "rollback", "savepoint", "release", "pragma", "vacuum", "reindex", "analyze", "attach", "detach", "select", "with", "values", "explain":
		st.kind = strings.ToUpper(at(0))
	default:
		st.kind = strings.ToUpper(at(0))
	}
	return st
}

// diff compares the scratch database's structure before and after.
func diff(before, after *schema.Model) apply.Diff {
	d := apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}
	key := func(r *schema.Relation) string { return r.QName().String() }
	for i := range after.Relations {
		r := &after.Relations[i]
		b := before.Relation(r.QName())
		if b == nil {
			d.Added = append(d.Added, key(r))
			continue
		}
		var changes []apply.ColumnChange
		for _, c := range r.Columns {
			bc := b.Column(c.Name)
			switch {
			case bc == nil:
				changes = append(changes, apply.ColumnChange{Relation: key(r), Column: c.Name, Change: "added"})
			case *bc != c:
				changes = append(changes, apply.ColumnChange{Relation: key(r), Column: c.Name, Change: "altered"})
			}
		}
		for _, c := range b.Columns {
			if r.Column(c.Name) == nil {
				changes = append(changes, apply.ColumnChange{Relation: key(r), Column: c.Name, Change: "dropped"})
			}
		}
		if len(changes) > 0 || !sameIndexes(b, r) {
			d.Altered = append(d.Altered, key(r))
			d.Columns = append(d.Columns, changes...)
		}
	}
	for i := range before.Relations {
		if after.Relation(before.Relations[i].QName()) == nil {
			d.Dropped = append(d.Dropped, key(&before.Relations[i]))
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Dropped)
	sort.Strings(d.Altered)
	return d
}

func sameIndexes(a, b *schema.Relation) bool {
	names := func(r *schema.Relation) []string {
		var out []string
		for _, i := range r.Indexes {
			out = append(out, i.Name)
		}
		for _, fk := range r.ForeignKeys {
			out = append(out, fk.Ref.String()+"("+strings.Join(fk.Columns, ",")+")")
		}
		sort.Strings(out)
		return out
	}
	return slices.Equal(names(a), names(b))
}

// scratchDB is a private in-memory database on one pinned connection, with
// an authorizer that refuses attaching a file so no statement reaches one.
type scratchDB struct {
	db   *sql.DB
	conn *sql.Conn
}

func openScratch(ctx context.Context) (*scratchDB, error) {
	c, err := sqlite.NewConnector("file::memory:?mode=memory&cache=private")
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(scratchConnector{c})
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &scratchDB{db: db, conn: conn}, nil
}

func (s *scratchDB) exec(ctx context.Context, q string) error {
	_, err := s.conn.ExecContext(ctx, q)
	return err
}

func (s *scratchDB) ExecContext(ctx context.Context, q string) (sql.Result, error) {
	return s.conn.ExecContext(ctx, q)
}

func (s *scratchDB) Close() {
	s.conn.Close()
	s.db.Close()
}

type scratchConnector struct{ driver.Connector }

func (r scratchConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := r.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	reg, ok := c.(sqlite.AuthorizerRegisterer)
	if !ok {
		c.Close()
		return nil, errors.New("sqlite driver has no authorizer")
	}
	err = reg.RegisterAuthorizer(func(action sqlite.AuthorizerActionCode, arg1, arg2, _, _ string) sqlite.AuthorizerReturnCode {
		switch {
		// VACUUM attaches an unnamed temporary database; a named file would be created on disk.
		case action == sqlite.AuthAttach && arg1 != "" && arg1 != ":memory:":
			return sqlite.AuthorizerDeny
		case action == sqlite.AuthFunction && strings.EqualFold(arg2, "load_extension"):
			return sqlite.AuthorizerDeny
		}
		return sqlite.AuthorizerOK
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

package mssql

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// effect is what a statement does to a table's rows while it holds its lock.
type effect int

const (
	effMeta    effect = iota // metadata only
	effScan                  // reads every row
	effIndex                 // reads every row and writes an index
	effRewrite               // writes every row
)

// change is one clause's effect and a short description of it.
type change struct {
	eff  effect
	what string
}

// work rates the effect of a statement on a table by the table's size.
// The size cutoffs are those of the MySQL rules (docs/ENGINES.md):
// large from 1M rows or 1 GiB, medium from 100k rows or 100 MiB.
func (a *ddlAnalysis) work(out *migrate.Statement, add adder, c change, online bool, display string, tbl *DDLTable, lockMode string) {
	switch c.eff {
	case effMeta:
		return
	case effScan:
		out.Work = "scan"
	case effIndex:
		out.Work = "index_build"
	case effRewrite:
		out.Work = "rewrite"
	}
	size, tier := "of unknown size", -1
	if tbl != nil && tbl.Sized {
		b := tbl.Bytes
		out.SizeBytes = &b
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
	rate := func(unknown score.Rating, tiers ...score.Rating) score.Rating {
		if tier < 0 {
			return unknown
		}
		return tiers[tier]
	}
	switch {
	case online:
		add("DBS-MSSQL-TABLE-WORK", rate(score.Medium, score.Low, score.Medium, score.High),
			fmt.Sprintf("%s runs online over %s (%s): writes continue, but it queues for an S lock at the start and takes a Sch-M lock at the end, "+
				"and inside a transaction it holds its locks until commit", c.what, display, size),
			"Run it when the server is quiet; add WAIT_AT_LOW_PRIORITY to the ONLINE option so its locks yield to other sessions")
	case c.eff == effRewrite:
		add("DBS-MSSQL-BLOCKS-TABLE", rate(score.High, score.Medium, score.High, score.Critical),
			fmt.Sprintf("%s writes every row of %s (%s) while holding a %s lock, so reads and writes of the table wait for the whole time", c.what, display, size, lockMode),
			a.rewriteAlt())
	case c.eff == effIndex:
		alt := "Add WITH (ONLINE = ON" + a.resumable() + ") so writes continue during the build"
		if a.base.EngineEdition != 0 && !a.base.Online() {
			alt = "This edition has no online index builds (Enterprise, Developer and Azure SQL only); run it when writes can pause"
		}
		blocked := "writes to it wait"
		if lockMode == lockSchM {
			blocked = "reads and writes of it wait"
		}
		add("DBS-MSSQL-OFFLINE-INDEX", rate(score.High, score.Medium, score.High, score.Critical),
			fmt.Sprintf("%s reads all of %s (%s) offline, holding a %s lock for the whole build, so %s", c.what, display, size, lockMode, blocked), alt)
	case c.eff == effScan:
		add("DBS-MSSQL-TABLE-SCAN", rate(score.Medium, score.Low, score.Medium, score.High),
			fmt.Sprintf("%s reads every row of %s (%s) while holding a %s lock, so reads and writes of the table wait until it finishes", c.what, display, size, lockMode),
			"Run it when the table is quiet, or add the constraint WITH NOCHECK first and validate it later in a quiet period")
	}
}

func (a *ddlAnalysis) rewriteAlt() string {
	if a.base.EngineEdition != 0 && !a.base.Online() {
		return "Run it when the table can be offline, or add a new column, backfill it in batches and switch over"
	}
	return "Add a new column, backfill it in batches and switch over; for ALTER COLUMN, WITH (ONLINE = ON) lets writes continue (Enterprise, Developer and Azure SQL)"
}

// resumable is the RESUMABLE option for an online index build, from SQL Server 2019.
func (a *ddlAnalysis) resumable() string {
	if a.base.Server.Num == 0 || a.base.Server.Num >= 150000 {
		return ", RESUMABLE = ON"
	}
	return ""
}

func worse(x, y change) change {
	if y.eff > x.eff {
		return y
	}
	return x
}

// options reads WITH (ONLINE = ON, RESUMABLE = ON, WAIT_AT_LOW_PRIORITY ...) after a statement's body.
type options struct {
	online, resumable, lowPriority, with bool
}

func readOptions(toks []Token) options {
	var o options
	depth := 0
	for i, t := range toks {
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && t.Word == "with" && i+1 < len(toks) && toks[i+1].Text == "(" {
			o.with = true
		}
		val := ""
		if i+2 < len(toks) && toks[i+1].Text == "=" {
			val = toks[i+2].Word
		}
		switch t.Word {
		case "online":
			o.online = o.online || val == "on"
		case "resumable":
			o.resumable = o.resumable || val == "on"
		case "wait_at_low_priority":
			o.lowPriority = true
		}
	}
	return o
}

// clauseSplit divides tokens at top-level commas.
func clauseSplit(toks []Token) [][]Token {
	var out [][]Token
	depth, start := 0, 0
	for i, t := range toks {
		switch t.Text {
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
	return append(out, toks[start:])
}

func hasWord(toks []Token, ws ...string) bool {
	for _, t := range toks {
		if slices.Contains(ws, t.Word) {
			return true
		}
	}
	return false
}

// hasPair reports the words x then y next to each other, as in NOT NULL or WITH VALUES.
func hasPair(toks []Token, x, y string) bool {
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].Word == x && toks[i+1].Word == y {
			return true
		}
	}
	return false
}

// typeOf is a column definition's type, lower-case without spaces: varchar(50), decimal(12,2).
func typeOf(def []Token) string {
	if len(def) == 0 {
		return ""
	}
	var b strings.Builder
	i := 0
	b.WriteString(strings.ToLower(ident(def[0])))
	for i+2 < len(def) && def[i+1].Text == "." { // a schema-qualified alias type
		b.WriteString("." + strings.ToLower(ident(def[i+2])))
		i += 2
	}
	if i+1 < len(def) && def[i+1].Text == "(" {
		for j := i + 1; j < len(def); j++ {
			b.WriteString(strings.ToLower(def[j].Text))
			if def[j].Text == ")" {
				break
			}
		}
	}
	return b.String()
}

// parseType splits "varchar(50)" into its name and length; -1 is max.
func parseType(t string) (name string, length int) {
	open := strings.IndexByte(t, '(')
	if open < 0 {
		return t, 0
	}
	arg := strings.TrimSuffix(t[open+1:], ")")
	if arg == "max" {
		return t[:open], -1
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		return t[:open], 0
	}
	return t[:open], n
}

var (
	varLength   = map[string]bool{"varchar": true, "nvarchar": true, "varbinary": true}
	fixedLength = map[string]bool{"char": true, "nchar": true, "binary": true}
	intRank     = map[string]int{"tinyint": 1, "smallint": 2, "int": 3, "bigint": 4}
	// lobTypes cannot be added online with a default
	// (https://learn.microsoft.com/sql/t-sql/statements/alter-table-transact-sql#adding-not-null-columns-as-an-online-operation).
	lobTypes = map[string]bool{"text": true, "ntext": true, "image": true, "xml": true, "hierarchyid": true, "geometry": true, "geography": true}
)

func isLOB(t string) bool {
	n, l := parseType(t)
	return lobTypes[n] || (varLength[n] && l == -1)
}

// columnChange predicts ALTER COLUMN from type ot (nullable on) to nt (nullable nn).
func columnChange(ot, nt string, on, nn bool) effect {
	ob, ol := parseType(ot)
	nb, nl := parseType(nt)
	// NULL to NOT NULL reads every row; for a variable-length column it also writes each one.
	nullEff := effMeta
	if on && !nn {
		nullEff = effScan
		if varLength[nb] || isLOB(nt) {
			nullEff = effRewrite
		}
	}
	typeEff := effRewrite
	switch {
	case ot == nt:
		typeEff = effMeta
	case ob == nb && varLength[ob]:
		switch {
		case ol == -1 || nl == -1:
			typeEff = effRewrite
		case nl >= ol:
			typeEff = effMeta
		}
	case ob == nb && fixedLength[ob]:
		if nl < ol {
			typeEff = effScan
		}
	case intRank[ob] > 0 && intRank[nb] > 0 && intRank[nb] < intRank[ob]:
		typeEff = effScan
	}
	return max(typeEff, nullEff)
}

func (a *ddlAnalysis) alterTable(out *migrate.Statement, add adder, display, key string, rest []Token) {
	tbl := a.base.Tables[key]
	out.Touch(display)
	a.altered[display] = true
	lock := migrate.Lock{Relation: display, Mode: lockSchM}
	at := func(i int) string {
		if i < len(rest) {
			return rest[i].Word
		}
		return ""
	}
	i, check := 0, ""
	if at(0) == "with" && (at(1) == "check" || at(1) == "nocheck") {
		check, i = at(1), 2
	}
	opts := readOptions(rest)
	defer func() { a.lockWait(out, add, opts.lowPriority) }()
	switch at(i) {
	case "add":
		out.Locks = append(out.Locks, lock)
		a.alterAdd(out, add, display, key, tbl, clauseSplit(rest[i+1:]), check, opts)
	case "alter":
		if at(i+1) == "column" {
			a.alterColumn(out, add, display, key, tbl, rest[i+2:], opts)
			return
		}
		out.Locks = append(out.Locks, lock)
	case "drop":
		out.Locks = append(out.Locks, lock)
		a.alterDrop(out, add, display, key, tbl, rest[i+1:])
	case "nocheck", "check":
		out.Locks = append(out.Locks, lock)
		a.toggleConstraints(out, add, display, tbl, at(i) == "check", check == "check", rest[i+2:])
	case "rebuild":
		out.Locks = append(out.Locks, lock)
		c := change{effRewrite, "ALTER TABLE REBUILD"}
		if a.onlineFails(out, add, opts) {
			return
		}
		a.work(out, add, c, opts.online, display, tbl, lockSchM)
	default:
		out.Locks = append(out.Locks, lock)
	}
}

// onlineFails adds the finding for ONLINE = ON on an edition without online operations.
func (a *ddlAnalysis) onlineFails(out *migrate.Statement, add adder, opts options) bool {
	if !opts.online || a.base.EngineEdition == 0 || a.base.Online() {
		return false
	}
	add("DBS-MSSQL-FAILS", score.High, fmt.Sprintf("ONLINE = ON needs Enterprise, Developer or Azure SQL; this server is %s, so the statement fails (error 1712)", a.base.Server.Edition),
		"Leave ONLINE out and run it when the table can be offline")
	return true
}

func (a *ddlAnalysis) alterAdd(out *migrate.Statement, add adder, display, key string, tbl *DDLTable, clauses [][]Token, check string, opts options) {
	worst := change{}
	var whats []string
	online := false
	for _, c := range clauses {
		if len(c) == 0 {
			continue
		}
		switch c[0].Word {
		case "constraint", "primary", "unique", "foreign", "check":
			ch, on := a.addConstraint(out, add, display, c, check, opts)
			if ch.eff > effMeta {
				whats = append(whats, ch.what)
			}
			online = online || on
			worst = worse(worst, ch)
			continue
		}
		col := ident(c[0])
		a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: col, Change: "added"})
		ch := a.addColumn(add, display, tbl, col, c[1:])
		if ch.eff > effMeta {
			whats = append(whats, ch.what)
		}
		worst = worse(worst, ch)
	}
	if worst.eff == effMeta || slices.ContainsFunc(out.Findings, func(f migrate.Finding) bool { return f.RuleID == "DBS-MSSQL-FAILS" }) {
		return
	}
	worst.what = strings.Join(whats, ", ")
	a.work(out, add, worst, online, display, tbl, lockSchM)
}

// addColumn predicts ADD of one column: metadata only, or a write to every row.
func (a *ddlAnalysis) addColumn(add adder, display string, tbl *DDLTable, col string, def []Token) change {
	if len(def) > 0 && def[0].Word == "as" {
		if hasWord(def, "persisted") {
			return change{effRewrite, "add persisted computed column " + col}
		}
		return change{}
	}
	typ := typeOf(def)
	notNull := hasPair(def, "not", "null")
	dflt := hasWord(def, "default")
	switch {
	case hasWord(def, "identity"):
		return change{effRewrite, "add identity column " + col}
	case typ == "rowversion" || typ == "timestamp":
		return change{effRewrite, "add " + typ + " column " + col}
	case notNull && !dflt:
		if tbl != nil && tbl.Sized && tbl.Rows == 0 {
			return change{}
		}
		add("DBS-MSSQL-FAILS", score.High,
			fmt.Sprintf("ADD %s NOT NULL without a DEFAULT fails when %s has rows (error 4901): existing rows would have no value", col, display),
			"Add it with a DEFAULT, or add it NULL, backfill it in batches and then make it NOT NULL")
		return change{}
	case dflt && (notNull || hasPair(def, "with", "values")):
		// A runtime-constant default is stored once in metadata on editions with online schema changes
		// (https://learn.microsoft.com/sql/t-sql/statements/alter-table-transact-sql#adding-not-null-columns-as-an-online-operation).
		switch {
		case hasWord(def, "newid", "newsequentialid"):
			return change{effRewrite, "add column " + col + " with a per-row default"}
		case isLOB(typ):
			return change{effRewrite, "add " + typ + " column " + col + " with a default"}
		case a.base.EngineEdition != 0 && !a.base.Online():
			return change{effRewrite, "add column " + col + " with a default (this edition writes it to every row)"}
		}
	}
	return change{}
}

func (a *ddlAnalysis) addConstraint(out *migrate.Statement, add adder, display string, c []Token, check string, opts options) (change, bool) {
	name := ""
	if c[0].Word == "constraint" && len(c) > 1 {
		name = ident(c[1])
	}
	kind := ""
	for _, t := range c {
		if t.Word == "primary" || t.Word == "unique" || t.Word == "foreign" || t.Word == "check" {
			kind = t.Word
			break
		}
	}
	label := strings.TrimSpace("constraint " + name)
	switch kind {
	case "foreign":
		if r := indexOf(c, "references"); r > 0 {
			ref, _, _ := a.tableName(c, r+1)
			out.Locks = append(out.Locks, migrate.Lock{Relation: ref, Mode: lockSchM})
			out.Touch(ref)
			if check == "nocheck" {
				add("DBS-MSSQL-UNTRUSTED-FK", score.Medium,
					fmt.Sprintf("WITH NOCHECK adds %s without checking existing rows: it is not trusted (is_not_trusted), so rows may already break it and the optimizer cannot rely on it", label),
					"Validate it later in a quiet period: ALTER TABLE ... WITH CHECK CHECK CONSTRAINT "+name)
				return change{}, false
			}
			return change{effScan, "adding foreign key " + name + " checks it against " + ref + "; it"}, false
		}
	case "check":
		if check == "nocheck" {
			add("DBS-MSSQL-UNTRUSTED-CHECK", score.Medium,
				fmt.Sprintf("WITH NOCHECK adds %s without checking existing rows: it is not trusted, so rows may already break it", label),
				"Validate it later in a quiet period: ALTER TABLE ... WITH CHECK CHECK CONSTRAINT "+name)
			return change{}, false
		}
		return change{effScan, "adding check " + name}, false
	case "primary", "unique":
		if a.onlineFails(out, add, opts) {
			return change{}, false
		}
		return change{effIndex, "adding " + strings.ToUpper(kind) + " " + name}, opts.online
	}
	return change{}, false
}

func (a *ddlAnalysis) alterColumn(out *migrate.Statement, add adder, display, key string, tbl *DDLTable, rest []Token, opts options) {
	mode := lockSchM
	if opts.online {
		mode = lockS // observed: an online ALTER COLUMN waits for S first
	}
	out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: mode})
	if len(rest) == 0 {
		return
	}
	colName := ident(rest[0])
	def := rest[1:]
	if w := indexOf(def, "with"); w >= 0 {
		def = def[:w]
	}
	if len(def) > 0 && (def[0].Word == "add" || def[0].Word == "drop") {
		return // ALTER COLUMN c ADD/DROP ROWGUIDCOL, PERSISTED, MASKED...
	}
	a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: colName, Change: "altered"})
	typ := typeOf(def)
	newNull := !hasPair(def, "not", "null")
	var old *DDLColumn
	if tbl != nil {
		old = tbl.Columns[strings.ToLower(colName)]
	}
	if old == nil {
		if a.onlineFails(out, add, opts) {
			return
		}
		a.work(out, add, change{effRewrite, "ALTER COLUMN " + colName + " (not in the base, so assumed to change its type)"}, opts.online, display, tbl, mode)
		return
	}
	if !hasWord(def, "null") && !old.Nullable {
		add("DBS-MSSQL-NULLABLE", score.Medium,
			fmt.Sprintf("ALTER COLUMN %s without NOT NULL makes the column nullable: SQL Server does not keep the old NOT NULL", colName),
			"Restate NOT NULL in the ALTER COLUMN")
	}
	if fail := a.dependents(old, typ, newNull, key); fail != "" {
		add("DBS-MSSQL-FAILS", score.High, fmt.Sprintf("ALTER COLUMN %s fails (error 5074): %s", colName, fail),
			"Drop the dependent index or constraint first and recreate it afterwards")
		return
	}
	if a.onlineFails(out, add, opts) {
		return
	}
	eff := columnChange(old.Type, typ, old.Nullable, newNull)
	what := fmt.Sprintf("changing %s from %s to %s", colName, old.Type, typ)
	if old.Type == typ {
		what = "making " + colName + " NOT NULL"
	}
	if old.Nullable && !newNull {
		what += " (it fails if a row holds NULL)"
	} else if eff == effScan {
		what += " (it fails if a value does not fit)"
	}
	a.work(out, add, change{eff, what}, opts.online && eff > effMeta, display, tbl, mode)
}

// dependents says why ALTER COLUMN fails on what uses the column, or "".
// Indexes allow only growing a varchar, nvarchar or varbinary; checks,
// defaults and foreign keys allow any change that keeps the type name
// (https://learn.microsoft.com/sql/t-sql/statements/alter-table-transact-sql#alter-column; checked in ddl_verify_test.go).
func (a *ddlAnalysis) dependents(old *DDLColumn, typ string, newNull bool, key string) string {
	if a.dropped[key] {
		return "" // an earlier statement dropped an index or constraint of the table, perhaps this one's
	}
	ob, ol := parseType(old.Type)
	nb, nl := parseType(typ)
	if old.Indexed {
		same := old.Type == typ || (ob == nb && varLength[ob] && ol != -1 && nl != -1 && nl >= ol)
		if !same || old.Nullable != newNull {
			return "an index or key uses the column"
		}
	}
	if ob != nb {
		switch {
		case old.Checked:
			return "a check constraint or computed column uses the column"
		case old.Default:
			return "a default constraint is bound to the column"
		case old.ForeignKey:
			return "a foreign key uses the column"
		}
	}
	return ""
}

// alterDrop follows DROP [CONSTRAINT] a, COLUMN b, ...: without a keyword an item is a constraint.
func (a *ddlAnalysis) alterDrop(out *migrate.Statement, add adder, display, key string, tbl *DDLTable, rest []Token) {
	if w := indexOfDepth0(rest, "with"); w >= 0 {
		rest = rest[:w]
	}
	mode := "constraint"
	for _, item := range clauseSplit(rest) {
		item = slices.DeleteFunc(slices.Clone(item), func(t Token) bool { return t.Word == "if" || t.Word == "exists" })
		if len(item) == 0 {
			continue
		}
		switch item[0].Word {
		case "column", "constraint":
			mode = item[0].Word
			item = item[1:]
		case "period":
			return
		}
		if len(item) == 0 {
			continue
		}
		name := ident(item[len(item)-1])
		if mode == "column" {
			a.dropColumn(add, display, key, tbl, name)
			continue
		}
		a.dropped[key] = true
		lname := strings.ToLower(name)
		if tbl == nil || tbl.ForeignKeyRefs[lname] != "" || tbl.Constraints[lname] == "" {
			a.droppedFK = true // a foreign key, or a constraint the base does not know
		}
		if tbl == nil {
			continue
		}
		if ref := tbl.ForeignKeyRefs[lname]; ref != "" {
			out.Locks = append(out.Locks, migrate.Lock{Relation: ref, Mode: lockSchM})
			out.Touch(ref)
		}
		if lost := tbl.Constraints[lname]; lost != "" {
			add("DBS-DROP-CONSTRAINT", score.Medium, fmt.Sprintf("drops %s on %s: %s", name, display, lost),
				"Enforce the rule in the application first, or add the replacement before dropping this one")
		}
	}
}

func (a *ddlAnalysis) dropColumn(add adder, display, key string, tbl *DDLTable, col string) {
	a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: col, Change: "dropped"})
	if tbl != nil && !a.dropped[key] {
		if c := tbl.Columns[strings.ToLower(col)]; c != nil && (c.Indexed || c.Checked || c.Default || c.ForeignKey) {
			add("DBS-MSSQL-FAILS", score.High, fmt.Sprintf("DROP COLUMN %s fails (error 5074): an index, key, default, check or foreign key uses it", col),
				"Drop what uses the column first, in the same migration")
			return
		}
	}
	a.drops = append(a.drops, "column "+display+"."+col)
	add("DBS-MSSQL-DROPS-DATA", score.High,
		fmt.Sprintf("DROP COLUMN %s removes the column and its data from %s (only metadata changes; the space returns when the table is rebuilt)", col, display),
		"Take a backup first; stop using it in one release and drop it in a later one")
}

// toggleConstraints follows [WITH CHECK] CHECK CONSTRAINT and NOCHECK CONSTRAINT.
func (a *ddlAnalysis) toggleConstraints(out *migrate.Statement, add adder, display string, tbl *DDLTable, enable, withCheck bool, names []Token) {
	for _, item := range clauseSplit(names) {
		if len(item) == 0 {
			continue
		}
		name := ident(item[0])
		fk := true
		if tbl != nil && item[0].Word != "all" {
			_, fk = tbl.ForeignKeyRefs[strings.ToLower(name)]
			if ref := tbl.ForeignKeyRefs[strings.ToLower(name)]; ref != "" {
				out.Locks = append(out.Locks, migrate.Lock{Relation: ref, Mode: lockSchM})
				out.Touch(ref)
			}
		}
		switch {
		case !enable && fk:
			add("DBS-MSSQL-DISABLED-FK", score.Medium, fmt.Sprintf("NOCHECK CONSTRAINT %s stops checking new rows, and the key is no longer trusted", name),
				"Keep it enabled; if it must be off for a load, re-enable it WITH CHECK CHECK CONSTRAINT afterwards")
		case !enable:
			add("DBS-MSSQL-UNTRUSTED-CHECK", score.Medium, fmt.Sprintf("NOCHECK CONSTRAINT %s stops checking new rows, and the constraint is no longer trusted", name),
				"Keep it enabled; if it must be off for a load, re-enable it WITH CHECK CHECK CONSTRAINT afterwards")
		case withCheck:
			a.work(out, add, change{effScan, "WITH CHECK CHECK CONSTRAINT " + name}, false, display, tbl, lockSchM)
		case fk:
			add("DBS-MSSQL-UNTRUSTED-FK", score.Medium, fmt.Sprintf("CHECK CONSTRAINT %s re-enables it without checking existing rows (WITH NOCHECK is the default), so it stays untrusted", name),
				"Use ALTER TABLE ... WITH CHECK CHECK CONSTRAINT "+name+" in a quiet period")
		default:
			add("DBS-MSSQL-UNTRUSTED-CHECK", score.Medium, fmt.Sprintf("CHECK CONSTRAINT %s re-enables it without checking existing rows (WITH NOCHECK is the default), so it stays untrusted", name),
				"Use ALTER TABLE ... WITH CHECK CHECK CONSTRAINT "+name+" in a quiet period")
		}
	}
}

func (a *ddlAnalysis) createIndex(out *migrate.Statement, add adder, display, key string, toks []Token, next int, clustered bool) {
	tbl := a.base.Tables[key]
	out.Touch(display)
	a.altered[display] = true
	opts := readOptions(toks[next:])
	mode := lockS
	if clustered {
		mode = lockSchM
	}
	out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: mode})
	defer a.lockWait(out, add, opts.lowPriority)
	if a.onlineFails(out, add, opts) {
		return
	}
	a.work(out, add, change{effIndex, "CREATE INDEX"}, opts.online, display, tbl, mode)
	if !opts.online && !opts.with && (a.base.EngineEdition == 0 || a.base.Online()) {
		sql := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out.SQL), ";"))
		note := "Builds the index while writes continue (Enterprise, Developer and Azure SQL)."
		if r := a.resumable(); r != "" {
			note += " RESUMABLE = ON fails inside an explicit transaction (error 574), so run it outside one; a failed or paused build resumes with ALTER INDEX ... RESUME."
		}
		out.Rewrite = &migrate.Rewrite{SQL: sql + " WITH (ONLINE = ON" + a.resumable() + ")", Note: note}
	}
}

func (a *ddlAnalysis) alterIndex(out *migrate.Statement, add adder, display, key string, name Token, rest []Token) {
	tbl := a.base.Tables[key]
	out.Touch(display)
	opts := readOptions(rest)
	defer a.lockWait(out, add, opts.lowPriority)
	if len(rest) == 0 || rest[0].Word != "rebuild" {
		out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: lockSchM})
		return
	}
	mode := lockSchM
	out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: mode})
	if a.onlineFails(out, add, opts) {
		return
	}
	a.work(out, add, change{effRewrite, "ALTER INDEX " + ident(name) + " REBUILD"}, opts.online, display, tbl, mode)
}

func (a *ddlAnalysis) dropTables(out *migrate.Statement, add adder, toks []Token) {
	i := 2
	for i < len(toks) {
		display, key, next := a.tableName(toks, i)
		if display == "" {
			break
		}
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, display)
		out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: lockSchM})
		out.Touch(display)
		if tbl := a.base.Tables[key]; tbl != nil {
			if tbl.ReferencedBy > 0 && !a.droppedFK {
				add("DBS-MSSQL-FAILS", score.High, fmt.Sprintf("DROP TABLE %s fails (error 3726): a foreign key of another table references it", display),
					"Drop the referencing foreign keys first, in the same migration")
			} else {
				a.drops = append(a.drops, "table "+display)
				add("DBS-MSSQL-DROPS-DATA", score.High, "DROP TABLE removes "+display+" and its data",
					"Take a backup first, or rename the table and drop it once nothing misses it")
			}
		}
		i = next
		if i < len(toks) && toks[i].Text == "," {
			i++
		} else {
			break
		}
	}
	a.lockWait(out, add, false)
}

func (a *ddlAnalysis) truncate(out *migrate.Statement, add adder, toks []Token) {
	display, key, _ := a.tableName(toks, 2)
	out.Touch(display)
	out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: lockSchM})
	defer a.lockWait(out, add, false)
	if tbl := a.base.Tables[key]; tbl != nil && tbl.ReferencedBy > 0 && !a.droppedFK {
		add("DBS-MSSQL-FAILS", score.High, fmt.Sprintf("TRUNCATE TABLE %s fails (error 4712): a foreign key references it", display),
			"Delete the rows in batches instead, or drop the referencing foreign keys first")
		return
	}
	a.drops = append(a.drops, "the rows of "+display)
	add("DBS-MSSQL-DROPS-DATA", score.High, "TRUNCATE TABLE deletes every row of "+display+"; it is not a schema-only change",
		"Take a backup first, or delete in batches if only some rows should go")
}

// dropIndexes follows DROP INDEX [IF EXISTS] name ON table [, ...] and the older DROP INDEX table.name.
func (a *ddlAnalysis) dropIndexes(out *migrate.Statement, add adder, toks []Token) {
	rest := slices.DeleteFunc(slices.Clone(toks[2:]), func(t Token) bool { return t.Word == "if" || t.Word == "exists" })
	if w := indexOfDepth0(rest, "with"); w >= 0 {
		rest = rest[:w]
	}
	for _, item := range clauseSplit(rest) {
		if len(item) == 0 {
			continue
		}
		var display, key, name string
		if on := indexOf(item, "on"); on > 0 {
			name = ident(item[on-1])
			display, key, _ = a.tableName(item, on+1)
		} else {
			if len(item) < 3 {
				continue
			}
			name = ident(item[len(item)-1])
			display, key, _ = a.tableName(item[:len(item)-2], 0)
		}
		a.dropped[key] = true
		out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: lockSchM})
		out.Touch(display)
		a.altered[display] = true
		tbl := a.base.Tables[key]
		if tbl == nil {
			continue
		}
		lname := strings.ToLower(name)
		switch {
		case tbl.KeyIndexes[lname]:
			add("DBS-MSSQL-FAILS", score.High, fmt.Sprintf("DROP INDEX %s fails (error 3723): it enforces a PRIMARY KEY or UNIQUE constraint", name),
				"Use ALTER TABLE "+display+" DROP CONSTRAINT "+name)
		case tbl.Constraints[lname] != "":
			add("DBS-DROP-CONSTRAINT", score.Medium, fmt.Sprintf("drops %s on %s: %s", name, display, tbl.Constraints[lname]),
				"Enforce the rule in the application first, or add the replacement before dropping this one")
		}
	}
	a.lockWait(out, add, false)
}

// rename follows EXEC sp_rename 'schema.table.column', 'new', 'COLUMN' and table renames.
func (a *ddlAnalysis) rename(out *migrate.Statement, add adder, toks []Token) {
	var args []string
	for _, t := range toks {
		if strings.HasPrefix(t.Text, "'") {
			args = append(args, strings.ReplaceAll(strings.Trim(t.Text, "'"), "''", "'"))
		}
	}
	if len(args) < 2 {
		return
	}
	parts := strings.Split(args[0], ".")
	for i := range parts {
		parts[i] = strings.Trim(parts[i], "[]")
	}
	column := len(args) >= 3 && strings.EqualFold(args[2], "column")
	if column && len(parts) < 2 {
		return
	}
	tableParts := parts
	if column {
		tableParts = parts[:len(parts)-1]
	}
	var ts []Token
	for i, p := range tableParts {
		if i > 0 {
			ts = append(ts, Token{Text: "."})
		}
		ts = append(ts, Token{Text: "[" + p + "]"})
	}
	display, _, _ := a.tableName(ts, 0)
	out.Touch(display)
	out.Locks = append(out.Locks, migrate.Lock{Relation: display, Mode: lockSchM})
	if column {
		a.altered[display] = true
		a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: parts[len(parts)-1], Change: "renamed", To: args[1]})
	}
	a.lockWait(out, add, false)
}

func indexOfDepth0(toks []Token, word string) int {
	depth := 0
	for i, t := range toks {
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && t.Word == word {
			return i
		}
	}
	return -1
}

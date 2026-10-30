package oracle

import (
	"fmt"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// opPlan is what one ALTER TABLE operation costs.
type opPlan struct {
	what string
	// mode is the table lock held while the work runs.
	mode string
	// dml means inserts, updates and deletes continue while it runs.
	dml bool
	// work is none, scan (reads every row), index_build or rewrite (writes every row or block).
	work string
	// op names its behavior on a busy table (behaviors).
	op string
}

var workRank = map[string]int{"none": 0, "scan": 1, "index_build": 2, "rewrite": 3}

var modeRank = map[string]int{"ROW SHARE": 1, "ROW EXCLUSIVE": 2, "SHARE": 3, "EXCLUSIVE": 4}

// alterTable rates ALTER TABLE name rest.
func (a *ddlAnalysis) alterTable(owner, name string, rest []Token) {
	key, tbl := a.table(owner, name)
	a.altered[key] = true
	a.out.Touch(key)
	w := words(rest)
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	var plans []opPlan
	switch at(0) {
	case "add":
		for _, it := range items(rest, 1) {
			iw := words(it)
			if len(iw) == 0 {
				continue
			}
			switch iw[0] {
			case "constraint", "primary", "unique", "foreign", "check":
				plans = append(plans, a.addConstraint(key, tbl, it, rest))
			case "supplemental", "partition", "subpartition", "period":
				plans = append(plans, opPlan{what: "add " + iw[0], mode: "EXCLUSIVE", work: "none", op: "other"})
			default:
				plans = append(plans, a.addColumn(key, tbl, it))
			}
		}
	case "modify":
		switch at(1) {
		case "constraint", "primary", "unique":
			plans = append(plans, a.modifyConstraint(key, tbl, rest))
		case "partition", "subpartition", "default", "lob", "varray", "nested", "opaque":
			plans = append(plans, opPlan{what: "modify " + at(1), mode: "EXCLUSIVE", work: "none", op: "other"})
		default:
			for _, it := range items(rest, 1) {
				plans = append(plans, a.modifyColumn(key, tbl, it))
			}
		}
	case "drop":
		plans = append(plans, a.drop(key, tbl, rest))
	case "set":
		if at(1) == "unused" {
			cols := columnList(rest[2:])
			for _, c := range cols {
				a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: key, Column: c, Change: "dropped"})
				a.drops = append(a.drops, "column "+key+"."+c)
			}
			a.add(ruleDropsData, score.High, fmt.Sprintf("SET UNUSED hides %s of %s for good: no statement brings an unused column back, and its data is lost", strings.Join(cols, ", "), key),
				"Take a backup first; stop using the column in one release and remove it in a later one")
			p := opPlan{what: "set unused " + strings.Join(cols, ", "), mode: "EXCLUSIVE", work: "none", op: "set unused"}
			if hasWord(rest, "online") {
				p.mode, p.dml, p.op = "ROW EXCLUSIVE", true, "set unused online"
			}
			plans = append(plans, p)
		}
	case "rename":
		switch at(1) {
		case "column":
			if len(rest) > 4 {
				from, to := ident(rest[2]), ident(rest[4])
				a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: key, Column: from, Change: "renamed", To: to})
			}
			plans = append(plans, opPlan{what: "rename a column", mode: "EXCLUSIVE", work: "none", op: "rename column"})
		case "to":
			if len(rest) > 2 {
				_, to, _ := qname(rest, 2)
				a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: key, Change: "renamed", To: to})
			}
			plans = append(plans, opPlan{what: "rename the table", mode: "EXCLUSIVE", work: "none", op: "rename table"})
		default:
			plans = append(plans, opPlan{what: "rename " + at(1), mode: "EXCLUSIVE", work: "none", op: "other"})
		}
	case "move":
		plans = append(plans, a.move(key, tbl, rest))
	case "enable", "disable":
		plans = append(plans, a.enableConstraint(key, tbl, rest))
	default:
		plans = append(plans, opPlan{what: strings.Join(w[:min(2, len(w))], " "), mode: "EXCLUSIVE", work: "none", op: "other"})
	}
	a.finish(key, tbl, plans)
}

// finish turns a statement's plans into its locks, work, size findings and the busy-table finding.
func (a *ddlAnalysis) finish(key string, tbl *TableInfo, plans []opPlan) {
	if len(plans) == 0 {
		return
	}
	p := plans[0]
	workOp := p.op
	var whats []string
	for _, q := range plans {
		whats = append(whats, q.what)
		if workRank[q.work] > workRank[p.work] {
			p.work, workOp = q.work, q.op
		}
		if modeRank[q.mode] > modeRank[p.mode] {
			p.mode = q.mode
		}
		p.dml = p.dml && q.dml
		if behaviorOf(q.op).rank() > behaviorOf(p.op).rank() {
			p.op = q.op
		}
	}
	p.what = strings.Join(whats, ", ")
	a.lock(key, p.mode)
	a.out.Work = p.work
	if tbl != nil && tbl.Sized {
		n := tbl.Bytes
		a.out.SizeBytes = &n
	}
	est := p
	est.op = workOp
	a.out.Estimate = ddlEstimate(est, tbl)
	a.sizeRules(key, tbl, p)
	a.busy(key, behaviorOf(p.op))
}

// sizeRules rates work that reads or writes the whole table by its size.
// The tiers are those of the MySQL check (docs/ENGINES.md): from 1M rows or
// 1 GiB, from 100k rows or 100 MiB, and below.
func (a *ddlAnalysis) sizeRules(key string, tbl *TableInfo, p opPlan) {
	if p.work == "none" {
		return
	}
	tier := 1
	if tbl != nil && (tbl.Sized || tbl.RowsKnown) {
		switch {
		case tbl.Rows >= 1_000_000 || tbl.Bytes >= 1<<30:
			tier = 2
		case tbl.Rows >= 100_000 || tbl.Bytes >= 100<<20:
			tier = 1
		default:
			tier = 0
		}
	}
	size := sizeText(tbl)
	if size == "" {
		size = " (of unknown size)"
	}
	verb := map[string]string{"scan": "reads every row of", "index_build": "builds an index by reading", "rewrite": "rewrites every row of"}[p.work]
	if !p.dml {
		r := []score.Rating{score.Medium, score.High, score.Critical}[tier]
		if tbl == nil || !(tbl.Sized || tbl.RowsKnown) {
			r = score.High
		}
		a.add(ruleBlocksDML, r, fmt.Sprintf("%s %s %s%s while holding a %s lock: inserts, updates and deletes on it wait for the whole time", p.what, verb, key, size, p.mode),
			a.blockingAlt(p))
		return
	}
	r := []score.Rating{score.Low, score.Low, score.Medium}[tier]
	a.add(ruleTableWork, r, fmt.Sprintf("%s %s %s%s; writes continue meanwhile", p.what, verb, key, size), "")
}

func (a *ddlAnalysis) blockingAlt(p opPlan) string {
	switch {
	case p.op == "add constraint unique", p.op == "add column index":
		return "CREATE UNIQUE INDEX ... ONLINE first, then ADD the constraint USING INDEX that index"
	case strings.HasPrefix(p.op, "create index"), strings.HasPrefix(p.op, "rebuild index"), strings.HasPrefix(p.op, "move"):
		if a.base.OnlineKnown && !a.base.Online {
			return "This edition has no Online Index Build (v$option), so ONLINE is not available: run it when writes can pause"
		}
		return "Add ONLINE, so writes continue while it builds"
	case strings.HasPrefix(p.op, "add constraint"), p.op == "modify not null":
		return "Add the constraint ENABLE NOVALIDATE (instant), then validate it later with ALTER TABLE ... MODIFY CONSTRAINT name VALIDATE, which lets writes continue"
	case p.op == "drop column":
		return "SET UNUSED (c) ONLINE removes the column from use at once, without touching rows; drop the unused columns in a quiet hour"
	}
	return "Run it when writes can pause, or use DBMS_REDEFINITION to change the table online"
}

// busy adds the finding for what the statement does when another
// transaction has uncommitted changes on the table.
func (a *ddlAnalysis) busy(key string, b behavior) {
	switch {
	case b.waitsTx:
		a.add(ruleDDLLock, score.Low, fmt.Sprintf("it waits for every open transaction that has changed %s to end, whatever ddl_lock_timeout is; writes continue meanwhile, but the migration stalls behind a long transaction", key), "")
	case b.nowait && a.timeout == 0:
		a.out.LockTimeout = true
		a.add(ruleDDLLock, score.Medium,
			fmt.Sprintf("it needs an exclusive lock on %s at once: if any transaction has uncommitted changes to the table, it fails with ORA-00054 (ddl_lock_timeout is 0, the default). Oracle commits before and after every DDL, so the script stops part way with the earlier statements applied, unlike PostgreSQL, where DDL queues for its lock", key),
			"Run ALTER SESSION SET ddl_lock_timeout = 5 earlier in the script, so it waits up to 5 s for open transactions before failing; keep it short, because DML arriving meanwhile queues behind the waiting DDL")
	case b.nowait:
		a.out.LockTimeout = true
		a.add(ruleDDLLock, score.Low, fmt.Sprintf("if a transaction has uncommitted changes to %s, it waits up to %d s (ddl_lock_timeout) for the table lock and then fails with ORA-00054; while it waits, inserts, updates and deletes arriving on the table queue behind it", key, a.timeout), "")
	}
}

// columnList reads "c" or "(c1, c2)" and the COLUMN keyword before it.
func columnList(toks []Token) []string {
	if len(toks) > 0 && toks[0].Word == "column" {
		toks = toks[1:]
	}
	var out []string
	for _, it := range items(toks, 0) {
		if len(it) > 0 && isName(it[0]) {
			if w := it[0].Word; w == "online" || w == "cascade" || w == "checkpoint" || w == "invalidate" {
				continue
			}
			out = append(out, ident(it[0]))
		}
		if toks[0].Text != "(" {
			break
		}
	}
	return out
}

func onlineOp(op string, toks []Token) string {
	if hasWord(toks, "online") {
		return op + " online"
	}
	return op
}

// constraintName reads CONSTRAINT name at the start of item, or "".
func constraintName(item []Token) string {
	if len(item) > 1 && item[0].Word == "constraint" {
		return ident(item[1])
	}
	return ""
}

// novalidate reports ENABLE NOVALIDATE or DISABLE in a constraint clause.
func novalidate(toks []Token) bool {
	return hasWord(toks, "novalidate", "disable")
}

func (a *ddlAnalysis) addColumn(key string, tbl *TableInfo, item []Token) opPlan {
	if len(item) == 0 {
		return opPlan{mode: "EXCLUSIVE", work: "none", op: "other"}
	}
	col := ident(item[0])
	def := item[1:]
	dw := words(def)
	a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: key, Column: col, Change: "added"})
	p := opPlan{what: "add column " + col, mode: "ROW EXCLUSIVE", dml: true, work: "none", op: "add column"}
	hasDefault := hasWord(def, "default")
	notNull := hasSeq(dw, "not", "null")
	switch {
	case hasWord(def, "identity"):
		p = opPlan{what: "add identity column " + col, mode: "EXCLUSIVE", work: "rewrite", op: "add identity"}
	case hasWord(def, "as") && !hasWord(def, "identity"):
		p.what, p.op = "add virtual column "+col, "add virtual"
	case hasWord(def, "check", "references", "unique", "primary"):
		p = opPlan{what: "add column " + col + " with a constraint", mode: "EXCLUSIVE", work: "none", op: "add column constraint"}
		if hasWord(def, "unique", "primary") {
			p.work, p.op = "index_build", "add column index"
		}
	}
	if notNull && !hasDefault && !hasWord(def, "identity") {
		if tbl == nil || !tbl.RowsKnown || tbl.Rows > 0 {
			sure := "if " + key + " has any rows"
			if tbl != nil && tbl.RowsKnown {
				sure = fmt.Sprintf("because %s has rows (%d by its statistics)", key, tbl.Rows)
			}
			a.add(ruleAddNotNull, score.High, fmt.Sprintf("ADD %s ... NOT NULL without a DEFAULT fails with ORA-01758 %s", col, sure),
				"Give the column a DEFAULT (stored as metadata, no rows are updated), or add it nullable, backfill it, then MODIFY it NOT NULL")
		}
	}
	return p
}

func (a *ddlAnalysis) modifyColumn(key string, tbl *TableInfo, item []Token) opPlan {
	if len(item) == 0 {
		return opPlan{mode: "EXCLUSIVE", work: "none", op: "other"}
	}
	col := ident(item[0])
	def := item[1:]
	dw := words(def)
	a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: key, Column: col, Change: "altered"})
	var base *ColumnInfo
	if tbl != nil {
		if c, ok := tbl.Columns[col]; ok {
			base = &c
		}
	}
	p := opPlan{what: "modify " + col, mode: "EXCLUSIVE", work: "none", op: "modify column"}
	rows := tbl == nil || !tbl.RowsKnown || tbl.Rows > 0
	if len(def) > 0 && def[0].Word != "" && !isAttrWord(def[0].Word) {
		newType := typeOf(def)
		if base == nil {
			p.what = fmt.Sprintf("change %s to %s (the base column is unknown)", col, newType)
		} else if oldType := normType(base.Type); oldType != newType {
			p = a.typeChange(key, col, oldType, newType, rows, p)
		}
	}
	switch {
	case hasSeq(dw, "not", "null") && !novalidate(def):
		if base == nil || base.Nullable {
			p.what += ", set NOT NULL"
			if workRank[p.work] < workRank["scan"] {
				p.work = "scan"
			}
			p.op = "modify not null"
			a.add(ruleNotNull, score.Medium, fmt.Sprintf("NOT NULL on %s reads every row to check it, and fails with ORA-02296 if any row holds NULL", col),
				fmt.Sprintf("ALTER TABLE %s MODIFY %s CONSTRAINT <name> NOT NULL ENABLE NOVALIDATE (instant, enforced for new rows), then ALTER TABLE %s MODIFY CONSTRAINT <name> VALIDATE later, which lets writes continue", key, col, key))
		}
	case hasSeq(dw, "not", "null"):
		p.what += ", set NOT NULL ENABLE NOVALIDATE"
		if p.op == "modify column" {
			p.op = "modify not null novalidate"
		}
	}
	return p
}

// isAttrWord begins a column modification that keeps the type.
func isAttrWord(w string) bool {
	switch w {
	case "default", "not", "null", "constraint", "visible", "invisible", "encrypt", "decrypt", "collate", "check", "enable", "disable", "generated", "drop", "lob":
		return true
	}
	return false
}

// normType spells a dictionary type as typeOf spells a definition's.
func normType(t string) string {
	return strings.ToUpper(strings.ReplaceAll(t, " ", ""))
}

func (a *ddlAnalysis) typeChange(key, col, oldType, newType string, rows bool, p opPlan) opPlan {
	ok, oldN := typeLength(oldType)
	okNew, newN := typeLength(newType)
	p.what = fmt.Sprintf("change %s from %s to %s", col, oldType, newType)
	switch {
	case ok && okNew && typeFamily(oldType) == typeFamily(newType) && newN >= oldN && !(charSemantics(oldType) && !charSemantics(newType)):
		p.what = fmt.Sprintf("widen %s from %s to %s", col, oldType, newType)
	case ok && okNew && typeFamily(oldType) == "NUMBER" && typeFamily(newType) == "NUMBER":
		if rows {
			a.add(ruleTypeChange, score.High, fmt.Sprintf("lowering the precision of %s from %s to %s needs the column to be empty: with data it fails with ORA-01440, whatever the values", col, oldType, newType),
				"Add a new column of the new type, copy the data in batches, then switch the application over and drop the old column")
		}
	case ok && okNew && typeFamily(oldType) == typeFamily(newType):
		p.work, p.op = "scan", "modify narrow"
		a.add(ruleNarrow, score.Medium, fmt.Sprintf("narrowing %s from %s to %s reads every row, and fails with ORA-01441 if any value is longer", col, oldType, newType),
			"Check the longest value first (SELECT max(length(col)) ...), or keep the wider type")
	default:
		if rows {
			a.add(ruleTypeChange, score.High, fmt.Sprintf("changing %s from %s to %s needs the column to be empty: with data it fails with ORA-01439", col, oldType, newType),
				"Add a new column of the new type, copy the data in batches, then switch the application over and drop the old column")
		}
	}
	return p
}

// charSemantics reports a length counted in characters, as in VARCHAR2(40 CHAR).
func charSemantics(t string) bool { return strings.HasSuffix(t, "CHAR)") }

// typeFamily groups types whose length can change in place.
func typeFamily(t string) string {
	name, _, _ := strings.Cut(t, "(")
	switch name {
	case "VARCHAR2", "VARCHAR":
		return "VARCHAR2"
	case "NUMBER", "INTEGER", "INT":
		return "NUMBER"
	}
	return name
}

// typeLength reads the length or precision of VARCHAR2(n [CHAR|BYTE]),
// CHAR(n), RAW(n) or NUMBER(p[,s]); NUMBER without one is the widest.
func typeLength(t string) (bool, int) {
	name, args, _ := strings.Cut(t, "(")
	if name == "NUMBER" && args == "" {
		return true, 39
	}
	if name == "INTEGER" || name == "INT" {
		return true, 38
	}
	var n int
	if _, err := fmt.Sscanf(args, "%d", &n); err != nil {
		return false, 0
	}
	if name == "NUMBER" && strings.Contains(args, ",") {
		return false, 0 // scale changes are not a simple widening
	}
	switch name {
	case "VARCHAR2", "VARCHAR", "CHAR", "NVARCHAR2", "NCHAR", "RAW", "NUMBER":
		return true, n
	}
	return false, 0
}

func (a *ddlAnalysis) addConstraint(key string, tbl *TableInfo, item, stmt []Token) opPlan {
	iw := words(item)
	name := constraintName(item)
	label := "a constraint"
	if name != "" {
		label = name
	}
	switch {
	case has(iw, "primary"), has(iw, "unique"):
		kind := "a unique constraint"
		if has(iw, "primary") {
			kind = "a primary key"
		}
		p := opPlan{what: "add " + kind, mode: "SHARE", work: "index_build", op: "add constraint unique"}
		if hasSeq(iw, "using", "index") {
			// USING INDEX name reuses an existing index; a USING INDEX (CREATE INDEX ...) builds one.
			if i := indexOf(item, "index"); i >= 0 && i+1 < len(item) && item[i+1].Text != "(" {
				p.work, p.op = "scan", "add constraint unique existing"
			}
		}
		if !novalidate(item) {
			a.add(ruleUniqueData, score.Medium, fmt.Sprintf("%s fails if existing rows have duplicates (ORA-02299 for a unique constraint), or NULLs for a primary key, which a schema check cannot see", kind),
				"CREATE UNIQUE INDEX ... ONLINE first, then ADD CONSTRAINT ... USING INDEX that index")
		}
		return p
	case has(iw, "foreign"), has(iw, "references"), has(iw, "check"):
		kind := "a check constraint"
		if has(iw, "foreign") || has(iw, "references") {
			kind = "a foreign key"
		}
		if novalidate(item) {
			return opPlan{what: "add " + kind + " " + label + " ENABLE NOVALIDATE", mode: "ROW SHARE", dml: true, work: "none", op: "add constraint novalidate"}
		}
		a.add(ruleValidate, score.Medium, fmt.Sprintf("adding %s %s reads every row of %s to validate it, and fails with %s if any row breaks it", kind, label, key, map[bool]string{true: "ORA-02298", false: "ORA-02293"}[kind == "a foreign key"]),
			"Add it ENABLE NOVALIDATE (instant, enforced for new rows), then run ALTER TABLE ... MODIFY CONSTRAINT name VALIDATE later, which lets writes continue")
		if name != "" {
			a.constraintRewrite(key, stmt, name)
		}
		return opPlan{what: "add " + kind + " " + label, mode: "EXCLUSIVE", work: "scan", op: "add constraint validate"}
	}
	return opPlan{what: "add " + label, mode: "EXCLUSIVE", work: "none", op: "other"}
}

// constraintRewrite splits ADD CONSTRAINT into an instant ENABLE NOVALIDATE and a later VALIDATE.
func (a *ddlAnalysis) constraintRewrite(key string, stmt []Token, name string) {
	if hasWord(stmt, "enable", "validate", "rely", "deferrable", "initially", "exceptions", "using") {
		return
	}
	a.rewrite = &migrate.Rewrite{SQL: strings.TrimSpace(a.out.SQL) + " ENABLE NOVALIDATE;\nALTER TABLE " + quoteKey(key) + " MODIFY CONSTRAINT " + quoteIdent(name) + " VALIDATE",
		Note: "Adds the constraint without checking existing rows (instant; new and changed rows are checked from then on), then validates the existing rows in a second statement that lets writes continue. Until the second one runs, the constraint is NOVALIDATE."}
}

// quoteKey quotes the parts of "OWNER.TABLE" that need it.
func quoteKey(key string) string {
	owner, name, ok := strings.Cut(key, ".")
	if !ok {
		return quoteIdent(key)
	}
	return quoteIdent(owner) + "." + quoteIdent(name)
}

func quoteIdent(s string) string {
	if s == strings.ToUpper(s) && !strings.ContainsAny(s, " \"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func has(w []string, x string) bool {
	for _, y := range w {
		if y == x {
			return true
		}
	}
	return false
}

// modifyConstraint rates MODIFY CONSTRAINT name ENABLE [NO]VALIDATE and the like.
func (a *ddlAnalysis) modifyConstraint(key string, tbl *TableInfo, rest []Token) opPlan {
	w := words(rest)
	switch {
	case has(w, "validate") && !has(w, "novalidate") && !has(w, "enable") && !has(w, "disable"):
		return opPlan{what: "validate a constraint", mode: "ROW SHARE", dml: true, work: "scan", op: "validate constraint"}
	case has(w, "enable") && !has(w, "novalidate"):
		a.add(ruleValidate, score.Medium, "ENABLE VALIDATE reads every row of "+key+" while blocking writes when the constraint was disabled, and fails if any row breaks it",
			"ENABLE NOVALIDATE first, then MODIFY CONSTRAINT name VALIDATE, which lets writes continue")
		return opPlan{what: "enable and validate a constraint", mode: "EXCLUSIVE", work: "scan", op: "enable validate"}
	}
	return opPlan{what: "modify a constraint", mode: "EXCLUSIVE", work: "none", op: "other"}
}

func (a *ddlAnalysis) enableConstraint(key string, tbl *TableInfo, rest []Token) opPlan {
	w := words(rest)
	if w[0] == "disable" || has(w, "novalidate") || has(w, "trigger") || has(w, "triggers") || has(w, "table") {
		return opPlan{what: strings.Join(w[:min(3, len(w))], " "), mode: "EXCLUSIVE", work: "none", op: "other"}
	}
	return a.modifyConstraint(key, tbl, rest)
}

// drop rates ALTER TABLE ... DROP.
func (a *ddlAnalysis) drop(key string, tbl *TableInfo, rest []Token) opPlan {
	w := words(rest)
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	switch at(1) {
	case "constraint", "primary", "unique":
		cname := ""
		switch at(1) {
		case "constraint":
			if len(rest) > 2 {
				cname = ident(rest[2])
			}
		case "primary":
			if tbl != nil {
				cname = tbl.PrimaryKey
			}
		}
		if tbl != nil && cname != "" {
			if lost, ok := tbl.Constraints[cname]; ok {
				a.add(ruleDropConstraint, score.Medium, fmt.Sprintf("drops %s on %s: %s", cname, key, lost),
					"Enforce the rule in the application first, or add the replacement before dropping this one")
			}
		}
		return opPlan{what: "drop " + strings.ToLower(strings.Join(w[1:min(3, len(w))], " ")), mode: "EXCLUSIVE", work: "none", op: onlineOp("drop constraint", rest)}
	case "unused":
		return opPlan{what: "drop unused columns", mode: "EXCLUSIVE", work: "rewrite", op: "drop column"}
	case "partition", "subpartition":
		a.add(ruleDropsData, score.High, "DROP PARTITION deletes every row in the partition of "+key, "Take a backup first, or EXCHANGE PARTITION it into a table to keep the rows")
		return opPlan{what: "drop a partition", mode: "EXCLUSIVE", work: "none", op: "other"}
	case "period", "supplemental":
		return opPlan{what: "drop " + at(1), mode: "EXCLUSIVE", work: "none", op: "other"}
	}
	cols := columnList(rest[1:])
	for _, c := range cols {
		a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: key, Column: c, Change: "dropped"})
		a.drops = append(a.drops, "column "+key+"."+c)
	}
	list := strings.Join(cols, ", ")
	a.add(ruleDropsData, score.High, fmt.Sprintf("DROP COLUMN removes %s from %s with its data; no FLASHBACK brings a dropped column back", list, key),
		"Take a backup first; stop using it in one release and drop it in a later one")
	if !hasWord(rest, "online") && len(cols) > 0 {
		a.rewrite = &migrate.Rewrite{SQL: "ALTER TABLE " + quoteKey(key) + " SET UNUSED (" + strings.Join(quoteAll(cols), ", ") + ") ONLINE",
			Note: "SET UNUSED removes the column from use at once without touching any row, and ONLINE lets writes continue; the space is reclaimed later by ALTER TABLE ... DROP UNUSED COLUMNS, which rewrites the rows. The data is just as lost."}
	}
	return opPlan{what: "drop column " + list, mode: "EXCLUSIVE", work: "rewrite", op: onlineOp("drop column", rest)}
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = quoteIdent(x)
	}
	return out
}

// move rates ALTER TABLE ... MOVE, which writes the table to new segments.
func (a *ddlAnalysis) move(key string, tbl *TableInfo, rest []Token) opPlan {
	online := hasWord(rest, "online")
	if online {
		a.needOnline("ALTER TABLE ... MOVE ONLINE")
		return opPlan{what: "move the table online", mode: "ROW SHARE", dml: true, work: "rewrite", op: "move online"}
	}
	if !hasSeq(words(rest), "update", "indexes") {
		a.add(ruleUnusable, score.High, "MOVE without ONLINE or UPDATE INDEXES leaves every index of "+key+" UNUSABLE: queries stop using them and inserts fail on unique ones until each is rebuilt",
			"Use MOVE ONLINE, which keeps the indexes usable and lets writes continue")
	}
	if a.base.Online || !a.base.OnlineKnown {
		a.rewrite = &migrate.Rewrite{SQL: strings.TrimSpace(a.out.SQL) + " ONLINE", Note: "ONLINE lets writes continue while the table is moved and keeps its indexes usable; it needs an edition with Online Index Build (v$option)."}
	}
	return opPlan{what: "move the table", mode: "EXCLUSIVE", work: "rewrite", op: "move"}
}

// needOnline flags ONLINE on an edition without Online Index Build.
func (a *ddlAnalysis) needOnline(what string) {
	if a.base.OnlineKnown && !a.base.Online {
		a.add(ruleOnline, score.High, what+" fails with ORA-00439 on this edition: v$option shows no Online Index Build, which Enterprise Edition (and Free) has and Standard Edition 2 lacks",
			"Leave ONLINE out and run it when writes can pause")
	}
}

func (a *ddlAnalysis) createIndex(toks []Token, i int) {
	on := indexOf(toks, "on")
	if on < 0 {
		return
	}
	idxOwner, _, _ := qname(toks, i+1)
	owner, name, _ := qname(toks, on+1)
	if owner == "" {
		owner = idxOwner
	}
	key, tbl := a.table(owner, name)
	a.altered[key] = true
	online := hasWord(toks[on:], "online")
	unique := hasWord(toks[:i], "unique")
	what := "CREATE INDEX"
	if unique {
		what = "CREATE UNIQUE INDEX"
		a.add(ruleUniqueData, score.Medium, "a unique index fails with ORA-01452 if existing rows have duplicates, which a schema check cannot see", "Check for duplicates first with a GROUP BY ... HAVING count(*) > 1")
	}
	if online {
		a.needOnline(what + " ... ONLINE")
		a.finish(key, tbl, []opPlan{{what: what + " ONLINE", mode: "ROW SHARE", dml: true, work: "index_build", op: "create index online"}})
		return
	}
	if a.base.Online || !a.base.OnlineKnown {
		note := "ONLINE lets inserts, updates and deletes continue while the index builds; it waits for transactions open on the table at its start and end instead of failing."
		if !a.base.OnlineKnown {
			note += " It needs an edition with Online Index Build (Enterprise or Free; v$option could not be read here)."
		}
		a.rewrite = &migrate.Rewrite{SQL: strings.TrimSpace(a.out.SQL) + " ONLINE", Note: note}
	}
	a.finish(key, tbl, []opPlan{{what: what, mode: "SHARE", work: "index_build", op: "create index"}})
}

func (a *ddlAnalysis) alterIndex(toks []Token) {
	owner, name, next := qname(toks, 2)
	if owner == "" {
		owner = a.base.Schema
	}
	key := a.base.Indexes[owner+"."+name]
	a.out.Touch(owner + "." + name)
	w := words(toks[next:])
	if len(w) == 0 || w[0] != "rebuild" || key == "" {
		return
	}
	tbl := a.base.Tables[key]
	if has(w, "online") {
		a.needOnline("ALTER INDEX ... REBUILD ONLINE")
		a.finish(key, tbl, []opPlan{{what: "rebuild index " + name + " online", mode: "ROW SHARE", dml: true, work: "index_build", op: "rebuild index online"}})
		return
	}
	a.finish(key, tbl, []opPlan{{what: "rebuild index " + name, mode: "SHARE", work: "index_build", op: "rebuild index"}})
}

func (a *ddlAnalysis) dropIndex(toks []Token) {
	owner, name, _ := qname(toks, 2)
	if owner == "" {
		owner = a.base.Schema
	}
	a.out.Touch(owner + "." + name)
	key := a.base.Indexes[owner+"."+name]
	if key == "" {
		return
	}
	tbl := a.base.Tables[key]
	if lost, ok := tbl.UniqueIndexes[name]; ok {
		a.add(ruleDropConstraint, score.Medium, fmt.Sprintf("drops the unique index %s on %s: %s", name, key, lost),
			"Enforce the rule in the application first, or add the replacement before dropping this one")
	}
	p := opPlan{what: "drop index " + name, mode: "EXCLUSIVE", work: "none", op: "drop index"}
	if hasWord(toks, "online") {
		p.mode, p.dml, p.op = "ROW SHARE", true, "drop index online"
	}
	a.finish(key, tbl, []opPlan{p})
}

func (a *ddlAnalysis) dropTable(toks []Token) {
	i := 2
	if i+1 < len(toks) && toks[i].Word == "if" {
		i += 2
	}
	owner, name, _ := qname(toks, i)
	key, tbl := a.table(owner, name)
	a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, key)
	a.drops = append(a.drops, "table "+key)
	a.lock(key, "EXCLUSIVE")
	purge := hasWord(toks, "purge")
	switch {
	case purge || a.base.Recyclebin == "off":
		why := "PURGE skips the recycle bin"
		if !purge {
			why = "the recycle bin is off on this server"
		}
		a.add(ruleDropsData, score.High, fmt.Sprintf("DROP TABLE removes %s%s and its data for good: %s", key, sizeText(tbl), why),
			"Take a backup first, or rename the table and drop it once nothing misses it")
	default:
		a.add(ruleDropsData, score.Medium, fmt.Sprintf("DROP TABLE removes %s%s; it goes to the recycle bin, and FLASHBACK TABLE %s TO BEFORE DROP restores it with its data until the space is reclaimed", key, sizeText(tbl), key),
			"Rename the table and drop it once nothing misses it; the recycle bin keeps it only until its tablespace needs the space")
	}
	a.busy(key, behaviorOf("drop table"))
}

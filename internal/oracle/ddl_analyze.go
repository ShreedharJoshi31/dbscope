package oracle

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/down"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// Rule ids of the Oracle migration check (docs/ENGINES.md, "Oracle").
const (
	ruleDDLLock        = "DBS-ORA-DDL-LOCK"
	ruleBlocksDML      = "DBS-ORA-BLOCKS-DML"
	ruleTableWork      = "DBS-ORA-TABLE-WORK"
	ruleAddNotNull     = "DBS-ORA-ADD-NOT-NULL"
	ruleNotNull        = "DBS-ORA-NOT-NULL"
	ruleValidate       = "DBS-ORA-VALIDATE"
	ruleUniqueData     = "DBS-ORA-UNIQUE-DATA"
	ruleNarrow         = "DBS-ORA-NARROW"
	ruleTypeChange     = "DBS-ORA-TYPE-CHANGE"
	ruleDropsData      = "DBS-ORA-DROPS-DATA"
	ruleUnusable       = "DBS-ORA-UNUSABLE-INDEXES"
	ruleOnline         = "DBS-ORA-ONLINE-EDITION"
	ruleDropConstraint = "DBS-DROP-CONSTRAINT"
)

// Analyze rates migration files against base: the table lock each DDL
// takes, whether it waits for open transactions or fails at once, whether
// writes continue, whether it scans or rewrites the table, and what
// existing data makes it fail. Predictions are those TestDDLPredictions
// checks on Oracle AI Database 26ai Free (23.26).
func Analyze(base *Base, files []ingest.Input) *migrate.Report {
	rep := &migrate.Report{Engine: "oracle", PGVersion: base.Server.Num, Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: append([]string{}, base.Warnings...), Base: base.Source,
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	if base.Server.Num > 0 && base.Server.Num < 230000 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("these predictions were checked on Oracle 23.26; this server is %s, where DDL that waits for open transactions on 23.26 may instead fail at once with ORA-00054", base.Server.Version))
	}
	a := &ddlAnalysis{base: base, rep: rep, timeout: base.DDLLockTimeout, altered: map[string]bool{}}
	if a.timeout < 0 {
		a.timeout = 0
	}
	paired := ingest.Pair(files)
	rep.Warnings = append(rep.Warnings, paired.Warnings...)
	drops := map[string][]string{}
	for _, f := range paired.Forward {
		a.drops = nil
		for _, p := range Split(f.SQL) {
			st := a.statement(f.Name, p)
			rep.Statements = append(rep.Statements, st)
			rep.Rating = score.Max(rep.Rating, st.Rating)
		}
		drops[f.Name] = a.drops
	}
	for _, f := range paired.Forward {
		if d := paired.Downs[f.Name]; d != nil {
			r := down.Result{Drops: drops[f.Name], Unchecked: "dbscope does not replay Oracle rollbacks"}
			migrate.AttachDown(rep, f.Name, down.Findings(d, r))
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
	base *Base
	rep  *migrate.Report
	// timeout is the session's ddl_lock_timeout in seconds, from the
	// instance and the script's ALTER SESSION statements.
	timeout int
	altered map[string]bool
	drops   []string // what the current file drops, for its rollback check
	out     *migrate.Statement
	rewrite *migrate.Rewrite
}

func (a *ddlAnalysis) add(id string, r score.Rating, msg, alt string) {
	a.out.Findings = append(a.out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
	a.out.Rating = score.Max(a.out.Rating, r)
}

// table resolves an owner-qualified or bare name to "OWNER.TABLE" and its base info.
func (a *ddlAnalysis) table(owner, name string) (string, *TableInfo) {
	if owner == "" {
		owner = a.base.Schema
	}
	key := name
	if owner != "" {
		key = owner + "." + name
	}
	return key, a.base.Tables[key]
}

func (a *ddlAnalysis) statement(file string, p Piece) migrate.Statement {
	out := migrate.Statement{File: file, Line: p.Line, Column: p.Column, EndLine: p.EndLine, SQL: p.SQL,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	a.out, a.rewrite = &out, nil
	toks := Tokens(p.SQL)
	w := words(toks)
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	switch {
	case p.Client:
		out.Kind = "SQL*PLUS " + strings.ToUpper(at(0))
	case p.Block:
		out.Kind = "PL/SQL"
		if at(0) == "create" {
			for _, x := range w[1:] {
				if x != "or" && x != "replace" && x != "editionable" && x != "noneditionable" {
					out.Kind = "CREATE " + strings.ToUpper(x)
					break
				}
			}
		}
		if out.Kind == "PL/SQL" && (hasSeq(w, "execute", "immediate") || hasSeq(w, "dbms_sql.parse") || at(0) == "exec" || at(0) == "execute") {
			a.rep.Unanalyzable = append(a.rep.Unanalyzable, migrate.Unanalyzable{File: file, Line: p.Line,
				Message: "a PL/SQL block runs SQL dbscope does not see (EXECUTE IMMEDIATE, DBMS_SQL or a procedure call); check its DDL by hand"})
		}
	default:
		a.classify(toks, w)
	}
	out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
	out.Rewrite = a.rewrite
	return out
}

func (a *ddlAnalysis) classify(toks []Token, w []string) {
	out := a.out
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	out.Kind = strings.ToUpper(at(0))
	switch at(0) {
	case "alter":
		out.Kind = "ALTER " + strings.ToUpper(at(1))
		switch at(1) {
		case "session":
			a.session(toks)
		case "table":
			owner, name, next := qname(toks, 2)
			a.alterTable(owner, name, toks[next:])
		case "index":
			a.alterIndex(toks)
		}
	case "create":
		i := 1
		for i < len(toks) && (toks[i].Word == "or" || toks[i].Word == "replace" || toks[i].Word == "global" || toks[i].Word == "private" ||
			toks[i].Word == "temporary" || toks[i].Word == "sharded" || toks[i].Word == "duplicated" || toks[i].Word == "blockchain" ||
			toks[i].Word == "immutable" || toks[i].Word == "unique" || toks[i].Word == "bitmap" || toks[i].Word == "multivalue") {
			i++
		}
		if i >= len(toks) {
			return
		}
		out.Kind = "CREATE " + strings.ToUpper(toks[i].Word)
		switch toks[i].Word {
		case "index":
			a.createIndex(toks, i)
		case "table":
			j := i + 1
			if j+2 < len(toks) && toks[j].Word == "if" {
				j += 3
			}
			owner, name, _ := qname(toks, j)
			key, _ := a.table(owner, name)
			a.rep.Diff.Added = append(a.rep.Diff.Added, key)
			out.Touch(key)
		}
	case "drop":
		out.Kind = "DROP " + strings.ToUpper(at(1))
		switch at(1) {
		case "table":
			a.dropTable(toks)
		case "index":
			a.dropIndex(toks)
		}
	case "truncate":
		out.Kind = "TRUNCATE TABLE"
		i := 1
		if at(1) == "table" {
			i = 2
		}
		owner, name, _ := qname(toks, i)
		key, tbl := a.table(owner, name)
		out.Touch(key)
		a.lock(key, "EXCLUSIVE")
		a.add(ruleDropsData, score.High, "TRUNCATE deletes every row of "+key+sizeText(tbl)+"; it commits at once and FLASHBACK TABLE cannot bring the rows back",
			"Take a backup first (for example CREATE TABLE ... AS SELECT), or delete in batches if the rows must stay recoverable until commit")
		a.busy(key, behaviorOf("truncate"))
	case "rename":
		out.Kind = "RENAME"
		owner, name, next := qname(toks, 1)
		key, _ := a.table(owner, name)
		out.Touch(key)
		a.altered[key] = true
		a.lock(key, "EXCLUSIVE")
		a.busy(key, behaviorOf("rename table"))
		if next+1 < len(toks) {
			_, to, _ := qname(toks, next+1)
			a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: key, Change: "renamed", To: to})
		}
	case "insert", "update", "delete", "merge":
		out.Tx = "implicit"
		if i := indexOf(toks, map[string]string{"insert": "into", "update": "update", "delete": "from", "merge": "into"}[at(0)]); i >= 0 {
			owner, name, _ := qname(toks, i+1)
			key, _ := a.table(owner, name)
			out.Touch(key)
		}
	}
}

// session follows ALTER SESSION SET ddl_lock_timeout = n.
func (a *ddlAnalysis) session(toks []Token) {
	for i, t := range toks {
		if t.Word == "ddl_lock_timeout" {
			for _, v := range toks[i+1:] {
				if n, err := strconv.Atoi(v.Text); err == nil {
					a.timeout = n
					return
				}
			}
		}
	}
}

func (a *ddlAnalysis) lock(key, mode string) {
	a.out.Touch(key)
	for _, l := range a.out.Locks {
		if l.Relation == key && l.Mode == mode {
			return
		}
	}
	a.out.Locks = append(a.out.Locks, migrate.Lock{Relation: key, Mode: mode})
}

func sizeText(t *TableInfo) string {
	switch {
	case t == nil:
		return ""
	case t.RowsKnown && t.Sized:
		return fmt.Sprintf(" (%d rows, %s)", t.Rows, bytesText(t.Bytes))
	case t.Sized:
		return " (" + bytesText(t.Bytes) + ")"
	case t.RowsKnown:
		return fmt.Sprintf(" (%d rows)", t.Rows)
	}
	return ""
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

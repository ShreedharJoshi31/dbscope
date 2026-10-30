package mssql

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

// Lock modes as sys.dm_tran_locks names them.
const (
	lockSchM = "Sch-M"
	lockS    = "S"
	lockSchS = "Sch-S"
)

// Analyze rates T-SQL migration files against base: the lock each
// statement takes, whether it rewrites or reads the whole table while
// holding it, and whether it fails on existing rows (docs/ENGINES.md, "SQL
// Server and Azure SQL"). ddl_verify_test.go checks the predictions on real servers.
func Analyze(base *DDLBase, files []ingest.Input) *migrate.Report {
	rep := &migrate.Report{Engine: "sqlserver", PGVersion: base.Server.Num, Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: append([]string{}, base.Warnings...), Base: base.Source,
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	if base.Source == "none" {
		rep.Warnings = append(rep.Warnings, "no base schema: table sizes, column types and constraints are unknown")
	}
	paired := ingest.Pair(files)
	rep.Warnings = append(rep.Warnings, paired.Warnings...)
	altered := map[string]bool{}
	for _, f := range paired.Forward {
		a := &ddlAnalysis{base: base, rep: rep, altered: altered, lockTimeout: -1, dropped: map[string]bool{}}
		for _, p := range Split(f.SQL) {
			st := a.statement(f.Name, p)
			rep.Statements = append(rep.Statements, st)
			rep.Rating = score.Max(rep.Rating, st.Rating)
		}
		if d := paired.Downs[f.Name]; d != nil {
			migrate.AttachDown(rep, f.Name, down.Findings(d, down.Result{Drops: a.drops,
				Unchecked: "SQL Server down migrations are not replayed"}))
		}
	}
	sort.Strings(rep.Diff.Added)
	sort.Strings(rep.Diff.Dropped)
	for k := range altered {
		rep.Diff.Altered = append(rep.Diff.Altered, k)
	}
	sort.Strings(rep.Diff.Altered)
	return rep
}

type ddlAnalysis struct {
	base    *DDLBase
	rep     *migrate.Report
	altered map[string]bool
	// lockTimeout is SET LOCK_TIMEOUT in milliseconds; -1, the default, waits forever.
	lockTimeout int
	// drops is the data the file removes, for the rollback check.
	drops []string
	// dropped marks tables whose indexes or constraints the file dropped,
	// which may be what blocked a later ALTER or DROP COLUMN.
	dropped map[string]bool
	// droppedFK: the file dropped a foreign key, perhaps one that pointed at a table it later drops or truncates.
	droppedFK bool
}

type adder func(id string, r score.Rating, msg, alt string)

func (a *ddlAnalysis) statement(file string, p Piece) migrate.Statement {
	out := migrate.Statement{File: file, Line: p.Line, Column: p.Column, EndLine: p.EndLine, SQL: p.SQL,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	add := func(id string, r score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
		out.Rating = score.Max(out.Rating, r)
	}
	toks := Tokens(p.SQL)
	if n := len(toks); n > 0 && toks[n-1].Semi {
		toks = toks[:n-1]
	}
	at := func(i int) string {
		if i < len(toks) {
			return toks[i].Word
		}
		return ""
	}
	finish := func() migrate.Statement {
		out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
		return out
	}
	out.Kind = strings.ToUpper(at(0))

	switch at(0) {
	case "set":
		if at(1) == "lock_timeout" {
			a.lockTimeout = setNumber(toks[2:])
		}
	case "alter":
		switch at(1) {
		case "table":
			out.Kind = "ALTER TABLE"
			display, key, next := a.tableName(toks, 2)
			a.alterTable(&out, add, display, key, toks[next:])
		case "index":
			out.Kind = "ALTER INDEX"
			if on := indexOf(toks, "on"); on > 1 {
				display, key, next := a.tableName(toks, on+1)
				a.alterIndex(&out, add, display, key, toks[2], toks[next:])
			}
		}
	case "create":
		i := 1
		clustered := false
		for i < len(toks) && (toks[i].Word == "unique" || toks[i].Word == "clustered" || toks[i].Word == "nonclustered") {
			clustered = clustered || toks[i].Word == "clustered"
			i++
		}
		switch at(i) {
		case "index":
			out.Kind = "CREATE INDEX"
			if on := indexOf(toks, "on"); on > 0 {
				display, key, next := a.tableName(toks, on+1)
				a.createIndex(&out, add, display, key, toks, next, clustered)
			}
		case "table":
			out.Kind = "CREATE TABLE"
			display, _, _ := a.tableName(toks, i+1)
			a.rep.Diff.Added = append(a.rep.Diff.Added, display)
			out.Touch(display)
		default:
			out.Kind = "CREATE " + strings.ToUpper(at(i))
		}
	case "drop":
		switch at(1) {
		case "table":
			out.Kind = "DROP TABLE"
			a.dropTables(&out, add, toks)
		case "index":
			out.Kind = "DROP INDEX"
			a.dropIndexes(&out, add, toks)
		}
	case "truncate":
		out.Kind = "TRUNCATE TABLE"
		a.truncate(&out, add, toks)
	case "exec", "execute":
		if at(1) == "sp_rename" || (at(1) == "sys" && at(3) == "sp_rename") {
			out.Kind = "RENAME"
			a.rename(&out, add, toks)
		}
	}
	return finish()
}

// setNumber reads SET LOCK_TIMEOUT's value, which may be negative.
func setNumber(toks []Token) int {
	sign, s := 1, ""
	for _, t := range toks {
		switch {
		case t.Text == "-":
			sign = -1
		case t.Word != "":
			s = t.Word
		}
		if s != "" {
			break
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return sign * n
}

// ident is a name token unquoted, keeping its case.
func ident(t Token) string {
	s := t.Text
	switch {
	case len(s) >= 2 && s[0] == '[':
		return strings.ReplaceAll(s[1:len(s)-1], "]]", "]")
	case len(s) >= 2 && s[0] == '"':
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

// tableName reads a [database.][schema.]table name starting at toks[i] and
// returns it as "schema.table", its lower-case key and the index after it.
func (a *ddlAnalysis) tableName(toks []Token, i int) (display, key string, next int) {
	for i < len(toks) && (toks[i].Word == "if" || toks[i].Word == "exists" || toks[i].Word == "table") {
		i++
	}
	if i >= len(toks) {
		return "", "", i
	}
	parts := []string{ident(toks[i])}
	next = i + 1
	for next+1 < len(toks) && toks[next].Text == "." {
		parts = append(parts, ident(toks[next+1]))
		next += 2
	}
	t := parts[len(parts)-1]
	s := a.base.Schema
	if len(parts) >= 2 {
		s = parts[len(parts)-2]
	}
	if s == "" {
		s = "dbo"
	}
	return s + "." + t, strings.ToLower(s + "." + t), next
}

// lockWait adds the schema-lock wait finding to a statement that needs a
// Sch-M (or S) lock, unless SET LOCK_TIMEOUT bounds the wait.
func (a *ddlAnalysis) lockWait(out *migrate.Statement, add adder, lowPriority bool) {
	if a.lockTimeout >= 0 || lowPriority {
		out.LockTimeout = true
		return
	}
	alt := "SET LOCK_TIMEOUT 5000 earlier in the script and retry on error 1222"
	add("DBS-MSSQL-LOCK-WAIT", score.Medium,
		"DDL needs a schema modification (Sch-M) lock, at least briefly; behind any open transaction on the table it waits, "+
			"and every later query on the table queues behind it. LOCK_TIMEOUT defaults to -1, which waits forever", alt)
}

func indexOf(toks []Token, word string) int {
	for i, t := range toks {
		if t.Word == word {
			return i
		}
	}
	return -1
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

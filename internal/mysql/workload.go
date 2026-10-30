package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// maxDigests bounds the statement digests read, most total time first.
const maxDigests = 2000

// Workload reads performance_schema: row I/O per table from
// table_io_waits_summary_by_index_usage, and statement digests from
// events_statements_summary_by_digest attributed to the tables their text
// names. dbscope never changes performance_schema's setup or truncates its
// tables, so when it is off or not readable the snapshot says what an admin
// changes. Only a failure to reach the server is returned as an error.
func Workload(ctx context.Context, db *sql.DB, m *schema.Model) (*workload.Snapshot, error) {
	snap := &workload.Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*workload.Table{}}
	tx, err := db.BeginTx(ctx, readOnly)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var on, lowerCase int
	if err := tx.QueryRowContext(ctx, `SELECT @@performance_schema, @@lower_case_table_names`).Scan(&on, &lowerCase); err != nil {
		return nil, fmt.Errorf("workload: %w", err)
	}
	if on == 0 {
		snap.Statements.Status = workload.StatusNotLoaded
		snap.Statements.Message = "performance_schema is off on this server: an admin adds performance_schema=ON under [mysqld] in my.cnf and restarts it. dbscope only reads it."
		return snap, nil
	}
	rels := indexRelations(m, lowerCase != 0)
	if len(m.Schemas) == 0 {
		snap.Statements.Status = workload.StatusOK
		return snap, nil
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(m.Schemas)), ",") + ")"
	args := make([]any, len(m.Schemas))
	for i, s := range m.Schemas {
		args[i] = s
	}
	err = tableIO(ctx, tx, snap, rels, in, args)
	if err == nil {
		err = digests(ctx, tx, snap, rels, in, args)
	}
	if err != nil {
		if err := unreadable(ctx, tx, &snap.Statements, err); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

// tableIO reads rows fetched without an index (full scans) and through an
// index, and rows written, per table. MySQL keeps no count of scans, so
// SeqScans and IdxScans are row counts here.
func tableIO(ctx context.Context, tx *sql.Tx, snap *workload.Snapshot, rels relations, in string, args []any) error {
	rows, err := tx.QueryContext(ctx, `SELECT OBJECT_SCHEMA, OBJECT_NAME,
			SUM(IF(INDEX_NAME IS NULL, COUNT_FETCH, 0)), SUM(IF(INDEX_NAME IS NULL, 0, COUNT_FETCH)),
			SUM(COUNT_INSERT + COUNT_UPDATE + COUNT_DELETE)
		FROM performance_schema.table_io_waits_summary_by_index_usage
		WHERE OBJECT_TYPE = 'TABLE' AND OBJECT_SCHEMA IN `+in+`
		GROUP BY OBJECT_SCHEMA, OBJECT_NAME`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sc, name string
		var t workload.Table
		if err := rows.Scan(&sc, &name, &t.SeqScans, &t.IdxScans, &t.RowsWritten); err != nil {
			return err
		}
		k := rels.key(sc, name)
		if k == "" {
			continue
		}
		if est := rels.byKey[k].RowEstimate; est != nil {
			t.LiveRows = *est
		}
		snap.Tables[k] = &t
	}
	return rows.Err()
}

// statementKind sorts a digest: session and catalog statements are left out
// entirely, data statements are attributed, the rest (DDL, CALL, ...) count
// as not tied to a table.
type statementKind int

const (
	catalogStmt statementKind = iota
	dataStmt
	otherStmt
)

// digests reads the statements whose default database is one of the
// model's. Attribution is by text, since no column lists a digest's tables:
// a name in DIGEST_TEXT resolves as the server resolved it, qualified or in
// the statement's default database (SCHEMA_NAME). Statements executed as
// server-side prepared statements (COM_STMT_EXECUTE) have no digest row on
// MySQL 8.4 or MariaDB 11.8, so they show only in the table I/O counters.
func digests(ctx context.Context, tx *sql.Tx, snap *workload.Snapshot, rels relations, in string, args []any) error {
	info := &snap.Statements
	var enabled string
	if err := tx.QueryRowContext(ctx, `SELECT ENABLED FROM performance_schema.setup_consumers WHERE NAME = 'statements_digest'`).Scan(&enabled); err != nil {
		return err
	}
	if !strings.EqualFold(enabled, "YES") {
		info.Status = workload.StatusNotLoaded
		info.Message = "performance_schema does not collect statement digests: an admin runs UPDATE performance_schema.setup_consumers SET ENABLED = 'YES' WHERE NAME = 'statements_digest' (and adds performance-schema-consumer-statements-digest=ON to my.cnf to keep it after a restart). dbscope only reads it."
		return nil
	}
	var since sql.NullFloat64
	// performance_schema keeps no reset time: the earliest digest of any database stands in.
	if err := tx.QueryRowContext(ctx, `SELECT UNIX_TIMESTAMP(MIN(FIRST_SEEN)) FROM performance_schema.events_statements_summary_by_digest`).Scan(&since); err != nil {
		return err
	}
	if since.Valid {
		t := time.UnixMicro(int64(since.Float64 * 1e6)).UTC()
		info.Since = &t
	}
	// Window totals cover every digest of these databases, not only the ones read.
	rows, err := tx.QueryContext(ctx, `SELECT coalesce(DIGEST, ''), coalesce(DIGEST_TEXT, ''), SCHEMA_NAME, COUNT_STAR, SUM_TIMER_WAIT,
			SUM(COUNT_STAR) OVER (), SUM(SUM_TIMER_WAIT) OVER ()
		FROM performance_schema.events_statements_summary_by_digest
		WHERE SCHEMA_NAME IN `+in+`
		ORDER BY SUM_TIMER_WAIT DESC LIMIT `+fmt.Sprint(maxDigests), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var allCalls, readCalls, leftOut int64
	var allPs, leftOutPs float64
	for rows.Next() {
		var id, text, sc string
		var calls int64
		var ps float64
		if err := rows.Scan(&id, &text, &sc, &calls, &ps, &allCalls, &allPs); err != nil {
			return err
		}
		info.Read++
		readCalls += calls
		toks := Tokens(text)
		switch kindOf(toks) {
		case catalogStmt:
			leftOut += calls
			leftOutPs += ps
			continue
		case otherStmt:
			info.UnattributedCalls += calls
			continue
		}
		totalMs := ps / 1e9 // picoseconds
		keys := tablesIn(toks, sc, rels)
		if len(keys) == 0 {
			info.UnattributedCalls += calls
			continue
		}
		q := workload.Query{QueryID: id, Text: text, Calls: calls, TotalMs: totalMs}
		if calls > 0 {
			q.MeanMs = totalMs / float64(calls)
		}
		for _, k := range keys {
			t := snap.Tables[k]
			if t == nil {
				t = &workload.Table{} // a view, or a table performance_schema has no row for
				snap.Tables[k] = t
			}
			t.QueryCalls += calls
			t.QueryTimeMs += totalMs
			workload.AddTopQuery(t, q)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	info.Status = workload.StatusOK
	info.UnreadCalls = max(0, allCalls-readCalls)
	info.TotalCalls, info.TotalTimeMs = allCalls-leftOut, (allPs-leftOutPs)/1e9
	return nil
}

// kindOf classifies a digest by its first word and the schemas it names.
func kindOf(toks []Token) statementKind {
	first, from := "", false
	for i, t := range toks {
		if first == "" && t.Word != "" {
			first = t.Word
		}
		from = from || t.Word == "from"
		if t.Text == "." && i > 0 {
			if name, ok := ident(toks[i-1]); (ok && isSystem(name)) || isSystem(toks[i-1].Word) {
				return catalogStmt
			}
		}
	}
	switch first {
	case "show", "set", "explain", "describe", "desc", "use", "begin", "start", "commit", "rollback",
		"savepoint", "release", "xa", "kill", "help":
		return catalogStmt
	case "select":
		if !from {
			return catalogStmt // SELECT VERSION(), @@variables, pings
		}
		return dataStmt
	case "with", "insert", "update", "delete", "replace", "table":
		return dataStmt
	}
	return otherStmt
}

// tablesIn returns the model relations a data statement names where a
// table goes: after FROM, JOIN, UPDATE, INTO or TABLE, and after commas in
// such a list. An alias or a column named like a table is not one.
func tablesIn(toks []Token, defaultSchema string, rels relations) []string {
	var out []string
	inList, expect := false, false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		name, quoted := ident(t)
		if !quoted && !(inList && expect && t.Word != "" && !listWords[t.Word]) {
			switch t.Word {
			case "from", "join", "straight_join", "into", "table":
				inList, expect = true, true
			case "update":
				// ON DUPLICATE KEY UPDATE lists columns.
				inList = i == 0 || toks[i-1].Word != "key"
				expect = inList
			case "as", "low_priority", "high_priority", "delayed", "ignore", "quick":
			default:
				if t.Text == "," {
					expect = inList
				} else {
					inList, expect = false, false
				}
			}
			continue
		}
		// A bare word here is a name the digest printed as a keyword, upper-cased.
		fold := !quoted
		if fold {
			name = t.Text
		}
		sc := defaultSchema
		if i+2 < len(toks) && toks[i+1].Text == "." {
			if n, ok := ident(toks[i+2]); ok {
				sc, name = name, n
				i += 2
			} else if w := toks[i+2].Word; w != "" {
				sc, name, fold = name, toks[i+2].Text, true
				i += 2
			}
		}
		if inList && expect {
			if k := rels.lookup(sc, name, fold); k != "" && !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
		expect = false
	}
	return out
}

// listWords can follow FROM, JOIN or a comma without being a table name.
var listWords = map[string]bool{"select": true, "lateral": true, "json_table": true, "dual": true, "nowait": true,
	"skip": true, "of": true, "as": true, "low_priority": true, "high_priority": true, "delayed": true, "ignore": true, "quick": true}

// ident returns a backquoted name's text. DIGEST_TEXT backquotes names,
// except those that are also keywords, which it prints bare and upper-cased.
func ident(t Token) (string, bool) {
	if len(t.Text) < 2 || t.Text[0] != '`' || t.Text[len(t.Text)-1] != '`' {
		return "", false
	}
	return strings.ReplaceAll(t.Text[1:len(t.Text)-1], "``", "`"), true
}

// relations finds the model's relations by name, folding case when the
// server does (lower_case_table_names 1 or 2).
type relations struct {
	fold   bool
	keys   map[[2]string]string
	folded map[[2]string][]string
	byKey  map[string]*schema.Relation
}

func indexRelations(m *schema.Model, fold bool) relations {
	r := relations{fold: fold, keys: map[[2]string]string{}, folded: map[[2]string][]string{}, byKey: map[string]*schema.Relation{}}
	for i := range m.Relations {
		rel := &m.Relations[i]
		k := rel.QName().String()
		r.keys[r.name(rel.Schema, rel.Name)] = k
		lower := [2]string{strings.ToLower(rel.Schema), strings.ToLower(rel.Name)}
		r.folded[lower] = append(r.folded[lower], k)
		r.byKey[k] = rel
	}
	return r
}

func (r relations) name(sc, name string) [2]string {
	if r.fold {
		return [2]string{strings.ToLower(sc), strings.ToLower(name)}
	}
	return [2]string{sc, name}
}

func (r relations) key(sc, name string) string { return r.keys[r.name(sc, name)] }

// lookup resolves a name from a digest; a folded one (printed upper-cased)
// matches only when a single relation has that name in any case.
func (r relations) lookup(sc, name string, fold bool) string {
	if !fold {
		return r.key(sc, name)
	}
	if ks := r.folded[[2]string{strings.ToLower(sc), strings.ToLower(name)}]; len(ks) == 1 {
		return ks[0]
	}
	return ""
}

// unreadable records a performance_schema error in info. Only an error that
// is not the server's (a lost connection, a cancelled context) is returned.
func unreadable(ctx context.Context, tx *sql.Tx, info *workload.Statements, err error) error {
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		return fmt.Errorf("workload: %w", err)
	}
	info.Status, info.Message = workload.StatusUnavailable, me.Message
	switch me.Number {
	case 1142, 1143, 1044: // table, column or database access denied
		who := "the user"
		var cur string
		if tx.QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&cur) == nil {
			if at := strings.LastIndexByte(cur, '@'); at >= 0 {
				q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
				who = q(cur[:at]) + "@" + q(cur[at+1:])
			}
		}
		info.Message = "This user may not read performance_schema: an admin runs GRANT SELECT ON performance_schema.* TO " + who + "."
	}
	return nil
}

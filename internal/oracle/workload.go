package oracle

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

const (
	maxStatements = 2000
	// maxPlanLines bounds the plan lines read for scans.
	maxPlanLines = 50000
	maxSynonyms  = 10000
)

// cursors selects the shared pool's cursors of this container parsed in a
// user schema; Oracle's own schemas (SYS, SYSTEM, DBSNMP...) parse the
// dictionary's recursive SQL and dbscope's reads when it connects as SYSTEM.
// Left out as not application traffic: ALTER SESSION, COMMIT, ROLLBACK,
// SAVEPOINT, SET TRANSACTION and EXPLAIN PLAN (dbscope's query runner sends
// the last two), and the optimizer's dynamic sampling and DBMS_STATS
// queries, which run under the user's schema but are tagged: by a leading
// comment, or (seen on 23.26) by dbms_stats among their hints. sql_text is
// the first 1,000 characters; names past them are not seen.
const cursors = `FROM v$sql
	WHERE con_id = to_number(sys_context('USERENV', 'CON_ID'))
		AND parsing_schema_name IN ` + userOwners + `
		AND command_type NOT IN (42, 44, 45, 46, 48, 50)
		AND sql_text NOT LIKE '/* OPT_DYN_SAMP */%' AND sql_text NOT LIKE '/* DS_SVC */%' AND sql_text NOT LIKE '/* SQL Analyze(%'
		AND lower(sql_text) NOT LIKE '%/*+%dbms_stats%*/%'`

// topCursors are the statements read: one row per sql_id and parsing
// schema (the same text parsed in two schemas can name different tables),
// most elapsed time first.
var topCursors = `SELECT sql_id, parsing_schema_name, command_type, signature, sql_text, executions, elapsed_time,
		sum(executions) OVER () AS all_executions, sum(elapsed_time) OVER () AS all_elapsed
	FROM (SELECT sql_id, parsing_schema_name, max(command_type) AS command_type,
			max(to_char(force_matching_signature)) AS signature, max(sql_text) AS sql_text,
			sum(executions) AS executions, sum(elapsed_time) AS elapsed_time
		` + cursors + `
		GROUP BY sql_id, parsing_schema_name)
	ORDER BY elapsed_time DESC, sql_id
	FETCH FIRST ` + strconv.Itoa(maxStatements) + ` ROWS ONLY`

// Workload reads the cursors of the shared pool (V$SQL) and their plans
// (V$SQL_PLAN) for statements and scans, and ALL_TAB_MODIFICATIONS for rows
// written.
//
// AWR and ASH (DBA_HIST_*, V$ACTIVE_SESSION_HISTORY) are never read: the
// Database Licensing Information User Manual lists them under the
// Diagnostics Pack, a paid option, and querying them counts as using it.
// V$SQL, V$SQL_PLAN and ALL_TAB_MODIFICATIONS are not on that list. dbscope
// never flushes, purges or changes anything; it does not call
// DBMS_STATS.FLUSH_DATABASE_MONITORING_INFO, so rows written may lag (the
// Database Reference says ALL_TAB_MODIFICATIONS is not filled at once).
//
// V$ views need SELECT_CATALOG_ROLE (or a grant on SYS.V_$SQL and
// SYS.V_$SQL_PLAN); without it the snapshot says what to grant and still
// has rows written. Only a failure to reach the server is an error.
func Workload(ctx context.Context, db *sql.DB, m *schema.Model) (*workload.Snapshot, error) {
	var user string
	if err := db.QueryRowContext(ctx, `SELECT USER FROM dual`).Scan(&user); err != nil {
		return nil, fmt.Errorf("workload: %w", err)
	}
	snap := &workload.Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*workload.Table{}}
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Stub || r.Kind == schema.KindView {
			continue
		}
		t := &workload.Table{}
		if r.RowEstimate != nil {
			t.LiveRows = *r.RowEstimate
		}
		snap.Tables[r.QName().String()] = t
	}
	var notes []string
	if view, err := rowsWritten(ctx, db, m, snap); err != nil {
		notes = append(notes, "Rows written could not be read: "+err.Error()+".")
	} else {
		notes = append(notes, "Rows written come from "+view+": they count since the table's statistics were last gathered, "+
			"and Oracle documents that they can lag until its monitoring data is flushed.")
		if view == "ALL_TAB_MODIFICATIONS" {
			notes = append(notes, "Without SELECT_CATALOG_ROLE, writes to partitions of other users' tables are not seen.")
		}
	}
	ix := newRelIndex(m)
	if err := ix.readSynonyms(ctx, db, m); err != nil && !denied(err) {
		notes = append(notes, "Synonyms could not be read, so names reached through them are not tied to tables.")
	}
	kept, err := statements(ctx, db, ix, snap)
	info := &snap.Statements
	if err != nil {
		info.Status = workload.StatusUnavailable
		if denied(err) {
			info.Message = fmt.Sprintf("This user may not read Oracle's shared pool (V$SQL). An admin runs: GRANT SELECT_CATALOG_ROLE TO %s; "+
				"it takes effect on new sessions.", grantee(user))
		} else {
			info.Message = "V$SQL could not be read: " + err.Error()
		}
		info.Message += " " + strings.Join(notes, " ")
		return snap, nil
	}
	if err := scans(ctx, db, ix, kept, snap); err != nil {
		if denied(err) {
			notes = append(notes, fmt.Sprintf("Scans need V$SQL_PLAN, which this user may not read: as SYS, an admin runs "+
				"GRANT READ ON SYS.V_$SQL_PLAN TO %s, or grants SELECT_CATALOG_ROLE.", grantee(user)))
		} else {
			notes = append(notes, "Scans could not be read from V$SQL_PLAN: "+err.Error()+".")
		}
	}
	if age, ok := poolAge(ctx, db); ok {
		since := snap.GeneratedAt.Add(-age).Truncate(time.Second)
		info.Since = &since
	}
	info.Message = strings.Join(append([]string{"Only statements still in the shared pool count: a cursor's counts start when it is loaded and are lost when it ages out. " +
		"Scans count executions of those statements whose plan reads the table in full or through an index, not each scan."}, notes...), " ")
	return snap, nil
}

// rowsWritten reads DBA_TAB_MODIFICATIONS, or ALL_TAB_MODIFICATIONS when
// the user may not, and returns the view read. On 23.26 ALL_ shows a reader
// the table-level rows of tables it may read but not their partitions' rows.
// A partitioned table can have a table-level row, partition rows or both,
// as of different times (seen after statistics were gathered), so the
// larger count is kept.
func rowsWritten(ctx context.Context, db *sql.DB, m *schema.Model, snap *workload.Snapshot) (string, error) {
	if len(m.Schemas) == 0 {
		return "DBA_TAB_MODIFICATIONS", nil
	}
	in, args := inList(m.Schemas)
	for _, view := range []string{"DBA_TAB_MODIFICATIONS", "ALL_TAB_MODIFICATIONS"} {
		counts := map[string]int64{}
		err := each(ctx, db, `SELECT table_owner, table_name, greatest(
				coalesce(sum(CASE WHEN partition_name IS NULL THEN inserts + updates + deletes END), 0),
				coalesce(sum(CASE WHEN partition_name IS NOT NULL AND subpartition_name IS NULL THEN inserts + updates + deletes END), 0))
			FROM `+strings.ToLower(view)+` WHERE table_owner `+in+` GROUP BY table_owner, table_name`, args, func(r *sql.Rows) error {
			var owner, name string
			var n int64
			if err := r.Scan(&owner, &name, &n); err != nil {
				return err
			}
			counts[schema.QualifiedName{Schema: owner, Name: name}.String()] = n
			return nil
		})
		if err != nil && denied(err) && view != "ALL_TAB_MODIFICATIONS" {
			continue
		}
		if err != nil {
			return "", err
		}
		for k, n := range counts {
			if t := snap.Tables[k]; t != nil {
				t.RowsWritten = n
			}
		}
		return view, nil
	}
	return "", nil
}

// cursorKey is one row of topCursors.
type cursorKey struct{ sqlID, schema string }

// statements reads topCursors and attributes them by text. Statements with
// the same text up to literals (one FORCE_MATCHING_SIGNATURE) are one query.
// It returns the data statements tied to a table, whose plans give scans.
func statements(ctx context.Context, db *sql.DB, ix relIndex, snap *workload.Snapshot) (map[cursorKey]bool, error) {
	info := &snap.Statements
	type stmt struct {
		id, text string
		keys     []string
		calls    int64
		us       int64
	}
	groups := map[string]*stmt{}
	var order []string
	kept := map[cursorKey]bool{}
	var allCalls, allUs, readCalls int64
	var ownCalls, ownUs int64
	err := each(ctx, db, topCursors, nil, func(r *sql.Rows) error {
		var k cursorKey
		var command int
		var signature, text sql.NullString
		var calls, us int64
		if err := r.Scan(&k.sqlID, &k.schema, &command, &signature, &text, &calls, &us, &allCalls, &allUs); err != nil {
			return err
		}
		info.Read++
		readCalls += calls
		keys, own := []string(nil), false
		if dataCommands[command] {
			keys, own = ix.tablesIn(text.String, k.schema)
		}
		switch {
		case own:
			ownCalls += calls
			ownUs += us
			return nil
		case len(keys) == 0:
			info.UnattributedCalls += calls
			return nil
		}
		kept[k] = true
		g := k.schema + "/" + k.sqlID
		if signature.String != "" && signature.String != "0" {
			g = k.schema + "/" + signature.String
		}
		if s := groups[g]; s != nil {
			s.calls += calls
			s.us += us
			return nil
		}
		groups[g] = &stmt{id: k.sqlID, text: text.String, keys: keys, calls: calls, us: us}
		order = append(order, g)
		return nil
	})
	if err != nil {
		return nil, err
	}
	info.Status = workload.StatusOK
	info.UnreadCalls = max(0, allCalls-readCalls)
	info.TotalCalls, info.TotalTimeMs = allCalls-ownCalls, float64(allUs-ownUs)/1000
	for _, g := range order {
		s := groups[g]
		ms := float64(s.us) / 1000 // elapsed_time is in microseconds
		q := workload.Query{QueryID: s.id, Text: s.text, Calls: s.calls, TotalMs: ms}
		if s.calls > 0 {
			q.MeanMs = ms / float64(s.calls)
		}
		for _, k := range s.keys {
			t := snap.Tables[k]
			if t == nil {
				t = &workload.Table{} // a view
				snap.Tables[k] = t
			}
			t.QueryCalls += s.calls
			t.QueryTimeMs += ms
			workload.AddTopQuery(t, q)
		}
	}
	return kept, nil
}

// dataCommands are V$SQL command types that read or change rows: INSERT,
// SELECT, UPDATE, DELETE and UPSERT (MERGE). PL/SQL blocks and CALL are
// not tied to a table; the SQL they run is a cursor of its own.
var dataCommands = map[int]bool{2: true, 3: true, 6: true, 7: true, 189: true}

// scans credits each table of m that the kept statements' plans read in
// full (SeqScans) or through an index (IdxScans) with the cursor's
// executions: once per execution, however many plan lines or loops read it.
// Lines an adaptive plan resolved away are not counted, and neither is a
// plan still unresolved.
func scans(ctx context.Context, db *sql.DB, ix relIndex, kept map[cursorKey]bool, snap *workload.Snapshot) error {
	if len(kept) == 0 {
		return nil
	}
	type child struct {
		cursorKey
		number int
	}
	type line struct {
		id   int
		full bool
		key  string
	}
	lines := map[child][]line{}
	execs := map[child]int64{}
	adaptive := map[child]bool{}
	err := each(ctx, db, `WITH top AS (`+topCursors+`)
		SELECT s.sql_id, s.parsing_schema_name, s.child_number, s.executions, coalesce(s.is_resolved_adaptive_plan, '-'),
			p.id, p.operation, coalesce(p.options, '-'), p.object_owner, p.object_name
		FROM v$sql s JOIN v$sql_plan p ON p.child_address = s.child_address AND p.sql_id = s.sql_id
		WHERE (s.sql_id, s.parsing_schema_name) IN (SELECT sql_id, parsing_schema_name FROM top)
			AND s.con_id = to_number(sys_context('USERENV', 'CON_ID'))
			AND p.operation IN ('TABLE ACCESS', 'INDEX', 'MAT_VIEW ACCESS') AND p.object_name IS NOT NULL
		FETCH FIRST `+strconv.Itoa(maxPlanLines)+` ROWS ONLY`, nil, func(r *sql.Rows) error {
		var c child
		var calls int64
		var resolved, op, options string
		var id int
		var owner, name sql.NullString
		if err := r.Scan(&c.sqlID, &c.schema, &c.number, &calls, &resolved, &id, &op, &options, &owner, &name); err != nil {
			return err
		}
		if !kept[c.cursorKey] || resolved == "N" {
			return nil
		}
		q := schema.QualifiedName{Schema: owner.String, Name: name.String}
		l := line{id: id}
		switch k := ix.rels[q]; {
		case op == "INDEX":
			l.key = ix.indexes[q]
		case k == "" || snap.Tables[k] == nil:
		case strings.Contains(options, "INDEX ROWID"):
			l.key = k
		case strings.HasSuffix(options, "FULL"):
			l.key, l.full = k, true
		}
		if l.key == "" {
			return nil
		}
		lines[c] = append(lines[c], l)
		execs[c] = calls
		adaptive[c] = resolved == "Y"
		return nil
	})
	if err != nil {
		return err
	}
	// An adaptive plan lists every line it could run; other_xml's display
	// map marks those it resolved away with skp="1".
	skipped := map[child]map[int]bool{}
	anyAdaptive := false
	for _, a := range adaptive {
		anyAdaptive = anyAdaptive || a
	}
	if anyAdaptive {
		err := each(ctx, db, `WITH top AS (`+topCursors+`)
			SELECT s.sql_id, s.parsing_schema_name, s.child_number, p.other_xml
			FROM v$sql s JOIN v$sql_plan p ON p.child_address = s.child_address AND p.sql_id = s.sql_id
			WHERE (s.sql_id, s.parsing_schema_name) IN (SELECT sql_id, parsing_schema_name FROM top)
				AND s.con_id = to_number(sys_context('USERENV', 'CON_ID'))
				AND s.is_resolved_adaptive_plan = 'Y' AND p.other_xml IS NOT NULL
			FETCH FIRST `+strconv.Itoa(maxStatements)+` ROWS ONLY`, nil, func(r *sql.Rows) error {
			var c child
			var doc string
			if err := r.Scan(&c.sqlID, &c.schema, &c.number, &doc); err != nil {
				return err
			}
			if ids, ok := skippedLines(doc); ok && adaptive[c] {
				skipped[c] = ids
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for c, ls := range lines {
		ids, ok := skipped[c]
		if adaptive[c] && !ok {
			continue // no display map read: which lines ran is unknown
		}
		full, index := map[string]bool{}, map[string]bool{}
		for _, l := range ls {
			switch {
			case ids[l.id]:
			case l.full:
				full[l.key] = true
			default:
				index[l.key] = true
			}
		}
		for k := range full {
			snap.Tables[k].SeqScans += execs[c]
		}
		for k := range index {
			snap.Tables[k].IdxScans += execs[c]
		}
	}
	return nil
}

// skippedLines reads the ids of the plan lines an adaptive plan does not run.
func skippedLines(doc string) (map[int]bool, bool) {
	var x struct {
		Rows []struct {
			Op   int `xml:"op,attr"`
			Skip int `xml:"skp,attr"`
		} `xml:"display_map>row"`
	}
	if err := xml.Unmarshal([]byte(doc), &x); err != nil || len(x.Rows) == 0 {
		return nil, false
	}
	ids := map[int]bool{}
	for _, r := range x.Rows {
		if r.Skip == 1 {
			ids[r.Op] = true
		}
	}
	return ids, true
}

// poolAge is how long the shared pool has held this container's cursors:
// since the instance started or the pluggable database last opened.
func poolAge(ctx context.Context, db *sql.DB) (time.Duration, bool) {
	var secs float64
	err := db.QueryRowContext(ctx, `SELECT least(
			(SELECT (SYSDATE - startup_time) * 86400 FROM v$instance),
			coalesce((SELECT extract(day FROM d) * 86400 + extract(hour FROM d) * 3600 + extract(minute FROM d) * 60 + extract(second FROM d)
				FROM (SELECT systimestamp - open_time AS d FROM v$containers WHERE con_id = to_number(sys_context('USERENV', 'CON_ID')))), 1e12))
		FROM dual`).Scan(&secs)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// denied reports Oracle's errors for a view the user may not read.
func denied(err error) bool {
	s := err.Error()
	return strings.Contains(s, "ORA-00942") || strings.Contains(s, "ORA-01031") || strings.Contains(s, "ORA-41900")
}

var plainName = regexp.MustCompile(`^[A-Z][A-Z0-9_$#]*$`)

// grantee spells a user name for a GRANT, quoted unless it needs no quotes.
func grantee(user string) string {
	if plainName.MatchString(user) {
		return user
	}
	return `"` + strings.ReplaceAll(user, `"`, `""`) + `"`
}

func inList(values []string) (string, []any) {
	marks := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		marks[i], args[i] = ":"+strconv.Itoa(i+1), v
	}
	return "IN (" + strings.Join(marks, ", ") + ")", args
}

// relIndex ties statement text to the model's relations. V$SQL has no
// column listing a statement's tables (V$SQL_PLAN has only those a plan
// reads, not those a statement writes), so names are read from the text
// where a table goes: after FROM, JOIN, INTO, UPDATE, DELETE, USING or
// APPLY, and after commas in such a list. An unquoted name is upper-cased,
// as Oracle stores it. A bare name resolves as Oracle would for the
// statement's parsing schema (the session's CURRENT_SCHEMA): a table of
// that schema, else its private synonym, else a public synonym, as far as
// the model and ALL_SYNONYMS show; a table of that schema outside the model
// is not seen, so a synonym of the same name may be credited instead.
type relIndex struct {
	rels     map[schema.QualifiedName]string
	synonyms map[schema.QualifiedName]string // owner (or PUBLIC) and synonym -> relation key
	// indexes maps an index, taken to be in its table's schema, to the table.
	indexes map[schema.QualifiedName]string
}

func newRelIndex(m *schema.Model) relIndex {
	ix := relIndex{rels: map[schema.QualifiedName]string{}, synonyms: map[schema.QualifiedName]string{}, indexes: map[schema.QualifiedName]string{}}
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Stub {
			continue
		}
		k := r.QName().String()
		ix.rels[r.QName()] = k
		for _, x := range r.Indexes {
			ix.indexes[schema.QualifiedName{Schema: r.Schema, Name: x.Name}] = k
		}
	}
	return ix
}

// readSynonyms reads the local synonyms for the model's relations.
func (ix relIndex) readSynonyms(ctx context.Context, db *sql.DB, m *schema.Model) error {
	if len(m.Schemas) == 0 {
		return nil
	}
	in, args := inList(m.Schemas)
	return each(ctx, db, `SELECT owner, synonym_name, table_owner, table_name FROM all_synonyms
		WHERE table_owner `+in+` AND db_link IS NULL FETCH FIRST `+strconv.Itoa(maxSynonyms)+` ROWS ONLY`, args,
		func(r *sql.Rows) error {
			var owner, name, tOwner, tName string
			if err := r.Scan(&owner, &name, &tOwner, &tName); err != nil {
				return err
			}
			if k := ix.rels[schema.QualifiedName{Schema: tOwner, Name: tName}]; k != "" {
				ix.synonyms[schema.QualifiedName{Schema: owner, Name: name}] = k
			}
			return nil
		})
}

// catalogNames are dictionary views and tables that dbscope's own reads
// (and tools') name bare, besides the ALL_, DBA_, USER_, CDB_ and V$ views.
var catalogNames = map[string]bool{"DUAL": true, "PLAN_TABLE": true, "PRODUCT_COMPONENT_VERSION": true, "SESSION_PRIVS": true,
	"SESSION_ROLES": true, "DICTIONARY": true, "DICT": true, "GLOBAL_NAME": true, "NLS_SESSION_PARAMETERS": true,
	"NLS_DATABASE_PARAMETERS": true, "NLS_INSTANCE_PARAMETERS": true}

func catalogName(parts []string) bool {
	if len(parts) == 2 && (parts[0] == "SYS" || parts[0] == "PUBLIC") {
		return true
	}
	if len(parts) != 1 {
		return false
	}
	n := parts[0]
	for _, p := range []string{"ALL_", "DBA_", "USER_", "CDB_", "V$", "GV$", "V_$", "GV_$"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return catalogNames[n]
}

// clauseWords end a FROM or INTO list, or follow a table name without being
// its alias.
var clauseWords = map[string]bool{"where": true, "group": true, "order": true, "having": true, "connect": true, "start": true,
	"union": true, "minus": true, "intersect": true, "except": true, "fetch": true, "offset": true, "for": true, "model": true,
	"window": true, "on": true, "using": true, "join": true, "inner": true, "left": true, "right": true, "full": true,
	"outer": true, "cross": true, "natural": true, "partition": true, "subpartition": true, "sample": true, "as": true,
	"pivot": true, "unpivot": true, "set": true, "values": true, "select": true, "with": true, "returning": true,
	"return": true, "log": true, "when": true, "then": true, "else": true, "end": true, "into": true, "from": true,
	"update": true, "delete": true, "apply": true, "of": true, "nowait": true, "skip": true, "wait": true, "default": true}

// tablesIn returns the keys of the relations a data statement's text
// names, each once. own reports a statement that reads only the dictionary
// (dbscope's own reads among them).
func (ix relIndex) tablesIn(text, parsingSchema string) (keys []string, own bool) {
	toks := Tokens(text)
	ctes := map[string]bool{}
	for i := 1; i+1 < len(toks); i++ {
		// WITH name [(columns)] AS ( ... ), name AS ( ... )
		if prev := toks[i-1]; (prev.Word == "with" || prev.Text == ",") && toks[i].Word != "" && !strings.Contains(toks[i].Word, ".") {
			j := i + 1
			if toks[j].Text == "(" {
				for j < len(toks) && toks[j].Text != ")" {
					j++
				}
				j++
			}
			if j+1 < len(toks) && toks[j].Word == "as" && toks[j+1].Text == "(" {
				ctes[strings.ToUpper(toks[i].Text)] = true
			}
		}
	}
	catalog := false
	seen := map[string]bool{}
	// Inside parentheses, FROM names a table only in a subquery, not in
	// EXTRACT(DAY FROM d) or TRIM(x FROM y).
	type frame struct{ inList, query bool }
	var stack []frame
	query, inList, expect, afterName := true, false, false, false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.Text {
		case "(":
			stack = append(stack, frame{inList, query})
			query = i+1 < len(toks) && (toks[i+1].Word == "select" || toks[i+1].Word == "with")
			inList, expect, afterName = false, false, false
			continue
		case ")":
			if n := len(stack); n > 0 {
				inList, query = stack[n-1].inList, stack[n-1].query
				stack = stack[:n-1]
			}
			expect, afterName = false, inList
			continue
		case ",":
			expect, afterName = inList, false
			continue
		}
		quoted := strings.HasPrefix(t.Text, `"`)
		if !quoted {
			w := t.Word
			switch {
			case w == "from" || w == "join" || w == "into" || w == "using" || w == "apply":
				inList, expect, afterName = query, query, false
				continue
			case w == "update" || w == "delete":
				// FOR UPDATE and MERGE's THEN UPDATE SET / DELETE WHERE name no table.
				prev := ""
				if i > 0 {
					prev = toks[i-1].Word
				}
				inList = query && prev != "for" && prev != "then"
				expect, afterName = inList, false
				continue
			case w == "lateral" || w == "only":
				continue
			case w == "":
				expect, afterName = false, false // a literal, bind or operator
				continue
			case afterName && !clauseWords[w]:
				afterName = false // an alias
				continue
			case !expect || clauseWords[w]:
				inList, expect, afterName = false, false, false
				continue
			}
		} else if !expect {
			afterName = false
			continue
		}
		parts, j := nameAt(toks, i)
		i = j - 1
		expect, afterName = false, true
		if j < len(toks) && toks[j].Text == "@" {
			i++ // a database link's table, followed by the link name
			continue
		}
		if len(parts) == 1 && !quoted && ctes[parts[0]] {
			continue
		}
		if k := ix.resolve(parts, parsingSchema); k != "" {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		} else if catalogName(parts) {
			catalog = true
		}
	}
	return keys, catalog && len(keys) == 0
}

// nameAt reads a possibly dotted, possibly quoted name starting at toks[i]
// and returns its parts as Oracle stores them and the index after it.
func nameAt(toks []Token, i int) ([]string, int) {
	var parts []string
	part := func(t Token) bool {
		switch {
		case t.Word != "":
			for _, p := range strings.Split(t.Text, ".") {
				parts = append(parts, strings.ToUpper(p))
			}
		case len(t.Text) >= 2 && t.Text[0] == '"' && t.Text[len(t.Text)-1] == '"':
			parts = append(parts, strings.ReplaceAll(t.Text[1:len(t.Text)-1], `""`, `"`))
		default:
			return false
		}
		return true
	}
	part(toks[i])
	j := i + 1
	for j+1 < len(toks) && toks[j].Text == "." && part(toks[j+1]) {
		j += 2
	}
	return parts, j
}

func (ix relIndex) resolve(parts []string, parsingSchema string) string {
	switch len(parts) {
	case 1:
		if k := ix.rels[schema.QualifiedName{Schema: parsingSchema, Name: parts[0]}]; k != "" {
			return k
		}
		if k := ix.synonyms[schema.QualifiedName{Schema: parsingSchema, Name: parts[0]}]; k != "" {
			return k
		}
		return ix.synonyms[schema.QualifiedName{Schema: "PUBLIC", Name: parts[0]}]
	case 2:
		if k := ix.rels[schema.QualifiedName{Schema: parts[0], Name: parts[1]}]; k != "" {
			return k
		}
		return ix.synonyms[schema.QualifiedName{Schema: parts[0], Name: parts[1]}]
	}
	return ""
}

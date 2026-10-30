package mssql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// ErrAzureWorkload means the profile reaches Azure SQL Database, whose
// permissions for these views differ and where Workload was not tested.
var ErrAzureWorkload = errors.New("workload is not available for Azure SQL Database yet")

const (
	maxStatements = 2000
	// maxTextChars bounds each statement text read; names past it are not seen.
	maxTextChars = 8000
	maxShownText = 1000
)

// Workload reads the table counters of sys.dm_db_index_usage_stats and the
// cached statements of sys.dm_exec_query_stats for the profile's database,
// and attributes each statement's calls and time to the tables its text
// names. Both views count since the server started and need VIEW SERVER
// PERFORMANCE STATE (VIEW SERVER STATE before 2022); without it the
// snapshot says what to grant. dbscope never clears the plan cache or
// changes a setting.
func Workload(ctx context.Context, db *sql.DB, m *schema.Model) (*workload.Snapshot, error) {
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	if srv.Azure {
		return nil, ErrAzureWorkload
	}
	snap := &workload.Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*workload.Table{}}
	var seesAll int
	var login, dbName string
	if err := db.QueryRowContext(ctx, `SELECT coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE'), 0)
		| coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE'), 0), QUOTENAME(SUSER_SNAME()), DB_NAME()`).Scan(&seesAll, &login, &dbName); err != nil {
		return nil, fmt.Errorf("workload: %w", err)
	}
	if seesAll == 0 {
		perm := "VIEW SERVER PERFORMANCE STATE"
		if srv.Num < 160000 {
			perm = "VIEW SERVER STATE"
		}
		snap.Statements.Status = workload.StatusUnavailable
		snap.Statements.Message = fmt.Sprintf("This login may not read SQL Server's index usage statistics or plan cache. An admin runs, in master: GRANT %s TO %s;", perm, login)
		return snap, nil
	}
	var upSecs int64
	if err := db.QueryRowContext(ctx, `SELECT DATEDIFF_BIG(SECOND, sqlserver_start_time, SYSDATETIME()) FROM sys.dm_os_sys_info`).Scan(&upSecs); err == nil {
		// Index usage counters also start over when the database is taken offline or detached.
		since := snap.GeneratedAt.Add(-time.Duration(upSecs) * time.Second).Truncate(time.Second)
		snap.TableStatsReset, snap.Statements.Since = &since, &since
	}
	if err := tableCounters(ctx, db, m, snap); err != nil {
		return nil, fmt.Errorf("workload: index usage: %w", err)
	}
	statements(ctx, db, newRelIndex(m, dbName), snap)
	return snap, nil
}

// tableCounters fills each table of m from its indexes' usage. Scans of the
// heap or clustered index read the table and are SeqScans; seeks, lookups and
// scans of a nonclustered index are IdxScans. RowsWritten is the heap's or
// clustered index's user_updates: it counts write statements run on the
// table (one for a two-row INSERT), not rows.
func tableCounters(ctx context.Context, db *sql.DB, m *schema.Model, snap *workload.Snapshot) error {
	if len(m.Schemas) == 0 {
		return nil
	}
	marks := make([]string, len(m.Schemas))
	args := make([]any, len(m.Schemas))
	for i, s := range m.Schemas {
		marks[i], args[i] = "@p"+strconv.Itoa(i+1), s
	}
	return each(ctx, db, `SELECT s.name, o.name,
			coalesce(sum(CASE WHEN u.index_id IN (0, 1) THEN u.user_scans END), 0),
			coalesce(sum(u.user_seeks + u.user_lookups + CASE WHEN u.index_id > 1 THEN u.user_scans ELSE 0 END), 0),
			coalesce(sum(CASE WHEN u.index_id IN (0, 1) THEN u.user_updates END), 0),
			coalesce((SELECT sum(p.rows) FROM sys.partitions p WHERE p.object_id = o.object_id AND p.index_id IN (0, 1)), 0)
		FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id
		LEFT JOIN sys.dm_db_index_usage_stats u ON u.database_id = DB_ID() AND u.object_id = o.object_id
		WHERE o.type = 'U' AND o.is_ms_shipped = 0 AND s.name IN (`+strings.Join(marks, ", ")+`)
		GROUP BY s.name, o.name, o.object_id`, args,
		func(r *sql.Rows) error {
			var sc, name string
			var t workload.Table
			if err := r.Scan(&sc, &name, &t.SeqScans, &t.IdxScans, &t.RowsWritten, &t.LiveRows); err != nil {
				return err
			}
			if q := (schema.QualifiedName{Schema: sc, Name: name}); m.Relation(q) != nil {
				snap.Tables[q.String()] = &t
			}
			return nil
		})
}

// statements reads the plan cache's statements of this database, most
// elapsed time first, and attributes them by text (see relIndex). A failure
// is recorded in snap.Statements: the table counters still stand.
func statements(ctx context.Context, db *sql.DB, ix relIndex, snap *workload.Snapshot) {
	info := &snap.Statements
	// Ad hoc and prepared statements have no dbid in dm_exec_sql_text; the
	// plan's dbid attribute is the database they ran in. System procedures
	// run from the resource database (dbid 32767).
	rows, err := db.QueryContext(ctx, `SELECT TOP (`+strconv.Itoa(maxStatements)+`) qs.query_hash, qs.execution_count, qs.total_elapsed_time,
			coalesce(LEFT(SUBSTRING(st.text, qs.statement_start_offset / 2 + 1, (CASE qs.statement_end_offset
				WHEN -1 THEN DATALENGTH(st.text) ELSE qs.statement_end_offset END - qs.statement_start_offset) / 2 + 1), `+strconv.Itoa(maxTextChars)+`), ''),
			sum(qs.execution_count) OVER (), sum(qs.total_elapsed_time) OVER ()
		FROM sys.dm_exec_query_stats qs
		CROSS APPLY sys.dm_exec_sql_text(qs.sql_handle) st
		CROSS APPLY (SELECT CAST(value AS int) AS dbid FROM sys.dm_exec_plan_attributes(qs.plan_handle) WHERE attribute = 'dbid') pa
		WHERE pa.dbid = DB_ID() AND (st.dbid IS NULL OR st.dbid = DB_ID()) AND coalesce(OBJECTPROPERTY(st.objectid, 'IsMSShipped'), 0) = 0
		ORDER BY qs.total_elapsed_time DESC`)
	if err != nil {
		info.Status, info.Message = workload.StatusUnavailable, err.Error()
		return
	}
	defer rows.Close()
	// Plans of one statement with different literals share a query_hash.
	type stmt struct {
		text  string
		calls int64
		us    int64
	}
	byHash := map[string]*stmt{}
	var order []string
	var allCalls, allUs, readCalls int64
	for rows.Next() {
		var hash []byte
		var s stmt
		if err := rows.Scan(&hash, &s.calls, &s.us, &s.text, &allCalls, &allUs); err != nil {
			info.Status, info.Message = workload.StatusUnavailable, err.Error()
			return
		}
		info.Read++
		readCalls += s.calls
		h := hex.EncodeToString(hash)
		if g := byHash[h]; g != nil {
			g.calls += s.calls
			g.us += s.us
			continue
		}
		byHash[h] = &s
		order = append(order, h)
	}
	if err := rows.Err(); err != nil {
		info.Status, info.Message = workload.StatusUnavailable, err.Error()
		return
	}
	info.Status = workload.StatusOK
	info.Message = "Only statements whose plans are in the plan cache count: a plan's counts start when it is cached and are lost when it is evicted or recompiled."
	info.UnreadCalls = max(0, allCalls-readCalls)
	info.TotalCalls, info.TotalTimeMs = allCalls, float64(allUs)/1000
	for _, h := range order {
		s := byHash[h]
		ms := float64(s.us) / 1000
		keys, own := ix.tablesIn(s.text)
		if own {
			info.TotalCalls -= s.calls
			info.TotalTimeMs -= ms
			continue
		}
		if len(keys) == 0 {
			info.UnattributedCalls += s.calls
			continue
		}
		q := workload.Query{QueryID: h, Text: s.text, Calls: s.calls, TotalMs: ms}
		if r := []rune(q.Text); len(r) > maxShownText {
			q.Text = string(r[:maxShownText]) + "…"
		}
		if s.calls > 0 {
			q.MeanMs = ms / float64(s.calls)
		}
		for _, k := range keys {
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
}

// relIndex ties statement text to the model's relations. The plan cache has
// no column listing a statement's tables (only its plan XML, too heavy to
// read for every statement), so names are matched by token: schema.table,
// db.schema.table for this database, or a bare table name when exactly one
// relation in the model has it. Names compare case-insensitively, as under
// SQL Server's default collations.
type relIndex struct {
	db        string
	qualified map[string]string
	bare      map[string][]string
}

func newRelIndex(m *schema.Model, database string) relIndex {
	ix := relIndex{db: strings.ToLower(database), qualified: map[string]string{}, bare: map[string][]string{}}
	for _, r := range m.Relations {
		k := r.QName().String()
		ix.qualified[strings.ToLower(r.Schema)+"."+strings.ToLower(r.Name)] = k
		n := strings.ToLower(r.Name)
		ix.bare[n] = append(ix.bare[n], k)
	}
	return ix
}

// catalogFuncs are the system functions dbscope's own probes call.
var catalogFuncs = map[string]bool{"serverproperty": true, "databasepropertyex": true, "has_perms_by_name": true,
	"fn_my_permissions": true, "is_srvrolemember": true}

// reserved are T-SQL's reserved keywords: unquoted, they are never a table
// name, so a table called [order] is not credited for ORDER BY.
var reserved = func() map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.Fields(`add all alter and any as asc authorization backup begin between break browse bulk by
		cascade case check checkpoint close clustered coalesce collate column commit compute constraint contains containstable
		continue convert create cross current current_date current_time current_timestamp current_user cursor database dbcc
		deallocate declare default delete deny desc disk distinct distributed double drop dump else end errlvl escape except
		exec execute exists exit external fetch file fillfactor for foreign freetext freetexttable from full function goto
		grant group having holdlock identity identity_insert identitycol if in index inner insert intersect into is join key
		kill left like lineno load merge national nocheck nonclustered not null nullif of off offsets on open opendatasource
		openquery openrowset openxml option or order outer over percent pivot plan precision primary print proc procedure
		public raiserror read readtext reconfigure references replication restore restrict return revert revoke right
		rollback rowcount rowguidcol rule save schema securityaudit select semantickeyphrasetable
		semanticsimilaritydetailstable semanticsimilaritytable session_user set setuser shutdown some statistics system_user
		table tablesample textsize then to top tran transaction trigger truncate try_convert tsequal union unique unpivot
		update updatetext use user values varying view waitfor when where while with within writetext`) {
		words[w] = true
	}
	return words
}()

// tablesIn returns the keys of the relations text names, each once. own
// reports dbscope's or the engine's own traffic, left out entirely: SET
// statements (dbscope's SHOWPLAN and ROWCOUNT among them) and statements that
// read the catalog (sys.*, INFORMATION_SCHEMA) without naming a relation.
func (ix relIndex) tablesIn(text string) (keys []string, own bool) {
	toks := Tokens(text)
	if len(toks) > 0 && toks[0].Word == "set" {
		return nil, true
	}
	catalog := false
	seen := map[string]bool{}
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for i := 0; i < len(toks); {
		name, quoted, ok := identifier(toks[i])
		if !ok {
			i++
			continue
		}
		parts := []string{name}
		j := i + 1
		for j+1 < len(toks) && toks[j].Text == "." {
			if n, _, ok := identifier(toks[j+1]); ok {
				parts, j = append(parts, n), j+2
			} else if toks[j+1].Text == "." { // db..table
				parts, j = append(parts, ""), j+1
			} else {
				break
			}
		}
		i = j
		var next string
		if j < len(toks) {
			next = toks[j].Text
		}
		last := parts[len(parts)-1]
		switch {
		case len(parts) >= 2 && (parts[len(parts)-2] == "sys" || parts[len(parts)-2] == "information_schema"):
			catalog = true
			continue
		case next == "(":
			catalog = catalog || catalogFuncs[last]
			continue
		case strings.HasPrefix(next, "'"): // N'...'
			continue
		}
		bare := func(n string) {
			if ks := ix.bare[n]; len(ks) == 1 {
				add(ks[0])
			}
		}
		switch len(parts) {
		case 1:
			if quoted || !reserved[name] {
				bare(name)
			}
		case 2:
			add(ix.qualified[parts[0]+"."+parts[1]])
		case 3:
			if parts[0] != ix.db {
				break // another database's, or a column named with its table
			}
			if parts[1] == "" {
				bare(parts[2])
			} else {
				add(ix.qualified[parts[1]+"."+parts[2]])
			}
		}
	}
	return keys, catalog && len(keys) == 0
}

// identifier returns a token's name, lower-cased and unquoted; variables,
// temporary tables and numbers are not names.
func identifier(t Token) (name string, quoted, ok bool) {
	switch s := t.Text; {
	case t.Word != "":
		if c := t.Word[0]; c == '@' || c == '#' || c == '$' || (c >= '0' && c <= '9') {
			return "", false, false
		}
		return t.Word, false, true
	case len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']':
		return strings.ToLower(strings.ReplaceAll(s[1:len(s)-1], "]]", "]")), true, true
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		return strings.ToLower(strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)), true, true
	}
	return "", false, false
}

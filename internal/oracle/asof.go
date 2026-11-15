package oracle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	oranet "github.com/sijms/go-ora/v2/network"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

var dialect = timetravel.Dialect{
	Ident:       quotedName,
	DottedWords: true,
	NotTables:   map[string]bool{"dual": true},
	ReadsTable:  map[string]bool{"only": true, "containers": true},
	AfterName:   map[string]bool{"partition": true, "subpartition": true, "sample": true},
	Fold: func(p timetravel.Part) string {
		if p.Quoted {
			return p.Name
		}
		return strings.ToUpper(p.Name)
	},
}

func quotedName(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`), true
}

// AsOfRefs finds the base tables a SELECT reads (internal/timetravel).
func AsOfRefs(sqlText string) ([]timetravel.Ref, error) {
	toks := Tokens(sqlText)
	tt := make([]timetravel.Token, len(toks))
	for i, t := range toks {
		// Tokens reads q'...' quoting only when q starts the token, so the body of
		// nq'...' would be read as SQL and could hide a FROM.
		if strings.HasSuffix(t.Word, "nq") && i+1 < len(toks) && toks[i+1].Pos == t.Pos+len(t.Text) && strings.HasPrefix(toks[i+1].Text, "'") {
			return nil, timetravel.Refuse("Write the NQ'...' string as N'...' to read as of a past time")
		}
		tt[i] = timetravel.Token{Text: t.Text, Word: t.Word, Pos: t.Pos}
	}
	return timetravel.Refs(tt, dialect)
}

// AsOfClause is the flashback clause for an instant. AS OF TIMESTAMP drops
// the zone of a TIMESTAMP WITH TIME ZONE and reads the digits in the server's
// clock zone (measured, docs/TIME.md), so the UTC instant is moved into that
// zone first; the session time zone plays no part.
func AsOfClause(at time.Time) string {
	return "AS OF TIMESTAMP CAST(FROM_TZ(TIMESTAMP '" + at.UTC().Format("2006-01-02 15:04:05.000000") +
		"', 'UTC') AT TIME ZONE TO_CHAR(SYSTIMESTAMP, 'TZH:TZM') AS TIMESTAMP)"
}

// AsOf rewrites a SELECT to read every table and view as of at. Each must be
// one this user may read with flashback (FLASHBACK on it, FLASHBACK ANY
// TABLE, or ownership).
func AsOf(sqlText string, at time.Time, resolve func([]timetravel.Ref) ([]timetravel.Table, error)) (string, error) {
	refs, err := AsOfRefs(sqlText)
	if err != nil {
		return "", err
	}
	tables, err := resolve(refs)
	if err != nil {
		return "", err
	}
	return timetravel.Rewrite(sqlText, tables, AsOfClause(at), func(r *schema.Relation) string {
		if r.TimeTravel == nil || !r.TimeTravel.Available {
			return "needs the FLASHBACK privilege on it, or FLASHBACK ANY TABLE"
		}
		return ""
	})
}

// Resolver resolves references against the catalog as this user sees it.
func Resolver(ctx context.Context, db *sql.DB) func([]timetravel.Ref) ([]timetravel.Table, error) {
	return func(refs []timetravel.Ref) ([]timetravel.Table, error) { return resolve(ctx, db, refs) }
}

// resolve finds the table or view each reference names, as Oracle would:
// an unqualified name in the session's current schema. Synonyms, database
// links and names this user cannot see are left unresolved.
func resolve(ctx context.Context, db *sql.DB, refs []timetravel.Ref) ([]timetravel.Table, error) {
	tables := make([]timetravel.Table, len(refs))
	if len(refs) == 0 {
		return tables, nil
	}
	var current string
	if err := db.QueryRowContext(ctx, `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual`).Scan(&current); err != nil {
		return nil, err
	}
	names := make([]schema.QualifiedName, len(refs))
	var pairs []string
	var args []any
	seen := map[schema.QualifiedName]bool{}
	for i, r := range refs {
		tables[i].Ref = r
		parts := make([]string, len(r.Parts))
		for k, p := range r.Parts {
			parts[k] = p.Name
			if !p.Quoted {
				parts[k] = strings.ToUpper(p.Name)
			}
		}
		switch len(parts) {
		case 1:
			names[i] = schema.QualifiedName{Schema: current, Name: parts[0]}
		case 2:
			names[i] = schema.QualifiedName{Schema: parts[0], Name: parts[1]}
		default:
			tables[i].Why = "has more than two name parts"
			continue
		}
		if !seen[names[i]] {
			seen[names[i]] = true
			pairs = append(pairs, fmt.Sprintf("(:%d, :%d)", len(args)+1, len(args)+2))
			args = append(args, names[i].Schema, names[i].Name)
		}
	}
	if len(pairs) == 0 {
		return tables, nil
	}
	type object struct {
		kind string
		ddl  time.Time
	}
	found := map[schema.QualifiedName]object{}
	err := each(ctx, db, `SELECT owner, object_name, object_type,
			TO_CHAR(SYS_EXTRACT_UTC(FROM_TZ(CAST(last_ddl_time AS TIMESTAMP), TO_CHAR(SYSTIMESTAMP, 'TZH:TZM'))), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM all_objects
		WHERE subobject_name IS NULL AND object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW', 'SYNONYM')
			AND (owner, object_name) IN (`+strings.Join(pairs, ", ")+`)`, args,
		func(r *sql.Rows) error {
			var q schema.QualifiedName
			var kind, ddl string
			if err := r.Scan(&q.Schema, &q.Name, &kind, &ddl); err != nil {
				return err
			}
			o := object{kind: kind}
			o.ddl, _ = time.Parse(time.RFC3339, ddl)
			if prev, ok := found[q]; !ok || prev.kind == "MATERIALIZED VIEW" {
				found[q] = o // a materialized view is listed again as its container table
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	m := &schema.Model{}
	index := map[schema.QualifiedName]int{}
	owners := map[string]bool{}
	for q, o := range found {
		kind := schema.KindTable
		switch o.kind {
		case "SYNONYM":
			continue
		case "VIEW":
			kind = schema.KindView
		}
		index[q] = len(m.Relations)
		m.Relations = append(m.Relations, schema.Relation{Schema: q.Schema, Name: q.Name, Kind: kind})
		owners[q.Schema] = true
	}
	if len(m.Relations) > 0 {
		var marks []string
		var oargs []any
		for o := range owners {
			oargs = append(oargs, o)
			marks = append(marks, ":"+strconv.Itoa(len(oargs)))
		}
		if err := flashback(ctx, db, "IN ("+strings.Join(marks, ", ")+")", oargs, m); err != nil {
			return nil, err
		}
	}
	for i := range tables {
		if tables[i].Why != "" {
			continue
		}
		q := names[i]
		o, ok := found[q]
		switch {
		case !ok:
			tables[i].Why = "is not a table or view this user can see in " + q.Schema + " (public synonyms are not followed)"
		case o.kind == "SYNONYM":
			tables[i].Why = "is a synonym; name the table it points to"
		default:
			tables[i].Rel = &m.Relations[index[q]]
			tables[i].DDL = o.ddl
		}
	}
	return tables, nil
}

// defaultUndoRetention is Oracle's default undo_retention, assumed when this
// user cannot read V$PARAMETER or V$UNDOSTAT.
const defaultUndoRetention = 900 * time.Second

// ddlMargin keeps the oldest point clear of a table's last DDL. LAST_DDL_TIME
// plus 1 s still gave ORA-01466 (measured on 23.26).
const ddlMargin = 5 * time.Second

// Bounds says how far back each table can be read: undo retention back from
// now, but never before the table's last DDL (ORA-01466).
func Bounds(ctx context.Context, db *sql.DB, names []schema.QualifiedName) (*timetravel.Bounds, error) {
	tables, err := resolve(ctx, db, timetravel.ExactRefs(names))
	if err != nil {
		return nil, err
	}
	var nowText string
	if err := db.QueryRowContext(ctx, `SELECT TO_CHAR(SYS_EXTRACT_UTC(SYSTIMESTAMP), 'YYYY-MM-DD"T"HH24:MI:SS.FF6"Z"') FROM dual`).Scan(&nowText); err != nil {
		return nil, err
	}
	now, err := time.Parse(time.RFC3339Nano, nowText)
	if err != nil {
		return nil, err
	}
	b := &timetravel.Bounds{Now: now, Tables: make([]timetravel.TableBounds, len(tables))}
	retention, source, err := undoRetention(ctx, db)
	if err != nil {
		return nil, err
	}
	b.Note = "Undo retention is a target, not a promise: unless the undo tablespace has RETENTION GUARANTEE, " +
		"Oracle may overwrite older undo sooner, and the read then fails with ORA-01555."
	if source == timetravel.SourceAssumedDefault {
		b.Note += " This user cannot read V$PARAMETER or V$UNDOSTAT (that needs SELECT_CATALOG_ROLE), " +
			"so the default undo_retention of 900 seconds is assumed."
	}
	for i, t := range tables {
		tb := timetravel.TableBounds{Table: names[i].Schema + "." + names[i].Name, Kind: timetravel.Kind(t.Rel), Source: timetravel.SourceNone}
		switch {
		case t.Rel == nil:
			tb.Note = "This name " + t.Why
		case !t.Rel.TimeTravel.Available:
			tb.Note = t.Rel.TimeTravel.Note
		default:
			tb.Available, tb.Source = true, source
			oldest := now.Add(-retention)
			// LAST_DDL_TIME has whole seconds, and Oracle maps times to SCNs only every
			// 3 seconds: a time just after the DDL can still map to before it.
			if ddl := t.DDL.Add(ddlMargin); ddl.After(oldest) {
				oldest = ddl
				tb.Note = "The table's definition last changed at " + t.DDL.Format(time.RFC3339) + "; Oracle cannot read it as of an earlier time (ORA-01466)"
			}
			tb.Oldest = &oldest
		}
		b.Tables[i] = tb
	}
	return b, nil
}

// undoRetention is max(undo_retention, the latest TUNED_UNDORETENTION), or the
// default when this user may not read those views (ORA-00942).
func undoRetention(ctx context.Context, db *sql.DB) (time.Duration, string, error) {
	var param int64
	err := db.QueryRowContext(ctx, `SELECT TO_NUMBER(value) FROM v$parameter WHERE name = 'undo_retention'`).Scan(&param)
	if noView(err) {
		return defaultUndoRetention, timetravel.SourceAssumedDefault, nil
	}
	if err != nil {
		return 0, "", err
	}
	var tuned sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT max(tuned_undoretention) KEEP (DENSE_RANK LAST ORDER BY begin_time) FROM v$undostat`).Scan(&tuned)
	if err != nil && !noView(err) {
		return 0, "", err
	}
	if tuned.Valid && tuned.Int64 > param {
		return time.Duration(tuned.Int64) * time.Second, timetravel.SourceTunedUndoRetention, nil
	}
	return time.Duration(param) * time.Second, timetravel.SourceUndoRetention, nil
}

func noView(err error) bool {
	var oe *oranet.OracleError
	return errors.As(err, &oe) && oe.ErrCode == 942
}

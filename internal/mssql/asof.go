package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

var dialect = timetravel.Dialect{
	Ident: quotedName,
	ReadsTable: map[string]bool{"containstable": true, "freetexttable": true, "changetable": true,
		"semantickeyphrasetable": true, "semanticsimilaritytable": true, "semanticsimilaritydetailstable": true},
	AfterName: map[string]bool{"tablesample": true},
	Hints: map[string]bool{"nolock": true, "readuncommitted": true, "readcommitted": true, "readcommittedlock": true,
		"readpast": true, "repeatableread": true, "serializable": true, "snapshot": true, "tablock": true, "paglock": true,
		"rowlock": true, "nowait": true, "index": true, "forceseek": true, "forcescan": true, "noexpand": true,
		"keepidentity": true, "keepdefaults": true, "ignore_constraints": true, "ignore_triggers": true, "spatial_window_max_cells": true,
		"updlock": true, "xlock": true, "tablockx": true, "holdlock": true},
	EndsFrom: map[string]bool{"option": true},
}

func quotedName(s string) (string, bool) {
	switch {
	case len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']':
		return strings.ReplaceAll(s[1:len(s)-1], "]]", "]"), true
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`), true
	}
	return "", false
}

// AsOfRefs finds the base tables a SELECT reads (internal/timetravel).
func AsOfRefs(sqlText string) ([]timetravel.Ref, error) {
	toks := Tokens(sqlText)
	tt := make([]timetravel.Token, len(toks))
	for i, t := range toks {
		tt[i] = timetravel.Token{Text: t.Text, Word: t.Word, Pos: t.Pos}
	}
	return timetravel.Refs(tt, dialect)
}

// AsOfClause is the temporal clause for an instant. The literal is UTC with
// no offset: SQL Server drops an offset silently and reads the digits as UTC
// (measured, docs/TIME.md). datetime2 keeps 100 ns.
func AsOfClause(at time.Time) string {
	return "FOR SYSTEM_TIME AS OF '" + at.UTC().Format("2006-01-02T15:04:05.0000000") + "'"
}

// AsOf rewrites a SELECT to read every table as of at. Each must be a
// system-versioned temporal table whose history this user may read; a view
// is refused because it can mix temporal and current tables.
func AsOf(sqlText string, at time.Time, resolve func([]timetravel.Ref) ([]timetravel.Table, error)) (string, error) {
	refs, err := AsOfRefs(sqlText)
	if err != nil {
		return "", err
	}
	tables, err := resolve(refs)
	if err != nil {
		return "", err
	}
	return timetravel.Rewrite(sqlText, tables, AsOfClause(at), allowed)
}

func allowed(r *schema.Relation) string {
	tt := r.TimeTravel
	switch {
	case r.Kind == schema.KindView:
		return "is a view, which can mix temporal and current tables; query its tables instead"
	case tt != nil && tt.Kind == schema.TimeTravelHistory:
		return "is the history table of " + tt.HistoryOf + "; read " + tt.HistoryOf + " as of the time instead"
	case tt == nil || tt.Kind != schema.TimeTravelTemporal:
		return "is not a system-versioned temporal table"
	case !tt.Available:
		return "cannot be read as of a past time by this user: " + tt.Note
	}
	return ""
}

// Resolver resolves references against the catalog as this user sees it.
func Resolver(ctx context.Context, db *sql.DB) func([]timetravel.Ref) ([]timetravel.Table, error) {
	return func(refs []timetravel.Ref) ([]timetravel.Table, error) { return resolve(ctx, db, refs) }
}

// resolve finds the object each reference names with OBJECT_ID, which
// resolves an unqualified name the way the query will (the user's default
// schema, then dbo). Synonyms and names in other databases stay unresolved.
func resolve(ctx context.Context, db *sql.DB, refs []timetravel.Ref) ([]timetravel.Table, error) {
	tables := make([]timetravel.Table, len(refs))
	if len(refs) == 0 {
		return tables, nil
	}
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	if !srv.Azure && srv.Num < 140000 {
		return nil, timetravel.Refuse("Reading as of a past time needs SQL Server 2017 or later")
	}
	var rows []string
	var args []any
	for i, r := range refs {
		tables[i].Ref = r
		if len(r.Parts) > 2 {
			tables[i].Why = "is in another database or server, which cannot be checked"
			continue
		}
		quoted := make([]string, len(r.Parts))
		for k, p := range r.Parts {
			quoted[k] = "[" + strings.ReplaceAll(p.Name, "]", "]]") + "]"
		}
		args = append(args, strings.Join(quoted, "."))
		rows = append(rows, fmt.Sprintf("(%d, @p%d)", i, len(args)))
	}
	if len(rows) == 0 {
		return tables, nil
	}
	m := &schema.Model{}
	byName := map[schema.QualifiedName]int{}
	kinds := map[int]string{}
	at := map[int]schema.QualifiedName{}
	var schemas []string
	err = each(ctx, db, `SELECT r.n, s.name, o.name, o.type
		FROM (VALUES `+strings.Join(rows, ", ")+`) r(n, name)
		JOIN sys.objects o ON o.object_id = OBJECT_ID(r.name) JOIN sys.schemas s ON s.schema_id = o.schema_id`, args,
		func(r *sql.Rows) error {
			var n int
			var q schema.QualifiedName
			var typ string
			if err := r.Scan(&n, &q.Schema, &q.Name, &typ); err != nil {
				return err
			}
			typ = strings.TrimSpace(typ)
			kinds[n], at[n] = typ, q
			if _, ok := byName[q]; !ok && (typ == "U" || typ == "V") {
				kind := schema.KindTable
				if typ == "V" {
					kind = schema.KindView
				}
				byName[q] = len(m.Relations)
				m.Relations = append(m.Relations, schema.Relation{Schema: q.Schema, Name: q.Name, Kind: kind})
				schemas = append(schemas, q.Schema)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(schemas) > 0 {
		var marks []string
		var sargs []any
		for _, s := range schemas {
			sargs = append(sargs, s)
			marks = append(marks, fmt.Sprintf("@p%d", len(sargs)))
		}
		rel := func(s, t string) *schema.Relation {
			if i, ok := byName[schema.QualifiedName{Schema: s, Name: t}]; ok {
				return &m.Relations[i]
			}
			return nil
		}
		if err := temporalTables(ctx, db, "s.name IN ("+strings.Join(marks, ", ")+")", sargs, rel); err != nil {
			return nil, err
		}
	}
	for i := range tables {
		if tables[i].Why != "" {
			continue
		}
		switch typ := kinds[i]; typ {
		case "":
			tables[i].Why = "is not a table or view this user can see"
		case "SN":
			tables[i].Why = "is a synonym; name the table it points to"
		case "U", "V":
			tables[i].Rel = &m.Relations[byName[at[i]]]
		default:
			tables[i].Why = "is not a table or view"
		}
	}
	return tables, nil
}

// Bounds says how far back each table can be read: a finite history
// retention period back from now, or no limit for INFINITE.
func Bounds(ctx context.Context, db *sql.DB, names []schema.QualifiedName) (*timetravel.Bounds, error) {
	tables, err := resolve(ctx, db, timetravel.ExactRefs(names))
	if err != nil {
		return nil, err
	}
	b := &timetravel.Bounds{Tables: make([]timetravel.TableBounds, len(tables))}
	if err := db.QueryRowContext(ctx, "SELECT SYSUTCDATETIME()").Scan(&b.Now); err != nil {
		return nil, err
	}
	b.Now = b.Now.UTC()
	oldest := map[schema.QualifiedName]time.Time{}
	var marks []string
	var args []any
	for _, t := range tables {
		if t.Rel != nil && allowed(t.Rel) == "" {
			args = append(args, "["+strings.ReplaceAll(t.Rel.Schema, "]", "]]")+"].["+strings.ReplaceAll(t.Rel.Name, "]", "]]")+"]")
			marks = append(marks, fmt.Sprintf("OBJECT_ID(@p%d)", len(args)))
		}
	}
	if len(marks) > 0 {
		// DATEADD so a month back from the 31st is what the server's cleanup means by it.
		err := each(ctx, db, `SELECT s.name, t.name, CASE
				WHEN t.history_retention_period < 0 THEN NULL
				WHEN t.history_retention_period_unit_desc = 'DAY' THEN DATEADD(DAY, -t.history_retention_period, SYSUTCDATETIME())
				WHEN t.history_retention_period_unit_desc = 'WEEK' THEN DATEADD(WEEK, -t.history_retention_period, SYSUTCDATETIME())
				WHEN t.history_retention_period_unit_desc = 'MONTH' THEN DATEADD(MONTH, -t.history_retention_period, SYSUTCDATETIME())
				WHEN t.history_retention_period_unit_desc = 'YEAR' THEN DATEADD(YEAR, -t.history_retention_period, SYSUTCDATETIME())
			END
			FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id
			WHERE t.object_id IN (`+strings.Join(marks, ", ")+`)`, args,
			func(r *sql.Rows) error {
				var q schema.QualifiedName
				var at sql.NullTime
				if err := r.Scan(&q.Schema, &q.Name, &at); err != nil {
					return err
				}
				if at.Valid {
					oldest[q] = at.Time.UTC()
				}
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	for i, t := range tables {
		tb := timetravel.TableBounds{Table: names[i].Schema + "." + names[i].Name, Kind: timetravel.Kind(t.Rel), Source: timetravel.SourceNone}
		switch {
		case t.Rel == nil:
			tb.Note = "This name " + t.Why
		case allowed(t.Rel) != "":
			tb.Note = "This table " + allowed(t.Rel)
		default:
			tb.Available = true
			if at, ok := oldest[t.Rel.QName()]; ok {
				tb.Oldest, tb.Source = &at, timetravel.SourceHistoryRetention
			} else {
				tb.Note = "History is kept until removed"
			}
		}
		b.Tables[i] = tb
	}
	return b, nil
}

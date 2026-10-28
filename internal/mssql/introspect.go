package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/schema"
)

// userSchemas excludes the catalog schemas and the fixed database roles'
// schemas (ids from 16384), which hold nothing a diagram shows.
const userSchemas = `s.schema_id < 16384 AND s.name NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest')`

// Namespaces lists the database's user schemas with their table and view counts.
func Namespaces(ctx context.Context, db *sql.DB) ([]introspect.Namespace, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.name, count(o.object_id)
		FROM sys.schemas s LEFT JOIN sys.objects o ON o.schema_id = s.schema_id AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0
		WHERE `+userSchemas+` GROUP BY s.name ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []introspect.Namespace{}
	for rows.Next() {
		var n introspect.Namespace
		if err := rows.Scan(&n.Name, &n.RelationCount); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// heapWarnRows is the size from which a heap is a warning rather than a note.
const heapWarnRows = 10_000

// Introspect reads the given schemas (every user schema when empty) of the
// profile's database.
func Introspect(ctx context.Context, db *sql.DB, schemas []string) (*schema.Model, error) {
	start := time.Now()
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(schemas) == 0 {
		ns, err := Namespaces(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("schemas: %w", err)
		}
		for _, n := range ns {
			if n.RelationCount > 0 {
				schemas = append(schemas, n.Name)
			}
		}
		if len(schemas) == 0 {
			schemas = []string{"dbo"}
		}
	}
	m := &schema.Model{Engine: "sqlserver", ServerVersion: srv.Num, Schemas: slices.Clone(schemas),
		Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	marks := make([]string, len(schemas))
	args := make([]any, len(schemas))
	for i, s := range schemas {
		marks[i], args[i] = "@p"+strconv.Itoa(i+1), s
	}
	in := "s.name IN (" + strings.Join(marks, ", ") + ")"
	byName := map[schema.QualifiedName]int{}
	rel := func(s, t string) *schema.Relation {
		if i, ok := byName[schema.QualifiedName{Schema: s, Name: t}]; ok {
			return &m.Relations[i]
		}
		return nil
	}

	err = each(ctx, db, `SELECT s.name, o.name, o.type, CAST(ep.value AS nvarchar(4000)),
			(SELECT sum(p.rows) FROM sys.partitions p WHERE p.object_id = o.object_id AND p.index_id IN (0, 1))
		FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id
		LEFT JOIN sys.extended_properties ep ON ep.class = 1 AND ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
		WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0 AND `+in+` ORDER BY s.name, o.name`, args,
		func(r *sql.Rows) error {
			var s, t, kind string
			var comment sql.NullString
			var rowsEst sql.NullInt64
			if err := r.Scan(&s, &t, &kind, &comment, &rowsEst); err != nil {
				return err
			}
			x := schema.Relation{Schema: s, Name: t, Kind: schema.KindTable, Comment: comment.String}
			if strings.TrimSpace(kind) == "V" {
				x.Kind = schema.KindView
			} else if rowsEst.Valid {
				n := rowsEst.Int64
				x.RowEstimate = &n
			}
			byName[x.QName()] = len(m.Relations)
			m.Relations = append(m.Relations, x)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("tables: %w", err)
	}

	err = each(ctx, db, `SELECT s.name, o.name, c.name, TYPE_NAME(c.user_type_id), c.max_length, c.precision, c.scale,
			c.is_nullable, c.is_identity, c.is_computed, dc.definition, cc.definition, coalesce(cc.is_persisted, 0), CAST(ep.value AS nvarchar(4000))
		FROM sys.columns c JOIN sys.objects o ON o.object_id = c.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		LEFT JOIN sys.default_constraints dc ON dc.object_id = c.default_object_id
		LEFT JOIN sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
		LEFT JOIN sys.extended_properties ep ON ep.class = 1 AND ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.name = 'MS_Description'
		WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0 AND `+in+` ORDER BY s.name, o.name, c.column_id`, args,
		func(r *sql.Rows) error {
			var s, t, typ string
			var maxLen, precision, scale int
			var c schema.Column
			var identity, isComputed, persisted bool
			var dflt, computed, comment sql.NullString
			if err := r.Scan(&s, &t, &c.Name, &typ, &maxLen, &precision, &scale, &c.Nullable, &identity, &isComputed, &dflt, &computed, &persisted, &comment); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			c.Type = typeText(typ, maxLen, precision, scale)
			c.Default = unwrapDefault(dflt.String)
			c.Comment = comment.String
			if identity {
				c.Identity = "by_default"
			}
			// Without VIEW DEFINITION the expression is NULL but is_computed still says what the column is.
			if isComputed {
				c.Generated, c.GeneratedKind = computed.String, "virtual"
				if persisted {
					c.GeneratedKind = "stored"
				}
			}
			x.Columns = append(x.Columns, c)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	clustered := map[schema.QualifiedName]string{} // the clustered index's name; absent for a heap
	heaps := map[schema.QualifiedName]bool{}
	err = each(ctx, db, `SELECT s.name, o.name, coalesce(i.name, ''), i.index_id, i.type_desc, i.is_unique, i.is_primary_key,
			i.is_disabled, coalesce(i.filter_definition, ''), coalesce(c.name, ''), coalesce(ic.is_included_column, 0)
		FROM sys.indexes i JOIN sys.objects o ON o.object_id = i.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		LEFT JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		LEFT JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0 AND `+in+`
		ORDER BY s.name, o.name, i.index_id, ic.is_included_column, ic.key_ordinal, ic.index_column_id`, args,
		func(r *sql.Rows) error {
			var s, t, name, kind, filter, col string
			var id int
			var unique, primary, disabled, included bool
			if err := r.Scan(&s, &t, &name, &id, &kind, &unique, &primary, &disabled, &filter, &col, &included); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			switch id {
			case 0:
				heaps[x.QName()] = true
				return nil
			case 1:
				clustered[x.QName()] = name
			}
			n := len(x.Indexes)
			if n == 0 || x.Indexes[n-1].Name != name {
				x.Indexes = append(x.Indexes, schema.Index{Name: name, Columns: []string{}, Unique: unique, Primary: primary,
					Method: strings.ToLower(strings.ReplaceAll(kind, "_", " ")), Partial: filter, Valid: !disabled})
				n++
				if disabled {
					x.AddFinding("DBS-MSSQL-DISABLED-INDEX", "info",
						fmt.Sprintf("The index %s is disabled: the server neither maintains nor uses it until it is rebuilt", name), "")
				}
			}
			if col == "" {
				return nil
			}
			idx := &x.Indexes[n-1]
			if included {
				idx.Include = append(idx.Include, col)
			} else {
				idx.Columns = append(idx.Columns, col)
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}

	err = each(ctx, db, `SELECT s.name, o.name, fk.name, pc.name, rs.name, ro.name, rc.name,
			fk.update_referential_action_desc, fk.delete_referential_action_desc, fk.is_not_trusted, fk.is_disabled
		FROM sys.foreign_keys fk
		JOIN sys.objects o ON o.object_id = fk.parent_object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN sys.objects ro ON ro.object_id = fk.referenced_object_id JOIN sys.schemas rs ON rs.schema_id = ro.schema_id
		JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE `+in+` ORDER BY s.name, o.name, fk.name, fkc.constraint_column_id`, args,
		func(r *sql.Rows) error {
			var s, t, name, col, rs, rt, rc, onUpdate, onDelete string
			var untrusted, disabled bool
			if err := r.Scan(&s, &t, &name, &col, &rs, &rt, &rc, &onUpdate, &onDelete, &untrusted, &disabled); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			if n := len(x.ForeignKeys); n > 0 && x.ForeignKeys[n-1].Name == name {
				fk := &x.ForeignKeys[n-1]
				fk.Columns, fk.RefCols = append(fk.Columns, col), append(fk.RefCols, rc)
				return nil
			}
			x.ForeignKeys = append(x.ForeignKeys, schema.ForeignKey{Name: name, Columns: []string{col},
				Ref: schema.QualifiedName{Schema: rs, Name: rt}, RefCols: []string{rc},
				OnUpdate: action(onUpdate), OnDelete: action(onDelete), Validated: !untrusted})
			switch {
			case disabled:
				x.AddFinding("DBS-MSSQL-DISABLED-FK", "warn",
					fmt.Sprintf("The foreign key %s is disabled (NOCHECK), so new rows are not checked against %s.%s", name, rs, rt), col)
			case untrusted:
				x.AddFinding("DBS-MSSQL-UNTRUSTED-FK", "warn",
					fmt.Sprintf("The foreign key %s is not trusted (added or re-enabled WITH NOCHECK): existing rows may violate it, and the optimizer cannot rely on it to remove joins", name), col)
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("foreign keys: %w", err)
	}

	err = each(ctx, db, `SELECT s.name, o.name, ck.name, ck.definition, ck.is_not_trusted, ck.is_disabled
		FROM sys.check_constraints ck JOIN sys.objects o ON o.object_id = ck.parent_object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE `+in+` ORDER BY s.name, o.name, ck.name`, args,
		func(r *sql.Rows) error {
			var s, t string
			var c schema.Check
			var def sql.NullString
			var untrusted, disabled bool
			if err := r.Scan(&s, &t, &c.Name, &def, &untrusted, &disabled); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			c.Expr, c.Validated = def.String, !untrusted
			x.Checks = append(x.Checks, c)
			if untrusted || disabled {
				x.AddFinding("DBS-MSSQL-UNTRUSTED-CHECK", "warn",
					fmt.Sprintf("The check constraint %s is disabled or not trusted (WITH NOCHECK), so existing rows may violate it", c.Name), "")
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("checks: %w", err)
	}

	for i := range m.Relations {
		x := &m.Relations[i]
		for _, idx := range x.Indexes {
			switch {
			case idx.Primary:
				x.PrimaryKey = idx.Columns
			case idx.Unique && idx.Partial == "":
				x.Uniques = append(x.Uniques, idx.Columns)
			}
		}
		storageFindings(x, heaps[x.QName()], clustered[x.QName()])
	}
	addStubs(m)
	m.IntrospectedAt = time.Now().UTC()
	m.DurationMs = time.Since(start).Milliseconds()
	return m, nil
}

// storageFindings flags tables without a clustered index and clustered keys
// that lead with a random GUID.
func storageFindings(x *schema.Relation, heap bool, clusteredName string) {
	if x.Kind != schema.KindTable {
		return
	}
	if heap {
		level, rows := "info", int64(0)
		if x.RowEstimate != nil {
			rows = *x.RowEstimate
		}
		if rows >= heapWarnRows {
			level = "warn"
		}
		x.AddFinding("DBS-MSSQL-HEAP", level, "The table has no clustered index (a heap): updates that grow rows leave forwarded records, "+
			"and deletes may not free pages, so scans read more than the data", "")
		return
	}
	for _, idx := range x.Indexes {
		if idx.Name != clusteredName || len(idx.Columns) == 0 {
			continue
		}
		col := x.Column(idx.Columns[0])
		if col != nil && col.Type == "uniqueidentifier" && strings.Contains(strings.ToLower(col.Default), "newid()") {
			x.AddFinding("DBS-MSSQL-GUID-CLUSTER", "warn", fmt.Sprintf("The clustered index %s leads with %s, a random NEWID(): "+
				"inserts land all over the index, splitting pages; NEWSEQUENTIALID() or a sequential key avoids this", idx.Name, col.Name), col.Name)
		}
	}
}

// typeText spells a column type as it is declared: nvarchar(50), decimal(10,2), varbinary(max).
func typeText(typ string, maxLen, precision, scale int) string {
	switch typ {
	case "varchar", "char", "varbinary", "binary":
		if maxLen == -1 {
			return typ + "(max)"
		}
		return fmt.Sprintf("%s(%d)", typ, maxLen)
	case "nvarchar", "nchar":
		if maxLen == -1 {
			return typ + "(max)"
		}
		return fmt.Sprintf("%s(%d)", typ, maxLen/2)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", typ, precision, scale)
	case "datetime2", "time", "datetimeoffset":
		if scale != 7 {
			return fmt.Sprintf("%s(%d)", typ, scale)
		}
	}
	return typ
}

// unwrapDefault removes the parentheses SQL Server stores around a default: ((0)), (getdate()), (N'x').
func unwrapDefault(d string) string {
	for len(d) >= 2 && d[0] == '(' && d[len(d)-1] == ')' && balanced(d[1:len(d)-1]) {
		d = d[1 : len(d)-1]
	}
	return d
}

func balanced(s string) bool {
	depth, quoted := 0, false
	for _, c := range s {
		switch {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// action turns NO_ACTION into "no action".
func action(desc string) string {
	return strings.ToLower(strings.ReplaceAll(desc, "_", " "))
}

// addStubs adds the parent tables of foreign keys that point outside the
// read schemas, so every edge has both ends.
func addStubs(m *schema.Model) {
	for i := 0; i < len(m.Relations); i++ {
		for _, fk := range m.Relations[i].ForeignKeys {
			if m.Relation(fk.Ref) == nil {
				m.Relations = append(m.Relations, schema.Relation{Schema: fk.Ref.Schema, Name: fk.Ref.Name,
					Kind: schema.KindTable, Stub: true, PrimaryKey: fk.RefCols})
			}
		}
	}
}

func each(ctx context.Context, db *sql.DB, q string, args []any, f func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := f(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

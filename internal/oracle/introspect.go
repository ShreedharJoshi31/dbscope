package oracle

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

// userOwners are the schemas Oracle does not maintain itself.
const userOwners = `(SELECT username FROM all_users WHERE oracle_maintained = 'N')`

// Namespaces lists the schemas with tables or views the user can see,
// leaving out Oracle's own (SYS, SYSTEM, MDSYS and the rest).
func Namespaces(ctx context.Context, db *sql.DB) ([]introspect.Namespace, error) {
	rows, err := db.QueryContext(ctx, `SELECT owner, count(*) FROM all_objects
		WHERE object_type IN ('TABLE', 'VIEW') AND owner IN `+userOwners+` GROUP BY owner ORDER BY owner`)
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

// Introspect reads the given schemas (every visible user schema when empty).
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
			schemas = append(schemas, n.Name)
		}
	}
	m := &schema.Model{Engine: "oracle", ServerVersion: srv.Num, Schemas: slices.Clone(schemas),
		Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	if len(schemas) == 0 {
		return m, nil
	}
	marks := make([]string, len(schemas))
	args := make([]any, len(schemas))
	for i, s := range schemas {
		marks[i], args[i] = ":"+strconv.Itoa(i+1), s
	}
	in := "IN (" + strings.Join(marks, ", ") + ")"
	byName := map[schema.QualifiedName]int{}
	rel := func(s, t string) *schema.Relation {
		if i, ok := byName[schema.QualifiedName{Schema: s, Name: t}]; ok {
			return &m.Relations[i]
		}
		return nil
	}
	add := func(x schema.Relation) {
		byName[x.QName()] = len(m.Relations)
		m.Relations = append(m.Relations, x)
	}

	// Materialized views own a container table of the same name; it is listed once, as the view.
	err = each(ctx, db, `SELECT t.owner, t.table_name, t.num_rows, t.last_analyzed, t.partitioned, c.comments,
			CASE WHEN mv.mview_name IS NULL THEN 0 ELSE 1 END
		FROM all_tables t
		LEFT JOIN all_tab_comments c ON c.owner = t.owner AND c.table_name = t.table_name
		LEFT JOIN all_mviews mv ON mv.owner = t.owner AND mv.mview_name = t.table_name
		WHERE t.owner `+in+` AND t.dropped = 'NO'
			AND (t.iot_type IS NULL AND t.secondary = 'N' AND t.nested = 'NO' OR t.iot_type = 'IOT')
		ORDER BY t.owner, t.table_name`, args,
		func(r *sql.Rows) error {
			var s, t, partitioned string
			var rows sql.NullInt64
			var analyzed sql.NullTime
			var comment sql.NullString
			var mview int
			if err := r.Scan(&s, &t, &rows, &analyzed, &partitioned, &comment, &mview); err != nil {
				return err
			}
			x := schema.Relation{Schema: s, Name: t, Kind: schema.KindTable, Comment: comment.String}
			switch {
			case mview == 1:
				x.Kind = schema.KindMatView
			case partitioned == "YES":
				x.Kind = schema.KindPartitionedTable
			}
			if rows.Valid {
				n := rows.Int64
				x.RowEstimate = &n
			}
			if !analyzed.Valid && mview == 0 {
				x.AddFinding("DBS-ORA-NO-STATS", "info", "The table has no optimizer statistics, so the optimizer guesses its size; "+
					"gather them with DBMS_STATS.GATHER_TABLE_STATS", "")
			}
			add(x)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("tables: %w", err)
	}
	err = each(ctx, db, `SELECT v.owner, v.view_name, c.comments FROM all_views v
		LEFT JOIN all_tab_comments c ON c.owner = v.owner AND c.table_name = v.view_name
		WHERE v.owner `+in+` ORDER BY v.owner, v.view_name`, args,
		func(r *sql.Rows) error {
			var s, t string
			var comment sql.NullString
			if err := r.Scan(&s, &t, &comment); err != nil {
				return err
			}
			add(schema.Relation{Schema: s, Name: t, Kind: schema.KindView, Comment: comment.String})
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("views: %w", err)
	}

	err = each(ctx, db, `SELECT c.owner, c.table_name, c.column_name, c.data_type, c.data_length, c.char_length, c.char_used,
			c.data_precision, c.data_scale, c.nullable, c.data_default, c.identity_column, c.virtual_column, cc.comments
		FROM all_tab_cols c
		LEFT JOIN all_col_comments cc ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
		WHERE c.owner `+in+` AND c.hidden_column = 'NO' ORDER BY c.owner, c.table_name, c.column_id`, args,
		func(r *sql.Rows) error {
			var s, t, typ, nullable, identity, virtual string
			var length, charLength int
			var charUsed, dflt, comment sql.NullString
			var precision, scale sql.NullInt64
			var c schema.Column
			if err := r.Scan(&s, &t, &c.Name, &typ, &length, &charLength, &charUsed, &precision, &scale, &nullable, &dflt, &identity, &virtual, &comment); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			c.Type = typeText(typ, length, charLength, charUsed.String, precision, scale)
			c.Nullable = nullable == "Y"
			c.Comment = comment.String
			d := strings.TrimSpace(dflt.String)
			switch {
			case identity == "YES":
				c.Identity = "by_default"
			case virtual == "YES":
				c.Generated, c.GeneratedKind = d, "virtual"
			default:
				c.Default = d
			}
			x.Columns = append(x.Columns, c)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	// Index columns of function-based indexes are hidden SYS_NC columns; their expressions are read below.
	exprs := map[[3]string]string{}
	err = each(ctx, db, `SELECT index_owner, index_name, column_position, column_expression FROM all_ind_expressions
		WHERE table_owner `+in, args, func(r *sql.Rows) error {
		var o, n, e string
		var pos int
		if err := r.Scan(&o, &n, &pos, &e); err != nil {
			return err
		}
		exprs[[3]string{o, n, strconv.Itoa(pos)}] = strings.TrimSpace(e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index expressions: %w", err)
	}
	err = each(ctx, db, `SELECT i.table_owner, i.table_name, i.owner, i.index_name, i.index_type, i.uniqueness, i.status,
			ic.column_name, ic.column_position
		FROM all_indexes i JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name
		WHERE i.table_owner `+in+` AND i.index_type <> 'LOB'
		ORDER BY i.table_owner, i.table_name, i.index_name, ic.column_position`, args,
		func(r *sql.Rows) error {
			var s, t, owner, name, kind, uniqueness, status, col string
			var pos int
			if err := r.Scan(&s, &t, &owner, &name, &kind, &uniqueness, &status, &col, &pos); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			if e, ok := exprs[[3]string{owner, name, strconv.Itoa(pos)}]; ok {
				col = e
			}
			if n := len(x.Indexes); n > 0 && x.Indexes[n-1].Name == name {
				x.Indexes[n-1].Columns = append(x.Indexes[n-1].Columns, col)
				return nil
			}
			x.Indexes = append(x.Indexes, schema.Index{Name: name, Columns: []string{col}, Unique: uniqueness == "UNIQUE",
				Method: strings.ToLower(kind), Valid: status != "UNUSABLE"})
			if status == "UNUSABLE" {
				x.AddFinding("DBS-ORA-UNUSABLE-INDEX", "warn", fmt.Sprintf("The index %s is UNUSABLE: queries ignore it, "+
					"and inserts fail while it is unique; rebuild it", name), "")
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}

	type key struct{ owner, name string }
	keyCols := map[key][]string{}
	err = each(ctx, db, `SELECT owner, constraint_name, column_name FROM all_cons_columns
		WHERE owner `+in+` ORDER BY owner, constraint_name, position`, args, func(r *sql.Rows) error {
		var o, n string
		var col sql.NullString
		if err := r.Scan(&o, &n, &col); err != nil {
			return err
		}
		keyCols[key{o, n}] = append(keyCols[key{o, n}], col.String)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("constraint columns: %w", err)
	}
	err = each(ctx, db, `SELECT c.owner, c.table_name, c.constraint_name, c.constraint_type, c.status, c.validated,
			c.search_condition_vc, c.r_owner, c.r_constraint_name, rc.table_name, c.delete_rule, c.index_name, c.generated
		FROM all_constraints c
		LEFT JOIN all_constraints rc ON rc.owner = c.r_owner AND rc.constraint_name = c.r_constraint_name
		WHERE c.owner `+in+` AND c.constraint_type IN ('P', 'U', 'R', 'C')
		ORDER BY c.owner, c.table_name, c.constraint_name`, args,
		func(r *sql.Rows) error {
			var s, t, name, kind, status, validated, generated string
			var cond, rOwner, rName, rTable, deleteRule, indexName sql.NullString
			if err := r.Scan(&s, &t, &name, &kind, &status, &validated, &cond, &rOwner, &rName, &rTable, &deleteRule, &indexName, &generated); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			cols := keyCols[key{s, name}]
			trusted := status == "ENABLED" && validated == "VALIDATED"
			switch kind {
			case "P":
				x.PrimaryKey = cols
				markPrimary(x, indexName.String)
			case "U":
				x.Uniques = append(x.Uniques, cols)
			case "C":
				// NOT NULL is a system-named check constraint; the column already says it.
				if generated == "GENERATED NAME" && strings.HasSuffix(cond.String, " IS NOT NULL") {
					return nil
				}
				x.Checks = append(x.Checks, schema.Check{Name: name, Expr: cond.String, Validated: trusted})
			case "R":
				ref := keyCols[key{rOwner.String, rName.String}]
				x.ForeignKeys = append(x.ForeignKeys, schema.ForeignKey{Name: name, Columns: cols, RefCols: ref,
					Ref: schema.QualifiedName{Schema: rOwner.String, Name: rTable.String}, OnDelete: strings.ToLower(deleteRule.String), OnUpdate: "no action",
					Validated: trusted})
			}
			if !trusted && kind != "P" {
				col := ""
				if len(cols) == 1 {
					col = cols[0]
				}
				x.AddFinding("DBS-ORA-NOT-VALIDATED", "warn", fmt.Sprintf("The constraint %s is %s and %s: existing rows may violate it",
					name, strings.ToLower(status), strings.ToLower(validated)), col)
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("constraints: %w", err)
	}

	for i := range m.Relations {
		unindexedForeignKeys(&m.Relations[i])
	}
	addStubs(m)
	m.IntrospectedAt = time.Now().UTC()
	m.DurationMs = time.Since(start).Milliseconds()
	return m, nil
}

func markPrimary(x *schema.Relation, index string) {
	for i := range x.Indexes {
		if x.Indexes[i].Name == index {
			x.Indexes[i].Primary = true
		}
	}
}

// unindexedForeignKeys flags foreign keys whose columns lead no index: a
// delete or key update on the parent then locks the whole child table.
func unindexedForeignKeys(x *schema.Relation) {
	for _, fk := range x.ForeignKeys {
		covered := false
		for _, idx := range x.Indexes {
			if len(idx.Columns) >= len(fk.Columns) && slices.Equal(idx.Columns[:len(fk.Columns)], fk.Columns) {
				covered = true
				break
			}
		}
		if !covered {
			x.AddFinding("DBS-ORA-FK-NO-INDEX", "warn", fmt.Sprintf("No index leads with the columns of the foreign key %s: "+
				"deleting or re-keying a %s row locks all of %s and scans it", fk.Name, fk.Ref.Name, x.Name), fk.Columns[0])
		}
	}
}

// typeText spells a column type as declared: VARCHAR2(100 CHAR), NUMBER(10,2), TIMESTAMP(6).
func typeText(typ string, length, charLength int, charUsed string, precision, scale sql.NullInt64) string {
	switch typ {
	case "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR":
		if charUsed == "C" && !strings.HasPrefix(typ, "N") {
			return fmt.Sprintf("%s(%d CHAR)", typ, charLength)
		}
		return fmt.Sprintf("%s(%d)", typ, charLength)
	case "RAW":
		return fmt.Sprintf("RAW(%d)", length)
	case "NUMBER":
		switch {
		case !precision.Valid && scale.Valid && scale.Int64 == 0:
			return "INTEGER"
		case !precision.Valid:
			return "NUMBER"
		case scale.Valid && scale.Int64 != 0:
			return fmt.Sprintf("NUMBER(%d,%d)", precision.Int64, scale.Int64)
		default:
			return fmt.Sprintf("NUMBER(%d)", precision.Int64)
		}
	case "FLOAT":
		if precision.Valid {
			return fmt.Sprintf("FLOAT(%d)", precision.Int64)
		}
	}
	return typ
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

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/schema"
)

// readOnly is how every dbscope transaction on MySQL starts: the server then
// refuses writes, and with one statement per call nothing can commit early.
var readOnly = &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}

// Namespaces lists the databases (MySQL's schemas) other than the server's own.
func Namespaces(ctx context.Context, db *sql.DB) ([]introspect.Namespace, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.SCHEMA_NAME, count(t.TABLE_NAME)
		FROM information_schema.SCHEMATA s LEFT JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME
		GROUP BY s.SCHEMA_NAME ORDER BY s.SCHEMA_NAME`)
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
		if !isSystem(n.Name) {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// Introspect reads the given databases (the profile's own when empty) in one
// read-only REPEATABLE READ transaction.
func Introspect(ctx context.Context, db *sql.DB, schemas []string, fallback string) (*schema.Model, error) {
	start := time.Now()
	if len(schemas) == 0 {
		schemas = []string{fallback}
	}
	tx, err := db.BeginTx(ctx, readOnly)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	srv, err := Describe(ctx, tx)
	if err != nil {
		return nil, err
	}
	m := &schema.Model{Engine: srv.Flavor, ServerVersion: srv.Num, Schemas: slices.Clone(schemas),
		Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(schemas)), ",") + ")"
	args := make([]any, len(schemas))
	for i, s := range schemas {
		args[i] = s
	}
	byName := map[schema.QualifiedName]int{}
	rel := func(s, t string) *schema.Relation {
		if i, ok := byName[schema.QualifiedName{Schema: s, Name: t}]; ok {
			return &m.Relations[i]
		}
		return nil
	}

	err = each(ctx, tx, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, TABLE_ROWS, coalesce(TABLE_COMMENT, '')
		FROM information_schema.TABLES WHERE TABLE_SCHEMA IN `+in+` ORDER BY TABLE_SCHEMA, TABLE_NAME`, args,
		func(r *sql.Rows) error {
			var s, t, kind, comment string
			var rowsEst sql.NullInt64
			if err := r.Scan(&s, &t, &kind, &rowsEst, &comment); err != nil {
				return err
			}
			x := schema.Relation{Schema: s, Name: t, Kind: schema.KindTable, Comment: comment}
			switch kind {
			case "VIEW", "SYSTEM VIEW":
				x.Kind, x.Comment = schema.KindView, "" // a view's comment is the literal "VIEW"
			case "SEQUENCE":
				return nil // MariaDB sequences are not relations a diagram shows
			default:
				if rowsEst.Valid {
					// InnoDB's TABLE_ROWS is an estimate that can be off by 40-50%.
					n := rowsEst.Int64
					x.RowEstimate = &n
				}
			}
			byName[x.QName()] = len(m.Relations)
			m.Relations = append(m.Relations, x)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("tables: %w", err)
	}

	err = each(ctx, tx, `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT,
			EXTRA, coalesce(GENERATION_EXPRESSION, ''), COLUMN_COMMENT, COLUMN_KEY
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA IN `+in+`
		ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`, args,
		func(r *sql.Rows) error {
			var s, t, extra, key string
			var c schema.Column
			var nullable string
			var dflt sql.NullString
			if err := r.Scan(&s, &t, &c.Name, &c.Type, &nullable, &dflt, &extra, &c.Generated, &c.Comment, &key); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			c.Nullable = nullable == "YES"
			c.Default = columnDefault(dflt, srv.Flavor)
			extra = strings.ToLower(extra)
			switch {
			case strings.Contains(extra, "auto_increment"):
				c.Identity = "by_default"
			case strings.Contains(extra, "stored generated") || strings.Contains(extra, "persistent generated"):
				c.GeneratedKind = "stored"
			case strings.Contains(extra, "virtual generated"):
				c.GeneratedKind = "virtual"
			}
			if c.GeneratedKind == "" {
				c.Generated = ""
			}
			x.Columns = append(x.Columns, c)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	// MariaDB has no STATISTICS.EXPRESSION; MySQL 8.0.13+ indexes expressions.
	expr := "NULL"
	if srv.Flavor == "mysql" && srv.Num >= 80013 {
		expr = "EXPRESSION"
	}
	err = each(ctx, tx, `SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, coalesce(COLUMN_NAME, ''), `+expr+`, INDEX_TYPE
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA IN `+in+`
		ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, args,
		func(r *sql.Rows) error {
			var s, t, name, col, method string
			var nonUnique int
			var expression sql.NullString
			if err := r.Scan(&s, &t, &name, &nonUnique, &col, &expression, &method); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			if col == "" {
				col = expression.String
			}
			if n := len(x.Indexes); n > 0 && x.Indexes[n-1].Name == name {
				x.Indexes[n-1].Columns = append(x.Indexes[n-1].Columns, col)
				return nil
			}
			x.Indexes = append(x.Indexes, schema.Index{Name: name, Columns: []string{col}, Unique: nonUnique == 0,
				Primary: name == "PRIMARY", Method: strings.ToLower(method), Valid: true})
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}
	for i := range m.Relations {
		x := &m.Relations[i]
		for _, idx := range x.Indexes {
			switch {
			case idx.Primary:
				x.PrimaryKey = idx.Columns
			case idx.Unique:
				x.Uniques = append(x.Uniques, idx.Columns)
			}
		}
	}

	// MariaDB hides REFERENTIAL_CONSTRAINTS rows from a SELECT-only user while
	// KEY_COLUMN_USAGE still lists the keys, so the rules are joined optionally
	// and read from SHOW CREATE TABLE when missing.
	missingRules := map[schema.QualifiedName]bool{}
	err = each(ctx, tx, `SELECT k.TABLE_SCHEMA, k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME,
			k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
			coalesce(c.UPDATE_RULE, ''), coalesce(c.DELETE_RULE, '')
		FROM information_schema.KEY_COLUMN_USAGE k
		LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS c
			ON c.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND c.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND c.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA IN `+in+` AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.TABLE_SCHEMA, k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, args,
		func(r *sql.Rows) error {
			var s, t, name, col, rs, rt, rc, onUpdate, onDelete string
			if err := r.Scan(&s, &t, &name, &col, &rs, &rt, &rc, &onUpdate, &onDelete); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			if onDelete == "" {
				missingRules[x.QName()] = true
			}
			if n := len(x.ForeignKeys); n > 0 && x.ForeignKeys[n-1].Name == name {
				fk := &x.ForeignKeys[n-1]
				fk.Columns, fk.RefCols = append(fk.Columns, col), append(fk.RefCols, rc)
				return nil
			}
			x.ForeignKeys = append(x.ForeignKeys, schema.ForeignKey{Name: name, Columns: []string{col},
				Ref: schema.QualifiedName{Schema: rs, Name: rt}, RefCols: []string{rc},
				OnUpdate: strings.ToLower(onUpdate), OnDelete: strings.ToLower(onDelete), Validated: true})
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("foreign keys: %w", err)
	}
	for q := range missingRules {
		if err := rulesFromDDL(ctx, tx, rel(q.Schema, q.Name)); err != nil {
			return nil, fmt.Errorf("foreign key rules of %s: %w", q, err)
		}
	}
	if err := checks(ctx, tx, srv, in, args, rel); err != nil {
		return nil, fmt.Errorf("checks: %w", err)
	}
	addStubs(m)
	m.IntrospectedAt = time.Now().UTC()
	m.DurationMs = time.Since(start).Milliseconds()
	return m, nil
}

// checks reads CHECK constraints: MariaDB's CHECK_CONSTRAINTS names the
// table, MySQL's (8.0.16+, when they started being enforced) needs TABLE_CONSTRAINTS.
func checks(ctx context.Context, tx *sql.Tx, srv Server, in string, args []any, rel func(s, t string) *schema.Relation) error {
	var q string
	switch {
	case srv.Flavor == "mariadb":
		q = `SELECT CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, CHECK_CLAUSE
			FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN ` + in
	case srv.Num >= 80016:
		q = `SELECT t.TABLE_SCHEMA, t.TABLE_NAME, t.CONSTRAINT_NAME, c.CHECK_CLAUSE
			FROM information_schema.TABLE_CONSTRAINTS t JOIN information_schema.CHECK_CONSTRAINTS c
				ON c.CONSTRAINT_SCHEMA = t.CONSTRAINT_SCHEMA AND c.CONSTRAINT_NAME = t.CONSTRAINT_NAME
			WHERE t.CONSTRAINT_TYPE = 'CHECK' AND t.TABLE_SCHEMA IN ` + in
	default:
		return nil
	}
	return each(ctx, tx, q+` ORDER BY 1, 2, 3`, args, func(r *sql.Rows) error {
		var s, t string
		var c schema.Check
		if err := r.Scan(&s, &t, &c.Name, &c.Expr); err != nil {
			return err
		}
		if x := rel(s, t); x != nil {
			c.Validated = true
			x.Checks = append(x.Checks, c)
		}
		return nil
	})
}

var fkClause = regexp.MustCompile("(?i)CONSTRAINT `((?:[^`]|``)+)` FOREIGN KEY .*?REFERENCES [^(]+\\([^)]*\\)((?: ON (?:DELETE|UPDATE) (?:CASCADE|SET NULL|SET DEFAULT|RESTRICT|NO ACTION))*)")
var fkAction = regexp.MustCompile(`(?i)ON (DELETE|UPDATE) (CASCADE|SET NULL|SET DEFAULT|RESTRICT|NO ACTION)`)

// rulesFromDDL fills a table's foreign key actions from SHOW CREATE TABLE,
// which omits the default, RESTRICT.
func rulesFromDDL(ctx context.Context, tx *sql.Tx, r *schema.Relation) error {
	var name, ddl string
	q := "SHOW CREATE TABLE `" + strings.ReplaceAll(r.Schema, "`", "``") + "`.`" + strings.ReplaceAll(r.Name, "`", "``") + "`"
	if err := tx.QueryRowContext(ctx, q).Scan(&name, &ddl); err != nil {
		return err
	}
	for _, m := range fkClause.FindAllStringSubmatch(ddl, -1) {
		cname := strings.ReplaceAll(m[1], "``", "`")
		for i := range r.ForeignKeys {
			fk := &r.ForeignKeys[i]
			if fk.Name != cname || fk.OnDelete != "" {
				continue
			}
			fk.OnDelete, fk.OnUpdate = "restrict", "restrict"
			for _, a := range fkAction.FindAllStringSubmatch(m[2], -1) {
				if strings.EqualFold(a[1], "DELETE") {
					fk.OnDelete = strings.ToLower(a[2])
				} else {
					fk.OnUpdate = strings.ToLower(a[2])
				}
			}
		}
	}
	return nil
}

// columnDefault normalizes COLUMN_DEFAULT: MariaDB quotes string literals and
// spells DEFAULT NULL as the string NULL; MySQL leaves literals bare and uses
// SQL NULL for both "no default" and DEFAULT NULL.
func columnDefault(d sql.NullString, flavor string) string {
	if !d.Valid || (flavor == "mariadb" && d.String == "NULL") {
		return ""
	}
	return d.String
}

// addStubs adds the parent tables of foreign keys that point outside the
// read databases, so every edge has both ends.
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

func each(ctx context.Context, tx *sql.Tx, q string, args []any, f func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, q, args...)
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

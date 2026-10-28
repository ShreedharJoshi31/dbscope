package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
)

// Schema is the name SQLite gives the opened file; attached databases are not read.
const Schema = "main"

// Beginner is a *sql.DB or a *sql.Conn.
type Beginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// Introspect reads every table and view in one read transaction, so the
// result is one consistent snapshot.
func Introspect(ctx context.Context, db Beginner) (*schema.Model, error) {
	start := time.Now()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	m := &schema.Model{Engine: "sqlite", Schemas: []string{Schema}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		return nil, err
	}
	var major, minor, patch int
	fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	m.ServerVersion = major*1_000_000 + minor*1_000 + patch

	// pragma_table_list (SQLite 3.37) types FTS and other virtual tables and their shadow tables, which are skipped.
	rows, err := tx.QueryContext(ctx, `SELECT type, name, wr FROM pragma_table_list
		WHERE schema = 'main' AND type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	type object struct {
		kind, name   string
		withoutRowid bool
	}
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.kind, &o.name, &o.withoutRowid); err != nil {
			rows.Close()
			return nil, err
		}
		objects = append(objects, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	stats, err := rowCounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, o := range objects {
		r := schema.Relation{Schema: Schema, Name: o.name, Kind: schema.KindTable}
		if o.kind == "view" {
			r.Kind = schema.KindView
		}
		if err := columns(ctx, tx, &r); err != nil {
			return nil, fmt.Errorf("columns of %s: %w", o.name, err)
		}
		if r.Kind == schema.KindTable {
			if n, ok := stats[o.name]; ok {
				r.RowEstimate = &n
			}
			if err := foreignKeys(ctx, tx, &r); err != nil {
				return nil, fmt.Errorf("foreign keys of %s: %w", o.name, err)
			}
			if err := indexes(ctx, tx, &r); err != nil {
				return nil, fmt.Errorf("indexes of %s: %w", o.name, err)
			}
			markRowidAlias(&r, o.withoutRowid)
		}
		m.Relations = append(m.Relations, r)
	}
	fillReferencedColumns(m)
	m.IntrospectedAt = time.Now().UTC()
	m.DurationMs = time.Since(start).Milliseconds()
	return m, nil
}

func columns(ctx context.Context, tx *sql.Tx, r *schema.Relation) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value, pk, hidden
		FROM pragma_table_xinfo(?) ORDER BY cid`, r.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pkCol struct {
		name string
		pos  int
	}
	var pk []pkCol
	for rows.Next() {
		var c schema.Column
		var notNull, pkPos, hidden int
		var dflt sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &notNull, &dflt, &pkPos, &hidden); err != nil {
			return err
		}
		if hidden == 1 {
			continue // a virtual table's hidden column
		}
		c.Nullable = notNull == 0 && pkPos == 0
		c.Default = dflt.String
		switch hidden {
		case 2:
			c.GeneratedKind = "virtual"
		case 3:
			c.GeneratedKind = "stored"
		}
		if pkPos > 0 {
			pk = append(pk, pkCol{c.Name, pkPos})
		}
		r.Columns = append(r.Columns, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	slices.SortFunc(pk, func(a, b pkCol) int { return a.pos - b.pos })
	for _, c := range pk {
		r.PrimaryKey = append(r.PrimaryKey, c.name)
	}
	return nil
}

// markRowidAlias marks an INTEGER PRIMARY KEY, which is the table's rowid and
// assigned by SQLite when an insert omits it. Declared DESC it is an ordinary
// key with its own index, so a primary-key index rules it out.
func markRowidAlias(r *schema.Relation, withoutRowid bool) {
	if withoutRowid || len(r.PrimaryKey) != 1 || slices.ContainsFunc(r.Indexes, func(i schema.Index) bool { return i.Primary }) {
		return
	}
	if c := r.Column(r.PrimaryKey[0]); c != nil && strings.EqualFold(c.Type, "INTEGER") {
		c.Identity = "by_default"
	}
}

var fkActions = map[string]string{"NO ACTION": "no action", "RESTRICT": "restrict", "CASCADE": "cascade",
	"SET NULL": "set null", "SET DEFAULT": "set default"}

func foreignKeys(ctx context.Context, tx *sql.Tx, r *schema.Relation) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, "table", "from", "to", on_update, on_delete
		FROM pragma_foreign_key_list(?) ORDER BY id, seq`, r.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[int]int{}
	for rows.Next() {
		var id int
		var table, from, onUpdate, onDelete string
		var to sql.NullString
		if err := rows.Scan(&id, &table, &from, &to, &onUpdate, &onDelete); err != nil {
			return err
		}
		i, ok := byID[id]
		if !ok {
			// SQLite keeps constraint names only in the CREATE TABLE text; number them instead.
			r.ForeignKeys = append(r.ForeignKeys, schema.ForeignKey{
				Name: r.Name + "_fk" + strconv.Itoa(id), Ref: schema.QualifiedName{Schema: Schema, Name: table},
				OnDelete: fkActions[onDelete], OnUpdate: fkActions[onUpdate], Validated: true,
			})
			i = len(r.ForeignKeys) - 1
			byID[id] = i
		}
		fk := &r.ForeignKeys[i]
		fk.Columns = append(fk.Columns, from)
		fk.RefCols = append(fk.RefCols, to.String) // empty: the parent's primary key, filled in later
	}
	return rows.Err()
}

// fillReferencedColumns resolves foreign keys written as REFERENCES parent
// (no column list), which point at the parent's primary key.
func fillReferencedColumns(m *schema.Model) {
	for i := range m.Relations {
		for j := range m.Relations[i].ForeignKeys {
			fk := &m.Relations[i].ForeignKeys[j]
			if !slices.Contains(fk.RefCols, "") {
				continue
			}
			parent := m.Relation(fk.Ref)
			if parent == nil {
				// SQLite accepts a reference to a table that does not exist.
				parent = &schema.Relation{}
				for _, rel := range m.Relations {
					if strings.EqualFold(rel.Name, fk.Ref.Name) {
						parent = &rel
						break
					}
				}
			}
			if len(parent.PrimaryKey) == len(fk.RefCols) {
				fk.RefCols = slices.Clone(parent.PrimaryKey)
			}
		}
	}
}

func indexes(ctx context.Context, tx *sql.Tx, r *schema.Relation) error {
	rows, err := tx.QueryContext(ctx, `SELECT l.name, l."unique", l.origin, l.partial, coalesce(s.sql, '')
		FROM pragma_index_list(?) l LEFT JOIN sqlite_schema s ON s.type = 'index' AND s.name = l.name ORDER BY l.name`, r.Name)
	if err != nil {
		return err
	}
	type entry struct {
		idx    schema.Index
		origin string
	}
	var list []entry
	for rows.Next() {
		var e entry
		var unique, partial int
		var ddl string
		if err := rows.Scan(&e.idx.Name, &unique, &e.origin, &partial, &ddl); err != nil {
			rows.Close()
			return err
		}
		e.idx.Unique, e.idx.Primary, e.idx.Method, e.idx.Valid = unique == 1, e.origin == "pk", "btree", true
		if m := indexWhere.FindStringSubmatch(ddl); partial == 1 && m != nil {
			e.idx.Partial = strings.TrimSpace(m[1])
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range list {
		cols, err := tx.QueryContext(ctx, `SELECT coalesce(name, '<expression>') FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, e.idx.Name)
		if err != nil {
			return err
		}
		for cols.Next() {
			var c string
			if err := cols.Scan(&c); err != nil {
				cols.Close()
				return err
			}
			e.idx.Columns = append(e.idx.Columns, c)
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			return err
		}
		if e.origin == "u" {
			r.Uniques = append(r.Uniques, e.idx.Columns)
		}
		r.Indexes = append(r.Indexes, e.idx)
	}
	return nil
}

// An index's columns cannot hold a subquery, so its only WHERE keyword starts the predicate.
var indexWhere = regexp.MustCompile(`(?is)\bWHERE\b(.*)$`)

// rowCounts reads ANALYZE's estimates from sqlite_stat1 when it exists: the
// first number of a table's stat is its row count. Counting rows would read
// every page of a large file.
func rowCounts(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	out := map[string]int64{}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name = 'sqlite_stat1'`).Scan(&exists); err != nil || exists == 0 {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT tbl, stat FROM sqlite_stat1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tbl string
		var stat sql.NullString
		if err := rows.Scan(&tbl, &stat); err != nil {
			return nil, err
		}
		first, _, _ := strings.Cut(stat.String, " ")
		if n, err := strconv.ParseInt(first, 10, 64); err == nil && n > out[tbl] {
			out[tbl] = n
		}
	}
	return out, rows.Err()
}

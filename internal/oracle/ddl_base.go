package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/schema"
)

// Base is the server and schema migrations are checked against.
type Base struct {
	Server Server
	// Banner is v$version's first line, e.g. "Oracle AI Database 26ai Free Release 23.26.3.0.0 - ...".
	Banner string
	// Online is whether v$option grants "Online Index Build", which ONLINE
	// index builds and ALTER TABLE ... MOVE ONLINE need; OnlineKnown is false
	// when v$option could not be read.
	Online, OnlineKnown bool
	// DDLLockTimeout is the instance's ddl_lock_timeout in seconds, -1 when unread.
	DDLLockTimeout int
	// Recyclebin is the recyclebin parameter ("on" or "off"), "" when unread.
	Recyclebin string
	// Schema is the schema unqualified names resolve to (upper-case as stored).
	Schema string
	// Tables is keyed by "OWNER.TABLE" as stored in the dictionary.
	Tables map[string]*TableInfo
	// Indexes maps "OWNER.INDEX" to its table's key.
	Indexes map[string]string
	// Source is "live", or "none" when the base is empty.
	Source string
	// Warnings are reads that were skipped, with why.
	Warnings []string
}

// TableInfo is what the rules need to know about a base table.
type TableInfo struct {
	Display string // OWNER.TABLE
	// Rows is from optimizer statistics; RowsKnown is false without them.
	Rows      int64
	RowsKnown bool
	// Bytes is the size of the table's segments, IndexBytes of its indexes; Sized is false when unknown.
	Bytes, IndexBytes int64
	Sized             bool
	Columns           map[string]ColumnInfo // keyed by name as stored
	// Constraints maps a constraint name to what dropping it stops enforcing.
	Constraints map[string]string
	// UniqueIndexes maps a unique index's name to what dropping it stops enforcing.
	UniqueIndexes map[string]string
	PrimaryKey    string // the primary key constraint's name
}

// ColumnInfo is a base column.
type ColumnInfo struct {
	Type     string // as introspect spells it: VARCHAR2(100 CHAR), NUMBER(10,2)
	Nullable bool
}

// ReadBase reads the server's version, edition and settings, and the
// tables, columns, constraints, indexes and segment sizes of schemas
// (every visible user schema when empty). Settings and sizes that need a
// privilege the user lacks are skipped with a warning.
func ReadBase(ctx context.Context, db *sql.DB, schemas []string) (*Base, error) {
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	b := &Base{Server: srv, Tables: map[string]*TableInfo{}, Indexes: map[string]string{}, Source: "live", DDLLockTimeout: -1}
	if err := db.QueryRowContext(ctx, `SELECT sys_context('USERENV', 'CURRENT_SCHEMA') FROM dual`).Scan(&b.Schema); err != nil {
		return nil, err
	}
	if len(schemas) == 1 {
		b.Schema = schemas[0]
	}
	warn := func(msg string) { b.Warnings = append(b.Warnings, msg) }
	var banner sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT banner_full FROM v$version WHERE rownum = 1`).Scan(&banner); err != nil {
		warn("v$version was not read, so the edition is unknown: " + firstLine(err))
	} else {
		b.Banner, _, _ = strings.Cut(banner.String, "\n")
	}
	var online string
	if err := db.QueryRowContext(ctx, `SELECT value FROM v$option WHERE parameter = 'Online Index Build'`).Scan(&online); err != nil {
		warn("v$option was not read, so whether this edition allows ONLINE index builds is unknown: " + firstLine(err))
	} else {
		b.Online, b.OnlineKnown = online == "TRUE", true
	}
	rows, err := db.QueryContext(ctx, `SELECT name, value FROM v$parameter WHERE name IN ('ddl_lock_timeout', 'recyclebin')`)
	if err != nil {
		warn("v$parameter was not read (it needs SELECT_CATALOG_ROLE), so ddl_lock_timeout is assumed to be 0 and the recycle bin on, the defaults: " + firstLine(err))
	} else {
		for rows.Next() {
			var n string
			var v sql.NullString
			if rows.Scan(&n, &v) != nil {
				break
			}
			switch n {
			case "ddl_lock_timeout":
				b.DDLLockTimeout, _ = strconv.Atoi(v.String)
			case "recyclebin":
				b.Recyclebin = strings.ToLower(v.String)
			}
		}
		rows.Close()
	}

	m, err := Introspect(ctx, db, schemas)
	if err != nil {
		return nil, err
	}
	for _, r := range m.Relations {
		if r.Stub || (r.Kind != schema.KindTable && r.Kind != schema.KindPartitionedTable) {
			continue
		}
		key := r.Schema + "." + r.Name
		t := &TableInfo{Display: key, Columns: map[string]ColumnInfo{}, Constraints: map[string]string{}, UniqueIndexes: map[string]string{}}
		if r.RowEstimate != nil {
			t.Rows, t.RowsKnown = *r.RowEstimate, true
		}
		for _, c := range r.Columns {
			t.Columns[c.Name] = ColumnInfo{Type: c.Type, Nullable: c.Nullable}
		}
		dup := func(cols []string) string {
			return "duplicate (" + strings.Join(cols, ", ") + ") values can be written"
		}
		for _, c := range r.Checks {
			t.Constraints[c.Name] = "rows breaking " + c.Expr + " can be written"
		}
		for _, fk := range r.ForeignKeys {
			t.Constraints[fk.Name] = fmt.Sprintf("(%s) is no longer checked against %s, so rows can point at %s rows that do not exist",
				strings.Join(fk.Columns, ", "), fk.Ref, fk.Ref)
		}
		for _, i := range r.Indexes {
			b.Indexes[r.Schema+"."+i.Name] = key
			if i.Unique {
				t.UniqueIndexes[i.Name] = dup(i.Columns)
			}
		}
		b.Tables[key] = t
	}
	if err := b.readConstraintNames(ctx, db, m.Schemas); err != nil {
		return nil, err
	}
	b.readSizes(ctx, db, m.Schemas)
	return b, nil
}

// readConstraintNames names primary and unique constraints, which the
// model keeps only as column lists.
func (b *Base) readConstraintNames(ctx context.Context, db *sql.DB, schemas []string) error {
	if len(schemas) == 0 {
		return nil
	}
	in, args := inList(schemas)
	return each(ctx, db, `SELECT c.owner, c.table_name, c.constraint_name, c.constraint_type,
			listagg(cc.column_name, ', ') WITHIN GROUP (ORDER BY cc.position)
		FROM all_constraints c JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
		WHERE c.owner `+in+` AND c.constraint_type IN ('P', 'U')
		GROUP BY c.owner, c.table_name, c.constraint_name, c.constraint_type`, args, func(r *sql.Rows) error {
		var s, t, name, kind, cols string
		if err := r.Scan(&s, &t, &name, &kind, &cols); err != nil {
			return err
		}
		if ti := b.Tables[s+"."+t]; ti != nil {
			ti.Constraints[name] = "duplicate (" + cols + ") values can be written"
			if kind == "P" {
				ti.PrimaryKey = name
				ti.Constraints[name] += ", and NULLs too"
			}
		}
		return nil
	})
}

// readSizes reads segment sizes from DBA_SEGMENTS, or from optimizer
// statistics (ALL_TABLES.BLOCKS) when the user cannot read it.
func (b *Base) readSizes(ctx context.Context, db *sql.DB, schemas []string) {
	if len(schemas) == 0 {
		return
	}
	in, args := inList(schemas)
	err := each(ctx, db, `SELECT s.owner, coalesce(i.table_name, l.table_name, s.segment_name), CASE WHEN i.index_name IS NULL THEN 0 ELSE 1 END, sum(s.bytes)
		FROM dba_segments s
		LEFT JOIN dba_indexes i ON i.owner = s.owner AND i.index_name = s.segment_name AND s.segment_type LIKE 'INDEX%' AND i.index_type <> 'LOB'
		LEFT JOIN dba_lobs l ON l.owner = s.owner AND (l.segment_name = s.segment_name OR l.index_name = s.segment_name)
		WHERE s.owner `+in+`
		GROUP BY s.owner, coalesce(i.table_name, l.table_name, s.segment_name), CASE WHEN i.index_name IS NULL THEN 0 ELSE 1 END`, args,
		func(r *sql.Rows) error {
			var s, t string
			var index int
			var n int64
			if err := r.Scan(&s, &t, &index, &n); err != nil {
				return err
			}
			if ti := b.Tables[s+"."+t]; ti != nil {
				if index == 1 {
					ti.IndexBytes += n
				} else {
					ti.Bytes += n
				}
			}
			return nil
		})
	if err == nil {
		for _, t := range b.Tables {
			t.Sized = true // no segment yet (deferred segment creation) means empty
		}
		return
	}
	b.Warnings = append(b.Warnings, "DBA_SEGMENTS was not read (it needs SELECT_CATALOG_ROLE), so table sizes come from optimizer statistics: "+firstLine(err))
	err = each(ctx, db, `SELECT t.owner, t.table_name, t.blocks * coalesce(ts.block_size, 8192) FROM all_tables t
		LEFT JOIN user_tablespaces ts ON ts.tablespace_name = t.tablespace_name
		WHERE t.owner `+in+` AND t.blocks IS NOT NULL`, args, func(r *sql.Rows) error {
		var s, t string
		var n int64
		if err := r.Scan(&s, &t, &n); err != nil {
			return err
		}
		if ti := b.Tables[s+"."+t]; ti != nil {
			ti.Bytes, ti.Sized = n, true
		}
		return nil
	})
	if err != nil {
		b.Warnings = append(b.Warnings, "table sizes were not read: "+firstLine(err))
	}
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return strings.TrimSpace(s)
}

package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dbscope/dbscope/internal/schema"
)

// DDLBase is the server and schema migrations are checked against.
type DDLBase struct {
	Server Server
	// EngineEdition is SERVERPROPERTY('EngineEdition'): 2 Standard, 3
	// Enterprise and Developer, 4 Express, 5 Azure SQL Database, 8 Managed Instance.
	EngineEdition int
	// Schema is the user's default schema, which unqualified names resolve to.
	Schema string
	// Tables is keyed by lower-case "schema.table".
	Tables map[string]*DDLTable
	// Source is "live", or "none" when nothing was read.
	Source string
	// Warnings are reads that were skipped.
	Warnings []string
}

// DDLTable is what the rules need to know about a base table.
type DDLTable struct {
	ObjectID    int64
	Rows, Bytes int64
	Sized       bool
	// Columns is keyed by lower-case name.
	Columns map[string]*DDLColumn
	// Constraints maps a lower-case foreign key, check, unique or primary key
	// constraint or unique index name to what dropping it stops enforcing.
	Constraints map[string]string
	// KeyIndexes are the lower-case names of indexes that enforce a primary
	// key or unique constraint; DROP INDEX refuses them.
	KeyIndexes map[string]bool
	// ForeignKeyRefs maps a lower-case foreign key name to the table it references.
	ForeignKeyRefs map[string]string
	// ReferencedBy counts foreign keys of other tables that point at this one.
	ReferencedBy int
}

// DDLColumn is a base column and what depends on it.
type DDLColumn struct {
	Type     string // as declared, lower-case: varchar(50), decimal(10,2), nvarchar(max)
	Nullable bool
	// Indexed: an index (key or included column), primary key or unique constraint uses it.
	Indexed bool
	// Checked: a check constraint or computed column names it.
	Checked bool
	// Default: a default constraint is bound to it.
	Default bool
	// ForeignKey: a foreign key references it or is defined on it.
	ForeignKey bool
}

// Online reports whether the edition runs online index operations and adds
// NOT NULL columns with a constant default as a metadata change: Enterprise
// and Developer, Azure SQL Database and Managed Instance
// (https://learn.microsoft.com/sql/sql-server/editions-and-components-of-sql-server-2022#rdbms-high-availability).
func (b *DDLBase) Online() bool {
	switch b.EngineEdition {
	case 3, 5, 8:
		return true
	}
	return false
}

// ReadBase reads the default schema, table sizes, column types and what
// depends on each column, for the migration rules. Only catalog views are
// read, which need no server permission.
func ReadBase(ctx context.Context, db *sql.DB, schemas []string) (*DDLBase, error) {
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	b := &DDLBase{Server: srv, Tables: map[string]*DDLTable{}, Source: "live", Warnings: []string{}}
	if err := db.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('EngineEdition') AS int), coalesce(SCHEMA_NAME(), 'dbo')`).
		Scan(&b.EngineEdition, &b.Schema); err != nil {
		return nil, err
	}
	m, err := Introspect(ctx, db, schemas)
	if err != nil {
		return nil, err
	}
	key := func(s, t string) string { return strings.ToLower(s + "." + t) }
	for _, r := range m.Relations {
		if r.Kind != schema.KindTable || r.Stub {
			continue
		}
		t := &DDLTable{Columns: map[string]*DDLColumn{}, Constraints: map[string]string{}, KeyIndexes: map[string]bool{},
			ForeignKeyRefs: map[string]string{}}
		col := func(name string) *DDLColumn {
			if c := t.Columns[strings.ToLower(name)]; c != nil {
				return c
			}
			return &DDLColumn{}
		}
		for _, c := range r.Columns {
			t.Columns[strings.ToLower(c.Name)] = &DDLColumn{Type: strings.ToLower(c.Type), Nullable: c.Nullable, Default: c.Default != ""}
		}
		for _, c := range r.Columns {
			if c.Generated != "" {
				names(c.Generated, func(n string) { col(n).Checked = true })
			}
		}
		dup := func(cols []string) string {
			return "duplicate (" + strings.Join(cols, ", ") + ") values can be written"
		}
		for _, i := range r.Indexes {
			if i.Unique {
				t.Constraints[strings.ToLower(i.Name)] = dup(i.Columns)
			}
			for _, c := range append(append([]string{}, i.Columns...), i.Include...) {
				col(c).Indexed = true
			}
		}
		for _, c := range r.Checks {
			t.Constraints[strings.ToLower(c.Name)] = "rows breaking " + c.Expr + " can be written"
			names(c.Expr, func(n string) { col(n).Checked = true })
		}
		for _, fk := range r.ForeignKeys {
			t.Constraints[strings.ToLower(fk.Name)] = fmt.Sprintf("(%s) is no longer checked against %s, so rows can point at %s rows that do not exist",
				strings.Join(fk.Columns, ", "), fk.Ref, fk.Ref)
			t.ForeignKeyRefs[strings.ToLower(fk.Name)] = fk.Ref.Schema + "." + fk.Ref.Name
			for _, c := range fk.Columns {
				col(c).ForeignKey = true
			}
		}
		if r.RowEstimate != nil {
			t.Rows = *r.RowEstimate
		}
		b.Tables[key(r.Schema, r.Name)] = t
	}
	for _, r := range m.Relations {
		for _, fk := range r.ForeignKeys {
			if t := b.Tables[key(fk.Ref.Schema, fk.Ref.Name)]; t != nil {
				t.ReferencedBy++
				for _, c := range fk.RefCols {
					if x := t.Columns[strings.ToLower(c)]; x != nil {
						x.ForeignKey = true
					}
				}
			}
		}
	}
	err = each(ctx, db, `SELECT s.name, o.name, kc.name
		FROM sys.key_constraints kc JOIN sys.objects o ON o.object_id = kc.parent_object_id JOIN sys.schemas s ON s.schema_id = o.schema_id`, nil,
		func(r *sql.Rows) error {
			var s, n, k string
			if err := r.Scan(&s, &n, &k); err != nil {
				return err
			}
			if t := b.Tables[key(s, n)]; t != nil {
				t.KeyIndexes[strings.ToLower(k)] = true
			}
			return nil
		})
	if err != nil {
		b.Warnings = append(b.Warnings, "key constraints were not read ("+err.Error()+"), so DROP INDEX of a constraint's index is not flagged")
	}
	// Reserved pages of the heap or clustered index and every other index.
	err = each(ctx, db, `SELECT s.name, o.name, o.object_id, sum(au.total_pages) * 8192
		FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id
		JOIN sys.partitions p ON p.object_id = o.object_id
		JOIN sys.allocation_units au ON au.container_id = CASE au.type WHEN 2 THEN p.partition_id ELSE p.hobt_id END
		WHERE o.type = 'U' AND o.is_ms_shipped = 0 GROUP BY s.name, o.name, o.object_id`, nil,
		func(r *sql.Rows) error {
			var s, n string
			var id, bytes int64
			if err := r.Scan(&s, &n, &id, &bytes); err != nil {
				return err
			}
			if t := b.Tables[key(s, n)]; t != nil {
				t.ObjectID, t.Bytes, t.Sized = id, bytes, true
			}
			return nil
		})
	if err != nil {
		b.Warnings = append(b.Warnings, "table sizes were not read ("+err.Error()+"), so work on tables is rated as if their size were unknown")
	}
	return b, nil
}

// names calls f with each [bracketed] name in a definition SQL Server stored,
// which brackets every column it names.
func names(def string, f func(string)) {
	for _, t := range Tokens(def) {
		if strings.HasPrefix(t.Text, "[") {
			f(ident(t))
		}
	}
}

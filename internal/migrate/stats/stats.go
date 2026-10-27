// Package stats reads what migration scoring needs from a live database
// besides its structure: relation sizes (pg_class relpages x block_size),
// row estimates, lock_timeout/statement_timeout from pg_db_role_setting, the
// server version, constrained domains and default partitions. `dbscope
// stats export` writes it as JSON so CI can score a migration against a
// schema dump without database access. It never reads row data.
package stats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/migrate/apply"
)

// Format identifies the file layout.
const Format = "dbscope-stats/1"

// File is the stats export.
type File struct {
	Format        string     `json:"format"`
	ServerVersion int        `json:"serverVersion"`
	BlockSize     int        `json:"blockSize"`
	Schemas       []string   `json:"schemas"`
	Relations     []Relation `json:"relations"`
	Indexes       []Index    `json:"indexes"`
	// Settings are lock_timeout and statement_timeout values from
	// pg_db_role_setting for this database or all databases.
	Settings []Setting `json:"settings"`
	// CurrentRole is the role that exported the file.
	CurrentRole string        `json:"currentRole"`
	Catalog     apply.Catalog `json:"catalog"`
	ExportedAt  time.Time     `json:"exportedAt"`
	// ActivitySince is when the per-table activity counters (Relation.Scans,
	// RowsWritten) were last reset; InRecovery means they came from a
	// replica, which counts only its own reads and no writes.
	ActivitySince *time.Time `json:"activitySince,omitempty"`
	InRecovery    bool       `json:"inRecovery,omitempty"`
	// TotalScans and TotalRowsWritten cover every table in the database, so
	// a relation's share is of the whole database, not only Schemas.
	TotalScans       int64 `json:"totalScans,omitempty"`
	TotalRowsWritten int64 `json:"totalRowsWritten,omitempty"`
}

// Relation is one table, partition or materialized view.
type Relation struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // r, p, m (pg_class.relkind)
	Pages  int64  `json:"pages"`
	// RowEstimate is reltuples; nil when never analyzed.
	RowEstimate *int64 `json:"rowEstimate"`
	TableBytes  int64  `json:"tableBytes"`
	IndexBytes  int64  `json:"indexBytes"`
	Partition   bool   `json:"partition,omitempty"`
	// Scans (sequential plus index) and RowsWritten (inserted, updated,
	// deleted) since File.ActivitySince, from pg_stat_all_tables: how much
	// traffic would queue behind a lock on it.
	Scans       int64 `json:"scans,omitempty"`
	RowsWritten int64 `json:"rowsWritten,omitempty"`
}

// Index is one index's size.
type Index struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Table  string `json:"table"`
	Bytes  int64  `json:"bytes"`
}

// Setting is one per-role or per-database timeout.
type Setting struct {
	// Role is "" for a setting that applies to every role.
	Role string `json:"role"`
	// AllDatabases is true when the setting is not tied to this database.
	AllDatabases bool   `json:"allDatabases"`
	Name         string `json:"name"`
	Value        string `json:"value"`
}

// Querier is satisfied by pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB opens transactions (*pgx.Conn, *pgxpool.Pool).
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

const nonSystem = `n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'`

const headerQuery = `SELECT current_setting('server_version_num')::int, current_setting('block_size')::int, current_user::text,
       ARRAY(SELECT n.nspname::text FROM pg_namespace n WHERE ` + nonSystem + ` AND has_schema_privilege(n.oid, 'USAGE') ORDER BY 1),
       pg_is_in_recovery(), (SELECT pg_stat_get_db_stat_reset_time(oid) FROM pg_database WHERE datname = current_database())`

const relationsQuery = `SELECT n.nspname::text, c.relname::text, c.relkind::text, c.relpages::int8, c.reltuples::float8,
       coalesce((SELECT sum(ic.relpages)::int8 FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
                 WHERE i.indrelid = c.oid), 0), c.relispartition,
       pg_stat_get_numscans(c.oid) + coalesce((SELECT sum(pg_stat_get_numscans(i.indexrelid))::int8 FROM pg_index i
                 WHERE i.indrelid = c.oid), 0),
       pg_stat_get_tuples_inserted(c.oid) + pg_stat_get_tuples_updated(c.oid) + pg_stat_get_tuples_deleted(c.oid)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r','p','m') AND n.nspname = ANY($1::text[])
ORDER BY 1, 2`

// totalsQuery sums every table's activity, the denominator of a table's share.
const totalsQuery = `SELECT coalesce(sum(pg_stat_get_numscans(c.oid)), 0)::int8,
       coalesce(sum(pg_stat_get_tuples_inserted(c.oid) + pg_stat_get_tuples_updated(c.oid) + pg_stat_get_tuples_deleted(c.oid))
                FILTER (WHERE c.relkind IN ('r', 'm')), 0)::int8
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'm', 'i') AND ` + nonSystem

const indexesQuery = `SELECT n.nspname::text, ic.relname::text, c.relname::text, ic.relpages::int8
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = ANY($1::text[])
ORDER BY 1, 2`

// settingsQuery reads role and database settings for timeouts only; other
// settings are not exported.
const settingsQuery = `SELECT coalesce(r.rolname::text, ''), s.setdatabase = 0, cfg
FROM pg_db_role_setting s
LEFT JOIN pg_roles r ON r.oid = s.setrole
CROSS JOIN LATERAL unnest(s.setconfig) AS cfg
WHERE s.setdatabase IN (0, (SELECT oid FROM pg_database WHERE datname = current_database()))
  AND (cfg LIKE 'lock\_timeout=%' OR cfg LIKE 'statement\_timeout=%')
ORDER BY 1, 2, 3`

const domainsQuery = `SELECT n.nspname::text || '.' || t.typname::text
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE t.typtype = 'd' AND ` + nonSystem + `
  AND (t.typnotnull OR EXISTS (SELECT 1 FROM pg_constraint c WHERE c.contypid = t.oid))
ORDER BY 1`

const defaultPartitionsQuery = `SELECT pn.nspname::text, p.relname::text, cn.nspname::text, c.relname::text
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_namespace cn ON cn.oid = c.relnamespace
JOIN pg_class p ON p.oid = i.inhparent
JOIN pg_namespace pn ON pn.oid = p.relnamespace
WHERE c.relispartition AND pg_get_expr(c.relpartbound, c.oid) = 'DEFAULT'
  AND pn.nspname = ANY($1::text[])
ORDER BY 1, 2`

const clusteredQuery = `SELECT DISTINCT n.nspname::text || '.' || c.relname::text
FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE i.indisclustered AND n.nspname = ANY($1::text[])
ORDER BY 1`

// Read runs the queries on q (a read-only transaction). An empty schema
// list means every non-system schema the role has USAGE on. Settings that
// the role cannot read are left out and reported in the returned warnings.
func Read(ctx context.Context, q Querier, schemas []string) (*File, []string, error) {
	f := &File{Format: Format, ExportedAt: time.Now().UTC().Truncate(time.Second), Catalog: apply.Catalog{
		ConstrainedDomains: map[string]bool{}, DefaultPartitions: map[string]apply.QName{}, Clustered: map[string]bool{}}}
	var defaults []string
	if err := q.QueryRow(ctx, headerQuery).Scan(&f.ServerVersion, &f.BlockSize, &f.CurrentRole, &defaults, &f.InRecovery, &f.ActivitySince); err != nil {
		return nil, nil, fmt.Errorf("stats header: %w", err)
	}
	f.Schemas = slices.Clone(schemas)
	if len(f.Schemas) == 0 {
		f.Schemas = defaults
	}
	slices.Sort(f.Schemas)
	f.Schemas = slices.Compact(f.Schemas)
	if f.Schemas == nil {
		f.Schemas = []string{}
	}
	bs := int64(f.BlockSize)
	f.Relations, f.Indexes, f.Settings = []Relation{}, []Index{}, []Setting{}
	var r Relation
	var tuples float64
	var indexPages int64
	if err := each(ctx, q, relationsQuery, []any{f.Schemas}, []any{&r.Schema, &r.Name, &r.Kind, &r.Pages, &tuples, &indexPages, &r.Partition, &r.Scans, &r.RowsWritten}, func() {
		x := r
		x.TableBytes, x.IndexBytes = r.Pages*bs, indexPages*bs
		x.RowEstimate = nil
		if tuples >= 0 {
			n := int64(math.Round(tuples))
			x.RowEstimate = &n
		}
		f.Relations = append(f.Relations, x)
	}); err != nil {
		return nil, nil, fmt.Errorf("stats relations: %w", err)
	}
	var ix Index
	var pages int64
	if err := each(ctx, q, indexesQuery, []any{f.Schemas}, []any{&ix.Schema, &ix.Name, &ix.Table, &pages}, func() {
		x := ix
		x.Bytes = pages * bs
		f.Indexes = append(f.Indexes, x)
	}); err != nil {
		return nil, nil, fmt.Errorf("stats indexes: %w", err)
	}
	// Optional: without them a table's share is of the schemas read.
	_ = savepoint(ctx, q, func() error {
		return q.QueryRow(ctx, totalsQuery).Scan(&f.TotalScans, &f.TotalRowsWritten)
	})
	var warnings []string
	var s Setting
	var cfg string
	if err := savepoint(ctx, q, func() error {
		return each(ctx, q, settingsQuery, nil, []any{&s.Role, &s.AllDatabases, &cfg}, func() {
			name, value, _ := strings.Cut(cfg, "=")
			f.Settings = append(f.Settings, Setting{Role: s.Role, AllDatabases: s.AllDatabases, Name: name, Value: value})
		})
	}); err != nil {
		warnings = append(warnings, "role and database timeouts could not be read (pg_db_role_setting): "+err.Error())
	}
	var dom string
	if err := each(ctx, q, domainsQuery, nil, []any{&dom}, func() { f.Catalog.ConstrainedDomains[dom] = true }); err != nil {
		return nil, nil, fmt.Errorf("stats domains: %w", err)
	}
	var pn, p, cn, c string
	if err := each(ctx, q, defaultPartitionsQuery, []any{f.Schemas}, []any{&pn, &p, &cn, &c}, func() {
		f.Catalog.DefaultPartitions[pn+"."+p] = apply.QName{Schema: cn, Name: c}
	}); err != nil {
		return nil, nil, fmt.Errorf("stats default partitions: %w", err)
	}
	var cl string
	if err := each(ctx, q, clusteredQuery, []any{f.Schemas}, []any{&cl}, func() { f.Catalog.Clustered[cl] = true }); err != nil {
		return nil, nil, fmt.Errorf("stats clustered tables: %w", err)
	}
	return f, warnings, nil
}

func each(ctx context.Context, q Querier, sql string, args, dest []any, fn func()) error {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	_, err = pgx.ForEachRow(rows, dest, func() error { fn(); return nil })
	return err
}

// savepoint runs fn so that its failure does not abort the transaction.
func savepoint(ctx context.Context, q Querier, fn func() error) error {
	tx, ok := q.(pgx.Tx)
	if !ok {
		return fn()
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}

// Export reads the stats in one REPEATABLE READ READ ONLY transaction.
func Export(ctx context.Context, db DB, schemas []string, timeout time.Duration) (*File, []string, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if ms := timeout.Milliseconds(); ms > 0 {
		if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", strconv.FormatInt(ms, 10)); err != nil {
			return nil, nil, err
		}
	}
	return Read(ctx, tx, schemas)
}

// LockTimeout is the lock_timeout that applies to role in this database,
// following PostgreSQL's precedence: role+database, role, database. "" when none.
func (f *File) LockTimeout(role string) string {
	pick := func(match func(Setting) bool) (string, bool) {
		for _, s := range f.Settings {
			if s.Name == "lock_timeout" && match(s) {
				return s.Value, true
			}
		}
		return "", false
	}
	for _, m := range []func(Setting) bool{
		func(s Setting) bool { return s.Role == role && role != "" && !s.AllDatabases },
		func(s Setting) bool { return s.Role == role && role != "" && s.AllDatabases },
		func(s Setting) bool { return s.Role == "" && !s.AllDatabases },
	} {
		if v, ok := pick(m); ok {
			return v
		}
	}
	return ""
}

// Decode reads a stats file and checks its format marker.
func Decode(r io.Reader) (*File, error) {
	var f File
	dec := json.NewDecoder(io.LimitReader(r, 64<<20))
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("stats file: %w", err)
	}
	if f.Format != Format {
		return nil, errors.New("stats file: not a dbscope stats export (format " + strconv.Quote(f.Format) + ")")
	}
	if f.BlockSize <= 0 {
		f.BlockSize = 8192
	}
	return &f, nil
}

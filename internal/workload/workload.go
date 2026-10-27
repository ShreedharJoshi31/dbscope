// Package workload reads what the database's real traffic touches: per-table
// counters from pg_stat_user_tables and, when pg_stat_statements is readable,
// the statements attributed to the tables and columns they use
// (docs/WORKLOAD.md).
package workload

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/schema"
)

// Statement statuses.
const (
	StatusOK           = "ok"
	StatusNotInstalled = "not_installed" // CREATE EXTENSION was not run in this database
	StatusNotLoaded    = "not_loaded"    // not in shared_preload_libraries
	StatusUnavailable  = "unavailable"   // any other error, in Message
)

// Snapshot is one reading. Counters are cumulative since their reset times,
// not rates.
type Snapshot struct {
	GeneratedAt time.Time `json:"generatedAt"`
	// InRecovery: a replica, whose counters cover only what ran on it and
	// never count writes.
	InRecovery      bool              `json:"inRecovery"`
	TableStatsReset *time.Time        `json:"tableStatsReset,omitempty"`
	Statements      Statements        `json:"statements"`
	Tables          map[string]*Table `json:"tables"`
}

// Statements describes what pg_stat_statements contributed.
type Statements struct {
	Status  string     `json:"status"`
	Message string     `json:"message,omitempty"`
	Since   *time.Time `json:"since,omitempty"` // pg_stat_statements_info.stats_reset
	// Dealloc counts eviction rounds; a rising number means texts churn and
	// rarely run statements are missing.
	Dealloc     int64   `json:"dealloc"`
	TotalCalls  int64   `json:"totalCalls"`
	TotalTimeMs float64 `json:"totalTimeMs"`
	// HiddenCalls are other roles' statements whose text this role may not
	// read (it lacks pg_read_all_stats).
	HiddenCalls int64 `json:"hiddenCalls"`
	// UnattributedCalls could not be tied to any table: parse failures,
	// utility statements, tables outside the schemas read, statements whose
	// text pg_stat_statements no longer has.
	UnattributedCalls int64 `json:"unattributedCalls"`
	// UnreadCalls belong to statements beyond the ones read (most time first).
	UnreadCalls int64 `json:"unreadCalls"`
	// Read is how many statements were read.
	Read int `json:"read"`
}

// Table is one relation's activity, keyed "schema.table" in Snapshot.Tables.
type Table struct {
	SeqScans    int64 `json:"seqScans"`
	IdxScans    int64 `json:"idxScans"`
	RowsWritten int64 `json:"rowsWritten"` // n_tup_ins + n_tup_upd + n_tup_del
	LiveRows    int64 `json:"liveRows"`
	// QueryCalls and QueryTimeMs sum the statements that use the table; a
	// statement using three tables counts in each.
	QueryCalls  int64                `json:"queryCalls"`
	QueryTimeMs float64              `json:"queryTimeMs"`
	Columns     map[string]ColumnUse `json:"columns,omitempty"`
	// WildcardCalls are calls of statements that select * from it.
	WildcardCalls int64   `json:"wildcardCalls,omitempty"`
	TopQueries    []Query `json:"topQueries,omitempty"`
}

// ColumnUse counts the statements naming a column.
type ColumnUse struct {
	Queries int   `json:"queries"`
	Calls   int64 `json:"calls"`
}

// Query is one normalized statement.
type Query struct {
	QueryID string  `json:"queryId"`
	Text    string  `json:"text"`
	Calls   int64   `json:"calls"`
	TotalMs float64 `json:"totalMs"`
	MeanMs  float64 `json:"meanMs"`
}

// Limits on what is read and returned.
const (
	maxStatements = 2000
	maxTopQueries = 5
	maxTextLen    = 1000
)

// Read takes a snapshot in one read-only transaction. m names the tables to
// attribute statements to; its schemas bound what is resolved. It fails only
// when the database cannot be read at all: pg_stat_statements problems are
// reported in Statements.
func Read(ctx context.Context, db interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, m *schema.Model, timeout time.Duration) (*Snapshot, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if ms := timeout.Milliseconds(); ms > 0 {
		if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", strconv.FormatInt(ms, 10)); err != nil {
			return nil, err
		}
	}
	s := &Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*Table{}}
	path := Path{Names: map[string]map[string]bool{}}
	err = tx.QueryRow(ctx, `SELECT pg_is_in_recovery(), current_schemas(false)::text[],
		(SELECT pg_stat_get_db_stat_reset_time(oid) FROM pg_database WHERE datname = current_database())`).
		Scan(&s.InRecovery, &path.Schemas, &s.TableStatsReset)
	if err != nil {
		return nil, fmt.Errorf("workload: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT n.nspname, c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY(current_schemas(false)) AND c.relkind IN ('r', 'p', 'v', 'm', 'f')`)
	if err != nil {
		return nil, fmt.Errorf("workload: search path: %w", err)
	}
	var sc, rel string
	if _, err := pgx.ForEachRow(rows, []any{&sc, &rel}, func() error {
		if path.Names[sc] == nil {
			path.Names[sc] = map[string]bool{}
		}
		path.Names[sc][rel] = true
		return nil
	}); err != nil {
		return nil, fmt.Errorf("workload: search path: %w", err)
	}
	rows, err = tx.Query(ctx, `SELECT schemaname, relname, coalesce(seq_scan, 0), coalesce(idx_scan, 0),
		coalesce(n_tup_ins + n_tup_upd + n_tup_del, 0), coalesce(n_live_tup, 0) FROM pg_stat_user_tables`)
	if err != nil {
		return nil, fmt.Errorf("workload: table statistics: %w", err)
	}
	var t Table
	if _, err := pgx.ForEachRow(rows, []any{&sc, &rel, &t.SeqScans, &t.IdxScans, &t.RowsWritten, &t.LiveRows}, func() error {
		row := t
		s.Tables[schema.QualifiedName{Schema: sc, Name: rel}.String()] = &row
		return nil
	}); err != nil {
		return nil, fmt.Errorf("workload: table statistics: %w", err)
	}
	stmts := readStatements(ctx, tx, &s.Statements)
	Attribute(s, m, path, stmts)
	return s, nil
}

// Statement is one pg_stat_statements row.
type Statement struct {
	QueryID string
	Text    string
	// Hidden: another role's statement this role may not read. A visible row
	// can still have no text (dropped by pg_stat_statements' text garbage
	// collection); it is then not tied to a table.
	Hidden  bool
	Calls   int64
	TotalMs float64
}

// minVersion has toplevel and pg_stat_statements_info (PostgreSQL 14).
var minVersion = [2]int{1, 9}

// readStatements fills info and returns the statements, most time first. A
// failure is recorded in info, never returned: tables stats still stand.
func readStatements(ctx context.Context, tx pgx.Tx, info *Statements) []Statement {
	var nsp, version string
	err := tx.QueryRow(ctx, `SELECT n.nspname, e.extversion FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_stat_statements'`).Scan(&nsp, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		info.Status = StatusNotInstalled
		return nil
	}
	if err != nil {
		info.Status, info.Message = StatusUnavailable, err.Error()
		return nil
	}
	var major, minor int
	if _, err := fmt.Sscanf(version, "%d.%d", &major, &minor); err != nil || major < minVersion[0] || (major == minVersion[0] && minor < minVersion[1]) {
		info.Status = StatusUnavailable
		info.Message = fmt.Sprintf("pg_stat_statements is version %s, older than this server's; run ALTER EXTENSION pg_stat_statements UPDATE", version)
		return nil
	}
	// A savepoint, so an error here (e.g. not preloaded) leaves the transaction usable.
	sp, err := tx.Begin(ctx)
	if err != nil {
		info.Status, info.Message = StatusUnavailable, err.Error()
		return nil
	}
	defer sp.Rollback(context.WithoutCancel(ctx))
	ns := pgx.Identifier{nsp}.Sanitize()
	if err := sp.QueryRow(ctx, `SELECT dealloc, stats_reset FROM `+ns+`.pg_stat_statements_info`).Scan(&info.Dealloc, &info.Since); err != nil {
		statementsError(info, err)
		return nil
	}
	// Window totals cover every statement, not only the ones read.
	rows, err := sp.Query(ctx, `SELECT coalesce(queryid::text, ''), coalesce(query, ''), calls, total_exec_time,
		sum(calls) OVER (), sum(total_exec_time) OVER ()
		FROM `+ns+`.pg_stat_statements(true)
		WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND toplevel
		ORDER BY total_exec_time DESC LIMIT `+strconv.Itoa(maxStatements))
	if err != nil {
		statementsError(info, err)
		return nil
	}
	var out []Statement
	var st Statement
	var totalCalls, totalMs float64
	var readCalls int64
	if _, err := pgx.ForEachRow(rows, []any{&st.QueryID, &st.Text, &st.Calls, &st.TotalMs, &totalCalls, &totalMs}, func() error {
		row := st
		row.Hidden = row.QueryID == "" || row.Text == "<insufficient privilege>"
		if row.Hidden {
			info.HiddenCalls += row.Calls
			row.Text = ""
		}
		readCalls += row.Calls
		out = append(out, row)
		return nil
	}); err != nil {
		statementsError(info, err)
		return nil
	}
	info.Status, info.TotalCalls, info.TotalTimeMs, info.Read = StatusOK, int64(totalCalls), totalMs, len(out)
	info.UnreadCalls = max(0, info.TotalCalls-readCalls)
	return out
}

func statementsError(info *Statements, err error) {
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &pe) && pe.Code == "55000":
		info.Status = StatusNotLoaded
	case errors.As(err, &pe):
		info.Status, info.Message = StatusUnavailable, fmt.Sprintf("%s (SQLSTATE %s)", pe.Message, pe.Code)
	default:
		info.Status, info.Message = StatusUnavailable, err.Error()
	}
}

// addTopQuery keeps a table's most time-consuming statements, most first.
func addTopQuery(t *Table, q Query) {
	i, _ := slices.BinarySearchFunc(t.TopQueries, q.TotalMs, func(a Query, ms float64) int {
		switch {
		case a.TotalMs > ms:
			return -1
		case a.TotalMs < ms:
			return 1
		}
		return 0
	})
	if i >= maxTopQueries {
		return
	}
	t.TopQueries = slices.Insert(t.TopQueries, i, q)
	if len(t.TopQueries) > maxTopQueries {
		t.TopQueries = t.TopQueries[:maxTopQueries]
	}
}

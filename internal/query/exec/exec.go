// Package exec runs a guarded SELECT the way docs/QUERY-SAFETY.md
// describes: one REPEATABLE READ READ ONLY transaction with pinned
// settings, a function check against the live catalog, EXPLAIN for the cost
// gate and the plan overlay, a cursor that bounds the rows fetched, and
// always ROLLBACK.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// DB is satisfied by *pgxpool.Pool and *pgx.Conn.
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Request is one run. Parsed must come from guard.Check: Run trusts it.
type Request struct {
	Parsed *guard.Parsed
	// Schemas pin search_path (after pg_catalog). Empty means every
	// non-system schema the role has USAGE on, in name order.
	Schemas          []string
	StatementTimeout time.Duration
	// MaxRows bounds the rows returned; ExplainOnly skips fetching.
	MaxRows     int
	ExplainOnly bool
	// Analyze adds EXPLAIN (ANALYZE, BUFFERS), which executes the query.
	Analyze bool
	// MaxCost is the cost gate; 0 disables it. ConfirmCost runs anyway.
	MaxCost     float64
	ConfirmCost bool
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	TypeOID  uint32 `json:"typeOid"`
	Relation string `json:"relation,omitempty"`
	Column   string `json:"column,omitempty"`
}

type Result struct {
	Columns     []Column            `json:"columns"`
	Rows        [][]json.RawMessage `json:"rows"`
	Truncated   bool                `json:"truncated"`
	RowCount    int                 `json:"rowCount"`
	DurationMs  float64             `json:"durationMs"`
	ExecutedSQL string              `json:"executedSql"`
	Plan        *plan.Node          `json:"plan"`
	Overlay     plan.Overlay        `json:"overlay"`
	Warnings    []string            `json:"warnings"`
}

// ErrConnect marks failures to get a connection or begin the transaction.
var ErrConnect = errors.New("connect")

// CostError is the cost gate refusing a statement.
type CostError struct {
	EstimatedCost float64
	EstimatedRows float64
	Limit         float64
}

func (e *CostError) Error() string {
	return fmt.Sprintf("the estimated cost %.0f is over this profile's limit of %.0f", e.EstimatedCost, e.Limit)
}

// DBError is PostgreSQL rejecting the statement. Position is the 1-based
// character offset into the executed SQL, or 0 when unknown or when the
// error came from one of dbscope's own statements.
type DBError struct {
	Err      *pgconn.PgError
	Position int
}

func (e *DBError) Error() string { return e.Err.Error() }
func (e *DBError) Unwrap() error { return e.Err }

const cursorName = "dbscope_c"

const (
	explainPrefix  = "EXPLAIN (VERBOSE, FORMAT JSON) "
	analyzePrefix  = "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) "
	declarePrefix  = "DECLARE " + cursorName + " NO SCROLL CURSOR FOR "
	rollbackBudget = 5 * time.Second
)

var txOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// nonSystem matches the same schemas as introspect: names starting with
// pg_ are reserved.
const nonSystem = `n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'`

// setupQuery pins the transaction's settings. $3 is the quoted schema list,
// or NULL for every non-system schema with USAGE. transaction_timeout only
// exists from PostgreSQL 17; CASE keeps set_config from running before that.
const setupQuery = `SELECT current_setting('server_version_num')::int,
       set_config('statement_timeout', $1, true),
       set_config('lock_timeout', '2s', true),
       set_config('idle_in_transaction_session_timeout', $2, true),
       set_config('search_path', concat_ws(', ', 'pg_catalog', coalesce($3::text,
           (SELECT string_agg(quote_ident(n.nspname), ', ' ORDER BY n.nspname) FROM pg_namespace n
            WHERE ` + nonSystem + ` AND has_schema_privilege(n.oid, 'USAGE'))), 'pg_temp'), true),
       set_config('DateStyle', 'ISO', true),
       set_config('TimeZone', 'UTC', true),
       set_config('IntervalStyle', 'iso_8601', true),
       set_config('bytea_output', 'hex', true),
       set_config('cursor_tuple_fraction', '1', true),
       CASE WHEN current_setting('server_version_num')::int >= 170000
            THEN set_config('transaction_timeout', $4, true) END`

// Run executes the request and always rolls back.
func Run(ctx context.Context, db DB, req Request) (*Result, error) {
	tx, err := db.BeginTx(ctx, txOptions)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}
	defer func() {
		// ROLLBACK, never COMMIT: a pg_notify would be delivered on commit.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackBudget)
		defer cancel()
		tx.Rollback(rctx)
	}()

	if err := setup(ctx, tx, req); err != nil {
		return nil, err
	}
	if err := guard.AllowFunctions(ctx, tx, req.Parsed.Functions, nil); err != nil {
		return nil, internalErr(err)
	}
	sql := req.Parsed.Deparsed
	res := &Result{ExecutedSQL: sql, Columns: []Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}

	p, err := explain(ctx, tx, explainPrefix, sql)
	if err != nil {
		return nil, err
	}
	gated := !req.ExplainOnly || req.Analyze // plain EXPLAIN executes nothing
	if req.MaxCost > 0 && p.Root.TotalCost > req.MaxCost && gated {
		if !req.ConfirmCost {
			return nil, &CostError{EstimatedCost: p.Root.TotalCost, EstimatedRows: p.Root.PlanRows, Limit: req.MaxCost}
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("Ran with an estimated cost of %.0f, over this profile's limit of %.0f, because you confirmed it.",
			p.Root.TotalCost, req.MaxCost))
	}
	if req.Analyze {
		if p, err = explain(ctx, tx, analyzePrefix, sql); err != nil {
			return nil, err
		}
		if !req.ExplainOnly {
			res.Warnings = append(res.Warnings, "EXPLAIN ANALYZE ran the query a second time to measure it.")
		}
	}
	rels, err := relationInfo(ctx, tx, p.Relations())
	if err != nil {
		return nil, err
	}
	res.Overlay = p.Annotate(rels)
	res.Plan = p.Root
	if req.ExplainOnly {
		return res, nil
	}
	if err := fetch(ctx, tx, sql, req.MaxRows, res); err != nil {
		return nil, err
	}
	return res, nil
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

func setup(ctx context.Context, tx pgx.Tx, req Request) error {
	timeout := req.StatementTimeout
	if timeout < time.Millisecond {
		timeout = time.Millisecond
	}
	// The idle timeout only has to outlast dbscope's own work between
	// statements; it ends the session if the agent dies mid-transaction.
	idle := max(timeout, 5*time.Second)
	// transaction_timeout ends the session (FATAL), so it must fire after
	// the statement timeout: it covers every execution plus slack.
	runs := time.Duration(1)
	if req.Analyze && !req.ExplainOnly {
		runs = 2
	}
	txTimeout := timeout*runs + 5*time.Second
	var path *string
	if len(req.Schemas) > 0 {
		s := ""
		for i, name := range req.Schemas {
			if i > 0 {
				s += ", "
			}
			s += pgx.Identifier{name}.Sanitize()
		}
		path = &s
	}
	rows, err := tx.Query(ctx, setupQuery, pgx.QueryExecModeExec, ms(timeout), ms(idle), path, ms(txTimeout))
	if err != nil {
		return internalErr(err)
	}
	rows.Close()
	return internalErr(rows.Err())
}

// internalErr wraps errors from dbscope's own statements; their positions
// refer to dbscope's SQL, not the user's.
func internalErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return &DBError{Err: pe}
	}
	return err
}

// userErr wraps an error from a statement built as prefix + executed SQL,
// shifting the position onto the executed SQL.
func userErr(err error, prefix string) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	pos := int(pe.Position) - len([]rune(prefix))
	if pos < 1 {
		pos = 0
	}
	return &DBError{Err: pe, Position: pos}
}

// query runs one statement over the extended protocol (QueryExecModeExec:
// the server refuses more than one statement) with text results. Exec is
// not used because pgx sends argument-less Exec calls over the simple
// protocol.
func query(ctx context.Context, tx pgx.Tx, sql string) (pgx.Rows, error) {
	return tx.Query(ctx, sql, pgx.QueryExecModeExec, pgx.QueryResultFormats{pgx.TextFormatCode})
}

func explain(ctx context.Context, tx pgx.Tx, prefix, sql string) (*plan.Plan, error) {
	rows, err := query(ctx, tx, prefix+sql)
	if err != nil {
		return nil, userErr(err, prefix)
	}
	var out []byte
	for rows.Next() {
		out = append(out, rows.RawValues()[0]...)
	}
	if err := rows.Err(); err != nil {
		return nil, userErr(err, prefix)
	}
	return plan.Parse(out)
}

const relationsQuery = `SELECT r.schema || '.' || r.name, coalesce(rn.nspname || '.' || rc.relname, ''),
       coalesce(c.reltuples, -1)::float8
FROM unnest($1::text[], $2::text[]) AS r(schema, name)
JOIN pg_namespace n ON n.nspname = r.schema
JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = r.name
LEFT JOIN pg_class rc ON rc.oid = CASE WHEN c.relispartition THEN pg_partition_root(c.oid) ELSE c.oid END
LEFT JOIN pg_namespace rn ON rn.oid = rc.relnamespace`

// relationInfo maps scanned relations to their partition roots and sizes.
func relationInfo(ctx context.Context, tx pgx.Tx, names []plan.RelName) (map[string]plan.RelInfo, error) {
	out := map[string]plan.RelInfo{}
	if len(names) == 0 {
		return out, nil
	}
	schemas := make([]string, len(names))
	rels := make([]string, len(names))
	for i, n := range names {
		schemas[i], rels[i] = n.Schema, n.Name
	}
	rows, err := tx.Query(ctx, relationsQuery, pgx.QueryExecModeExec, schemas, rels)
	if err != nil {
		return nil, internalErr(err)
	}
	var key string
	var info plan.RelInfo
	_, err = pgx.ForEachRow(rows, []any{&key, &info.Root, &info.Rows}, func() error {
		out[key] = info
		return nil
	})
	return out, internalErr(err)
}

func fetch(ctx context.Context, tx pgx.Tx, sql string, maxRows int, res *Result) error {
	rows, err := query(ctx, tx, declarePrefix+sql)
	if err == nil {
		rows.Close()
		err = rows.Err()
	}
	if err != nil {
		return userErr(err, declarePrefix)
	}
	start := time.Now()
	rows, err = query(ctx, tx, "FETCH FORWARD "+strconv.Itoa(maxRows+1)+" FROM "+cursorName)
	if err != nil {
		return internalErr(err)
	}
	fields := rows.FieldDescriptions()
	for rows.Next() {
		if len(res.Rows) == maxRows {
			res.Truncated = true
			continue
		}
		raw := rows.RawValues()
		row := make([]json.RawMessage, len(raw))
		for i, v := range raw {
			row[i] = Value(fields[i].DataTypeOID, v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		// Errors while fetching come from the user's query; their position
		// refers to the cursor's query text.
		return userErr(err, declarePrefix)
	}
	res.DurationMs = math.Round(float64(time.Since(start).Microseconds())/10) / 100
	res.RowCount = len(res.Rows)
	cols, err := columns(ctx, tx, fields)
	if err != nil {
		return err
	}
	res.Columns = cols
	return nil
}

const columnsQuery = `SELECT f.ord::int, format_type(f.typ, f.typmod),
       coalesce(rn.nspname || '.' || rc.relname, ''), coalesce(a.attname::text, '')
FROM unnest($1::int8[], $2::int4[], $3::int8[], $4::int4[]) WITH ORDINALITY AS f(typ, typmod, rel, att, ord)
LEFT JOIN pg_class c ON c.oid = f.rel::oid
LEFT JOIN pg_class rc ON rc.oid = CASE WHEN c.relispartition THEN pg_partition_root(c.oid) ELSE c.oid END
LEFT JOIN pg_namespace rn ON rn.oid = rc.relnamespace
LEFT JOIN pg_attribute a ON a.attrelid = f.rel::oid AND a.attnum = f.att AND f.att > 0
ORDER BY 1`

// columns names each result column's type and, when it is a plain table
// column, its relation (partitions mapped to their root) and attribute,
// in one catalog query.
func columns(ctx context.Context, tx pgx.Tx, fields []pgconn.FieldDescription) ([]Column, error) {
	out := make([]Column, len(fields))
	if len(fields) == 0 {
		return out, nil
	}
	typs := make([]int64, len(fields))
	mods := make([]int32, len(fields))
	rels := make([]int64, len(fields))
	atts := make([]int32, len(fields))
	for i, f := range fields {
		out[i] = Column{Name: f.Name, TypeOID: f.DataTypeOID}
		typs[i], mods[i], rels[i], atts[i] = int64(f.DataTypeOID), f.TypeModifier, int64(f.TableOID), int32(f.TableAttributeNumber)
	}
	rows, err := tx.Query(ctx, columnsQuery, pgx.QueryExecModeExec, typs, mods, rels, atts)
	if err != nil {
		return nil, internalErr(err)
	}
	var ord int
	var typ, rel, col string
	_, err = pgx.ForEachRow(rows, []any{&ord, &typ, &rel, &col}, func() error {
		c := &out[ord-1]
		c.Type = typ
		if rel != "" && col != "" {
			c.Relation, c.Column = rel, col
		}
		return nil
	})
	return out, internalErr(err)
}

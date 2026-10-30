package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// workloadDays is how far back query_log is read; its own TTL may keep less.
const workloadDays = 7

// maxStatements bounds the (query shape, table) rows read, most time first.
const maxStatements = 2000

// Workload reads system.query_log, which lists the tables each finished
// query used, so statements are attributed without parsing their text.
// Inserts add their written rows to the table's writes. Only reads and
// data changes count, as for PostgreSQL: DDL names its table too. Only initial
// queries count (not the parts a distributed query sends to shards), and
// queries that touch only system tables are left out.
func Workload(ctx context.Context, c *Client, m *schema.Model) (*workload.Snapshot, error) {
	ctx, cancel := detach(ctx)
	defer cancel()
	snap := &workload.Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*workload.Table{},
		Statements: workload.Statements{Status: workload.StatusOK}}
	known := map[string]bool{}
	for _, r := range m.Relations {
		known[r.QName().String()] = true
	}

	var calls uint64
	var totalMs float64
	var since time.Time
	err := c.Conn.QueryRow(ctx, `SELECT count(), toFloat64(sum(query_duration_ms)), min(event_time) FROM system.query_log
		WHERE type = 'QueryFinish' AND is_initial_query AND event_date >= today() - $1 AND query_kind IN ('Select', 'Insert', 'Delete', 'Update')
		  AND notEmpty(arrayFilter(t -> NOT startsWith(t, 'system.') AND NOT startsWith(lower(t), 'information_schema.'), tables))`,
		workloadDays).Scan(&calls, &totalMs, &since)
	if err != nil {
		var ex *ch.Exception
		switch {
		case errors.As(err, &ex) && ex.Code == 60:
			snap.Statements.Status = workload.StatusNotLoaded
			snap.Statements.Message = "system.query_log does not exist: query logging is off (log_queries = 0, or no query_log section in the server config)"
		case errors.As(err, &ex) && ex.Code == 497:
			snap.Statements.Status = workload.StatusUnavailable
			snap.Statements.Message = "The user may not read system.query_log: GRANT SELECT ON system.query_log TO it."
		default:
			snap.Statements.Status, snap.Statements.Message = workload.StatusUnavailable, err.Error()
		}
		return snap, nil
	}
	snap.Statements.TotalCalls, snap.Statements.TotalTimeMs = int64(calls), totalMs
	if calls > 0 {
		s := since.UTC()
		snap.Statements.Since = &s
	}

	rows, err := c.Conn.Query(ctx, `SELECT t, toString(normalized_query_hash), any(query), count(), toFloat64(sum(query_duration_ms)),
			sumIf(written_rows, query_kind = 'Insert')
		FROM system.query_log ARRAY JOIN tables AS t
		WHERE type = 'QueryFinish' AND is_initial_query AND event_date >= today() - $1 AND query_kind IN ('Select', 'Insert', 'Delete', 'Update')
		  AND NOT startsWith(t, 'system.') AND NOT startsWith(lower(t), 'information_schema.')
		GROUP BY t, normalized_query_hash ORDER BY sum(query_duration_ms) DESC LIMIT $2`, workloadDays, maxStatements)
	if err != nil {
		return nil, fmt.Errorf("workload: query_log: %w", err)
	}
	defer rows.Close()
	attributed := map[string]bool{}
	for rows.Next() {
		var table, hash, text string
		var n, written uint64
		var ms float64
		if err := rows.Scan(&table, &hash, &text, &n, &ms, &written); err != nil {
			return nil, err
		}
		snap.Statements.Read++
		// query_log names tables "db.table", the model's own key.
		if !known[table] {
			continue
		}
		attributed[hash] = true
		t := snap.Tables[table]
		if t == nil {
			t = &workload.Table{}
			snap.Tables[table] = t
		}
		t.QueryCalls += int64(n)
		t.QueryTimeMs += ms
		t.RowsWritten += int64(written)
		workload.AddTopQuery(t, workload.Query{QueryID: hash, Text: strings.TrimSpace(text), Calls: int64(n), TotalMs: ms, MeanMs: ms / float64(max(n, 1))})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Calls of shapes tied to no table of the map (other databases, dropped tables).
	var unattributed uint64
	hashes := make([]string, 0, len(attributed))
	for h := range attributed {
		hashes = append(hashes, h)
	}
	if err := c.Conn.QueryRow(ctx, `SELECT count() FROM system.query_log
		WHERE type = 'QueryFinish' AND is_initial_query AND event_date >= today() - $1 AND query_kind IN ('Select', 'Insert', 'Delete', 'Update')
		  AND notEmpty(arrayFilter(t -> NOT startsWith(t, 'system.') AND NOT startsWith(lower(t), 'information_schema.'), tables))
		  AND NOT has($2, toString(normalized_query_hash))`, workloadDays, hashes).Scan(&unattributed); err == nil {
		snap.Statements.UnattributedCalls = int64(unattributed)
	}
	return snap, nil
}

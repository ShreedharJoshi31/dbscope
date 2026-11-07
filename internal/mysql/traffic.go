package mysql

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// readTraffic reads rows read and written per table from the
// performance_schema table the Workload view uses (tableIO), so the analysis
// can tell a busy table. The counters run from server start. When they
// cannot be read it returns a warning instead, and no table counts as busy.
func readTraffic(ctx context.Context, db *sql.DB, m *schema.Model) (*migrate.Traffic, string) {
	if len(m.Schemas) == 0 {
		return nil, ""
	}
	const skipped = "busy tables are not taken into account: "
	tx, err := db.BeginTx(ctx, readOnly)
	if err != nil {
		return nil, skipped + err.Error()
	}
	defer tx.Rollback()
	var on, lowerCase int
	if err := tx.QueryRowContext(ctx, `SELECT @@performance_schema, @@lower_case_table_names`).Scan(&on, &lowerCase); err != nil {
		return nil, skipped + err.Error()
	}
	if on == 0 {
		return nil, skipped + "performance_schema is off on this server"
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(m.Schemas)), ",") + ")"
	args := make([]any, len(m.Schemas))
	for i, s := range m.Schemas {
		args[i] = s
	}
	snap := &workload.Snapshot{Tables: map[string]*workload.Table{}}
	if err := tableIO(ctx, tx, snap, indexRelations(m, lowerCase != 0), in, args); err != nil {
		return nil, skipped + "performance_schema is not readable (" + err.Error() + "); GRANT SELECT ON performance_schema.* allows it"
	}
	t := &migrate.Traffic{Writes: map[string]int64{}, Reads: map[string]int64{},
		WriteUnit: "rows written", ReadUnit: "rows read", Of: "the schemas read"}
	for k, tb := range snap.Tables {
		k = strings.ToLower(k)
		t.Writes[k] += tb.RowsWritten
		t.Reads[k] += tb.SeqScans + tb.IdxScans
		t.TotalWrites += tb.RowsWritten
		t.TotalReads += tb.SeqScans + tb.IdxScans
	}
	var name string
	var uptime int64
	if tx.QueryRowContext(ctx, `SHOW GLOBAL STATUS LIKE 'Uptime'`).Scan(&name, &uptime) == nil && uptime > 0 {
		since := time.Now().Add(-time.Duration(uptime) * time.Second)
		t.Since = &since
	}
	return t, ""
}

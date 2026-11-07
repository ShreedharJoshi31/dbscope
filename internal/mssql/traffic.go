package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// readTraffic reads each table's index usage counters, the ones the Workload
// view uses (tableCounters), so the analysis can tell a busy table. They
// count statements, not rows, since the server started. When they cannot be
// read it returns a warning instead, and no table counts as busy.
func readTraffic(ctx context.Context, db *sql.DB, m *schema.Model, srv Server) (*migrate.Traffic, string) {
	const skipped = "busy tables are not taken into account: "
	if srv.Azure {
		return nil, skipped + "Azure SQL Database's usage counters are not read yet"
	}
	var seesAll int
	if err := db.QueryRowContext(ctx, `SELECT coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE'), 0)
		| coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE'), 0)`).Scan(&seesAll); err != nil {
		return nil, skipped + err.Error()
	}
	if seesAll == 0 {
		perm := "VIEW SERVER PERFORMANCE STATE"
		if srv.Num < 160000 {
			perm = "VIEW SERVER STATE"
		}
		return nil, skipped + "this login may not read index usage statistics (GRANT " + perm + " in master allows it)"
	}
	snap := &workload.Snapshot{Tables: map[string]*workload.Table{}}
	if err := tableCounters(ctx, db, m, snap); err != nil {
		return nil, fmt.Sprintf("%sindex usage: %v", skipped, err)
	}
	t := &migrate.Traffic{Writes: map[string]int64{}, Reads: map[string]int64{},
		WriteUnit: "write statements", ReadUnit: "table and index reads", Of: "the schemas read"}
	for k, tb := range snap.Tables {
		k = strings.ToLower(k)
		t.Writes[k] += tb.RowsWritten
		t.Reads[k] += tb.SeqScans + tb.IdxScans
		t.TotalWrites += tb.RowsWritten
		t.TotalReads += tb.SeqScans + tb.IdxScans
	}
	var upSecs int64
	if db.QueryRowContext(ctx, `SELECT DATEDIFF_BIG(SECOND, sqlserver_start_time, SYSDATETIME()) FROM sys.dm_os_sys_info`).Scan(&upSecs) == nil {
		since := time.Now().Add(-time.Duration(upSecs) * time.Second)
		t.Since = &since
	}
	return t, ""
}

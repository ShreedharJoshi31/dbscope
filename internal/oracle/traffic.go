package oracle

import (
	"context"
	"database/sql"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// readTraffic reads rows written per table from the view the Workload view
// uses (rowsWritten), so the analysis can tell a busy table. Only writes are
// read: Oracle queries never wait for a DDL lock. Each table counts from its
// last statistics gathering, so there is no single start date. When the
// counts cannot be read it returns a warning instead, and no table counts as
// busy.
func readTraffic(ctx context.Context, db *sql.DB, m *schema.Model) (*migrate.Traffic, string) {
	if len(m.Schemas) == 0 {
		return nil, ""
	}
	snap := &workload.Snapshot{Tables: map[string]*workload.Table{}}
	for i := range m.Relations {
		if r := &m.Relations[i]; !r.Stub && r.Kind != schema.KindView {
			snap.Tables[r.QName().String()] = &workload.Table{}
		}
	}
	if _, err := rowsWritten(ctx, db, m, snap); err != nil {
		return nil, "busy tables are not taken into account: TAB_MODIFICATIONS was not read: " + firstLine(err)
	}
	t := &migrate.Traffic{Writes: map[string]int64{}, WriteUnit: "rows written", Of: "the schemas read since each table's statistics were gathered"}
	for k, tb := range snap.Tables {
		t.Writes[k] = tb.RowsWritten
		t.TotalWrites += tb.RowsWritten
	}
	return t, ""
}

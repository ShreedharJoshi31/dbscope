package elastic

import (
	"context"
	"fmt"

	"github.com/dbscope/dbscope/internal/migrate"
)

// readTraffic reads each index's counters from index stats, the ones the
// Workload view uses, so the analysis can tell a busy index: shard searches
// (every copy that ran a query phase), and documents indexed and deleted on
// primaries. They count from when each shard copy started, with no reset
// time. When they cannot be read it returns a warning instead, and no index
// counts as busy.
func readTraffic(ctx context.Context, c *Client, indices map[string]*IndexInfo) (*migrate.Traffic, string) {
	const skipped = "busy indices are not taken into account: "
	stats, err := readIndexStats(ctx, c)
	if err != nil {
		if denied(err) {
			return nil, skipped + fmt.Sprintf(monitorIndexGrant, c.Pattern)
		}
		return nil, skipped + err.Error()
	}
	t := &migrate.Traffic{Writes: map[string]int64{}, Reads: map[string]int64{},
		WriteUnit: "documents indexed and deleted", ReadUnit: "shard searches", Of: "the indices read"}
	for name, st := range stats {
		if indices[name] == nil || system(name, c.Pattern) {
			continue
		}
		calls, _ := st.searches()
		t.Writes[name], t.Reads[name] = st.writes(), calls
		t.TotalWrites += st.writes()
		t.TotalReads += calls
	}
	return t, ""
}

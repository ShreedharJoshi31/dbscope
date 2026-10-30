package clickhouse

import (
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/estimate"
)

// Rates are in MB/s of compressed bytes (system.tables.total_bytes, or the
// column's data_compressed_bytes for MODIFY COLUMN), measured by
// TestEstimateMutations with mutations_sync = 2 on ClickHouse 26.8 in Docker
// on an Apple M2 (8 cores, 4 GB VM, internal SSD), wide parts, three runs at
// 0.5, 1.5 and 5 million rows (15, 45 and 150 MB compressed):
//   - MODIFY COLUMN UInt32 to UInt64 (2, 6, 20 MB): 6, 31, 18; 6, 6, 19; 2, 28, 23 MB/s
//   - UPDATE of every row: 27, 41, 196; 14, 55, 169; 19, 44, 89
//   - DELETE of a tenth of the rows: 36, 61, 87; 17, 38, 54; 67, 37, 79
//   - OPTIMIZE FINAL: 188, 137, 198; 171, 130, 182; 211, 277, 235
//
// A mutation took up to 1.2 s however small its data (scheduling it and
// polling for its end, which depend on the background pool), so the fixed
// part is that tripled. Low rates come from the largest table, where size
// dominates: its slowest run divided by 5 (slower disks, fewer cores, merges
// competing, data that compresses better than the test's). High is the
// fastest rate at any size doubled.
var chRates = map[string]chRate{
	"DBS-CH-COLUMN-REWRITE": {estimate.Rate{LowMBps: 3.6, HighMBps: 62, FixedSeconds: 3.6}, "converting the column in every part"},
	"DBS-CH-UPDATE":         {estimate.Rate{LowMBps: 17, HighMBps: 390, FixedSeconds: 3.6}, "rewriting every part of the table"},
	"DBS-CH-ALTER-DELETE":   {estimate.Rate{LowMBps: 10, HighMBps: 175, FixedSeconds: 3.6}, "rewriting every part of the table"},
	"DBS-CH-OPTIMIZE-FINAL": {estimate.Rate{LowMBps: 36, HighMBps: 555, FixedSeconds: 3.6}, "merging every part of the table"},
}

type chRate struct {
	rate estimate.Rate
	what string
}

// addEstimate adds the duration of one rewrite to the statement's estimate;
// the clauses of one ALTER each rewrite their data. TTL and MATERIALIZE get
// none: their cost depends on the expression being computed.
func addEstimate(out *migrate.Statement, id string, bytes uint64) {
	r, ok := chRates[id]
	if !ok || bytes == 0 {
		return
	}
	e := migrate.Estimate(estimate.Bytes(int64(bytes), r.rate, r.what))
	if out.Estimate != nil {
		e.LowSeconds += out.Estimate.LowSeconds
		e.HighSeconds += out.Estimate.HighSeconds
		e.Basis = out.Estimate.Basis + "; " + e.Basis
	}
	out.Estimate = &e
}

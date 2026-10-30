package mysql

import (
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/estimate"
)

// Rates are in MB/s of the table's data plus index length, measured by
// TestEstimateDDL on MySQL 8.4 and MariaDB 11.8 in Docker on an Apple M2
// (8 cores, 4 GB VM, internal SSD), default 128 MB buffer pool, at 65,536,
// 262,144 and 1,048,576 rows (5 to 135 MB):
//   - ADD INDEX on VARCHAR(64), INPLACE: MySQL 51, 50, 44; MariaDB 86, 83, 69
//   - FORCE, INPLACE rebuild: MySQL 45, 51, 43; MariaDB 87, 95, 79
//   - MODIFY INT to BIGINT, COPY: MySQL 27, 29, 29; MariaDB 78, 60, 47
//
// Low is the slowest rate divided by 5 (slower disks, a buffer pool smaller
// than the table, concurrent load), high the fastest doubled. No fixed
// overhead showed at the smallest size, so none is added.
var (
	copyRate    = estimate.Rate{LowMBps: 5.4, HighMBps: 155}
	inplaceRate = estimate.Rate{LowMBps: 8.5, HighMBps: 190}
	indexRate   = estimate.Rate{LowMBps: 8.5, HighMBps: 170}
)

// ddlEstimate is the duration of a table rebuild or a secondary index build
// on tbl, or nil when its size is unknown or the work is not of a measured
// kind (full-text and spatial indexes build differently).
func ddlEstimate(plan Plan, work string, tbl *TableInfo, kinds []string) *migrate.Estimate {
	if tbl == nil || !tbl.Sized || tbl.Bytes <= 0 || containsAnyKind(kinds, "add fulltext", "add spatial") {
		return nil
	}
	var r estimate.Range
	switch {
	case work == "rewrite" && plan.Algo == Copy:
		r = estimate.Bytes(tbl.Bytes, copyRate, "copying the table row by row (ALGORITHM=COPY)")
	case work == "rewrite":
		r = estimate.Bytes(tbl.Bytes, inplaceRate, "rebuilding the table in place (ALGORITHM=INPLACE)")
	case work == "index_build":
		r = estimate.Bytes(tbl.Bytes, indexRate, "reading the table to build the index")
	default:
		return nil
	}
	e := migrate.Estimate(r)
	return &e
}

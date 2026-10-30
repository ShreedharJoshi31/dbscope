package mongo

import (
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/estimate"
)

// indexRate is in MB/s of the collection's uncompressed data size
// ($collStats storageStats.size), measured by TestEstimateIndexBuild on
// MongoDB 8.0 in Docker on an Apple M2 (8 cores, 4 GB VM, internal SSD), at
// 100,000, 300,000 and 1,000,000 documents (8, 23 and 76 MB):
//   - {s: 1} on a 32-character string: 57, 62, 41 MB/s
//   - {k: 1, n: 1} on two integers: 73, 96, 64 MB/s
//
// Low is the slowest rate divided by 5 (slower disks, a cache smaller than
// the collection, a replica set whose members build at once), high the
// fastest doubled. No fixed overhead showed at the smallest size.
var indexRate = estimate.Rate{LowMBps: 8, HighMBps: 190}

// indexEstimate is the duration of an index build on the collection. One
// createIndexes call builds all its indexes in a single scan, so a call gets
// one estimate, for its first index.
func indexEstimate(info *CollectionInfo) *migrate.Estimate {
	if info == nil || info.Bytes <= 0 {
		return nil
	}
	e := migrate.Estimate(estimate.Bytes(info.Bytes, indexRate, "reading the collection to build the index"))
	return &e
}

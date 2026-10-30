package sqlite

import (
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/estimate"
)

// Rates are in MB/s of the table's pages plus its indexes' (dbstat),
// measured by TestEstimate on the modernc SQLite build dbscope links, on an
// Apple M2 (8 cores, internal SSD, APFS), rollback journal, default page
// cache, at 100,000, 300,000 and 1,000,000 rows (9 to 90 MB):
//   - CREATE INDEX on a 32-character TEXT: 63, 59, 56 MB/s
//   - INSERT INTO new SELECT ... FROM old: 128, 129, 105
//   - DROP COLUMN: 215, 180, 204
//   - UPDATE of every row: 250, 224, 223
//
// Low is the slowest rate divided by 5 (slower disks, a busier machine, a
// file not in the OS cache), high the fastest doubled.
var (
	sqliteIndexRate  = estimate.Rate{LowMBps: 11, HighMBps: 125}
	sqliteCopyRate   = estimate.Rate{LowMBps: 21, HighMBps: 255}
	sqliteDropRate   = estimate.Rate{LowMBps: 36, HighMBps: 430}
	sqliteUpdateRate = estimate.Rate{LowMBps: 44, HighMBps: 500}
)

// sqliteEstimate is the duration of the statement's work on a table of n
// bytes, or nil for work of a kind that was not measured (VACUUM and
// statements whose cost depends on a WHERE or a CHECK).
func sqliteEstimate(st *statement, work string, n int64) *migrate.Estimate {
	if n <= 0 {
		return nil
	}
	var r estimate.Range
	switch {
	case work == "index_build":
		r = estimate.Bytes(n, sqliteIndexRate, "reading the table to build the index")
	case work == "rewrite" && st.kind == "ALTER TABLE DROP COLUMN":
		r = estimate.Bytes(n, sqliteDropRate, "rewriting every row of the table")
	case work == "rewrite" && (st.kind == "INSERT" || st.kind == "REPLACE"):
		r = estimate.Bytes(n, sqliteCopyRate, "copying the table into the new one")
	case work == "rewrite" && st.kind == "UPDATE":
		r = estimate.Bytes(n, sqliteUpdateRate, "updating every row of the table")
	default:
		return nil
	}
	e := migrate.Estimate(r)
	return &e
}

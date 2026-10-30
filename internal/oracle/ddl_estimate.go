package oracle

import (
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/estimate"
)

// Rates are in MB/s of the table's segment size (DBA_SEGMENTS), measured
// by TestDDLPredictions/estimates on Oracle Free 23.26 in Docker on an
// Apple M2 (4 GB VM shared with other containers) at 0.8M, 3.2M and 6.4M
// rows of six narrow columns (35, 151 and 294 MB):
//   - CREATE INDEX on a VARCHAR2: 113, 48, 55; with ONLINE 73, 53, 69
//   - MOVE: 102, 108, 143; MOVE ONLINE 14, 23, 23
//   - DROP COLUMN: 14, 12, 7
//   - ADD CONSTRAINT ... CHECK, validating: 2019, 1263, 807
//
// Low is the slowest rate divided by 5 (slower disks, a buffer cache
// smaller than the table, concurrent load), high the fastest (doubled by
// estimate.Bytes). ADD ... IDENTITY writes every row like DROP COLUMN.
var rates = map[string]estimate.Rate{
	"index":       {LowMBps: 9.6, HighMBps: 113},
	"move":        {LowMBps: 20, HighMBps: 143},
	"move online": {LowMBps: 2.8, HighMBps: 23},
	"update":      {LowMBps: 1.4, HighMBps: 14},
	"scan":        {LowMBps: 160, HighMBps: 2019},
}

// rateFor is the measured kind of work an op does, and what to call it.
var rateFor = map[string][2]string{
	"create index":            {"index", "reading the table to build the index"},
	"create index online":     {"index", "reading the table to build the index online"},
	"rebuild index":           {"index", "reading the table to rebuild the index"},
	"rebuild index online":    {"index", "reading the table to rebuild the index online"},
	"add constraint unique":   {"index", "reading the table to build the constraint's index"},
	"add column index":        {"index", "reading the table to build the constraint's index"},
	"move":                    {"move", "writing the table to new segments (MOVE)"},
	"move online":             {"move online", "writing the table to new segments while logging concurrent changes (MOVE ONLINE)"},
	"drop column":             {"update", "rewriting every row to remove the column"},
	"drop column online":      {"update", "rewriting every row to remove the column"},
	"add identity":            {"update", "writing a value into every row"},
	"add constraint validate": {"scan", "reading every row to validate the constraint"},
	"validate constraint":     {"scan", "reading every row to validate the constraint"},
	"enable validate":         {"scan", "reading every row to validate the constraint"},
	"modify not null":         {"scan", "reading every row to check for NULLs"},
	"modify narrow":           {"scan", "reading every row to check the values fit"},
}

// ddlEstimate is how long p's work on tbl may take, or nil when its size
// is unknown or the work is of a kind that was not measured.
func ddlEstimate(p opPlan, tbl *TableInfo) *migrate.Estimate {
	if tbl == nil || !tbl.Sized || tbl.Bytes <= 0 || p.work == "none" {
		return nil
	}
	k, ok := rateFor[p.op]
	if !ok {
		return nil
	}
	e := migrate.Estimate(estimate.Bytes(tbl.Bytes, rates[k[0]], k[1]))
	return &e
}

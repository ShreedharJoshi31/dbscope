package migrate

import (
	"github.com/dbscope/dbscope/internal/migrate/estimate"
	"github.com/dbscope/dbscope/internal/migrate/rules"
)

// pgRates are per work kind, in MB/s of the size the statement is rated by
// (heap and indexes for a rewrite, heap for a scan or index build).
// Measured by TestEstimatePostgres on PostgreSQL 17 in Docker on an Apple M2
// (8 cores, 4 GB VM, internal SSD) with the table in cache, at 8, 25 and 85 MB
// of heap (1e5, 3e5, 1e6 rows):
//   - ALTER COLUMN TYPE rewrite: 105, 89, 88 MB/s of heap and indexes
//   - CREATE INDEX on text: 38, 104, 86 MB/s; CONCURRENTLY on (int, text): 80, 94, 83
//   - VALIDATE CHECK: 850, 2711, 2772 MB/s; VALIDATE FOREIGN KEY: 1102, 939, 1274
//
// Low is the slowest rate divided by 5 (slower disks, concurrent load), for
// scans by 10 because these read only cached pages (a cold read is bound by
// the disk, e.g. 125 MB/s for an AWS gp3 volume's baseline,
// docs.aws.amazon.com/ebs/latest/userguide/general-purpose.html); high is the
// fastest rate doubled. The fixed part covers the ~0.2 s the smallest index
// builds spent beyond their rate, doubled.
var pgRates = map[rules.Work]estimate.Rate{
	rules.Rewrite:    {LowMBps: 17, HighMBps: 210, FixedSeconds: 0.5},
	rules.IndexBuild: {LowMBps: 7.5, HighMBps: 210, FixedSeconds: 0.5},
	rules.Scan:       {LowMBps: 85, HighMBps: 5500, FixedSeconds: 0.5},
}

// pgEstimated are the rule keys whose work is the measured kind: a rewrite
// of heap and indexes, a btree build reading the heap, or a scan of the heap
// (with a join to the referenced table for foreign keys). Statements whose
// duration depends on a query (DML, materialized views), on dead tuples
// (VACUUM) or on a sort (CLUSTER) get no estimate.
var pgEstimated = map[string]string{
	"alter_type.rewrite":                    "rewriting the table and its indexes",
	"add_column.volatile_default":           "rewriting the table and its indexes",
	"add_column.serial":                     "rewriting the table and its indexes",
	"add_column.identity":                   "rewriting the table and its indexes",
	"add_column.generated_stored":           "rewriting the table and its indexes",
	"add_column.domain":                     "rewriting the table and its indexes",
	"set_tablespace":                        "rewriting the table and its indexes",
	"set_logged":                            "rewriting the table and its indexes",
	"set_access_method":                     "rewriting the table and its indexes",
	"vacuum_full":                           "rewriting the table and its indexes",
	"create_index":                          "building the index from the table",
	"create_index.partitioned":              "building the index from the table",
	"create_index.concurrently":             "building the index from the table",
	"create_index.concurrently.partitioned": "building the index from the table",
	"add_unique":                            "building the index from the table",
	"add_pk":                                "building the index from the table",
	"validate_constraint":                   "scanning the table",
	"validate_constraint.fk":                "scanning the table against the referenced one",
	"add_check":                             "scanning the table",
	"add_fk":                                "scanning the table against the referenced one",
	"set_not_null":                          "scanning the table",
}

// pgEstimate is the duration of a statement's work on n bytes, or nil when
// any part of it that does work is not of a measured kind.
func pgEstimate(eff *rules.Effect, n int64, major int) *Estimate {
	if eff.Work == rules.None || n <= 0 {
		return nil
	}
	what := ""
	for _, k := range eff.Keys {
		r, err := rules.Lookup(k, major)
		if err != nil || r.Work == rules.None {
			continue
		}
		w, ok := pgEstimated[k]
		if !ok {
			return nil
		}
		if r.Work == eff.Work {
			what = w
		}
	}
	if what == "" {
		return nil
	}
	e := Estimate(estimate.Bytes(n, pgRates[eff.Work], what))
	return &e
}

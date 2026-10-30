package clickhouse

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestEstimateMutations runs each estimated mutation, with mutations_sync = 2
// so it returns when every part is rewritten, on tables of three sizes and
// checks the real duration falls inside the estimate made from a live base.
// Its log lines are the measurements behind chRates.
func TestEstimateMutations(t *testing.T) {
	p := mytest.ClickHouse(t, "26.8")
	c, err := Connect(p, mytest.ClickHousePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	exec := func(q string) {
		t.Helper()
		if err := c.Conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, rows := range []int{500_000, 1_500_000, 5_000_000} {
		exec("DROP TABLE IF EXISTS t SYNC")
		exec(`CREATE TABLE t (id UInt64, s String, n UInt32, d Date) ENGINE = MergeTree PARTITION BY toYYYYMM(d) ORDER BY id
			SETTINGS min_bytes_for_wide_part = 0`) // compact parts report no per-column sizes
		for m := 1; m <= 3; m++ {
			exec(fmt.Sprintf(`INSERT INTO t SELECT number, toString(cityHash64(number)), rand(), toDate('2026-0%d-01')
				FROM numbers(%d, %d)`, m, (m-1)*rows/3, rows/3))
		}
		for _, tc := range []struct {
			sql, settings string
			column        string // the size is this column's, else the table's
		}{
			{"ALTER TABLE t MODIFY COLUMN n UInt64", " SETTINGS mutations_sync = 2", "n"},
			{"ALTER TABLE t UPDATE n = n + 1 WHERE 1", " SETTINGS mutations_sync = 2", ""},
			{"ALTER TABLE t DELETE WHERE id % 10 = 0", " SETTINGS mutations_sync = 2", ""},
			{"OPTIMIZE TABLE t FINAL", "", ""},
		} {
			base, err := ReadBase(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			tbl := base.Tables["default.t"]
			size := tbl.Bytes
			if tc.column != "" {
				size = tbl.Columns[tc.column].Bytes
			}
			s := Analyze(base, []ingest.Input{{Name: "m.sql", SQL: tc.sql}}).Statements[0]
			if s.Estimate == nil {
				t.Fatalf("%s: no estimate (%+v)", tc.sql, s.Findings)
			}
			start := time.Now()
			exec(tc.sql + tc.settings)
			checkEstimate(t, fmt.Sprintf("%d rows: %s", rows, tc.sql), int64(size), time.Since(start), s.Estimate)
		}
	}
}

// TestEstimateNone checks which statements get no estimate, and that the
// rewrites of one ALTER add up.
func TestEstimateNone(t *testing.T) {
	base := &Base{Database: "default", Tables: map[string]*TableInfo{"default.t": {Engine: "MergeTree", Rows: 1e7, Bytes: 300e6,
		Columns: map[string]ColumnInfo{"n": {Type: "UInt32", Bytes: 40e6}, "s": {Type: "String", Bytes: 200e6}, "d": {Type: "Date", Bytes: 1e6}}}}}
	est := func(sql string) *migrate.Estimate {
		return Analyze(base, []ingest.Input{{Name: "m.sql", SQL: sql}}).Statements[0].Estimate
	}
	for _, sql := range []string{
		"ALTER TABLE t MODIFY TTL d + INTERVAL 1 YEAR", // cost depends on the TTL expression
		"ALTER TABLE t DROP COLUMN s",                  // replaces parts without reading data
		"ALTER TABLE t MODIFY COLUMN n UInt32 DEFAULT 1",
		"ALTER TABLE other UPDATE n = 1 WHERE 1",
		"OPTIMIZE TABLE t",
	} {
		if e := est(sql); e != nil {
			t.Errorf("%s: estimate %+v, want none", sql, e)
		}
	}
	u, d := est("ALTER TABLE t UPDATE n = 1 WHERE 1"), est("ALTER TABLE t DELETE WHERE n = 1")
	both := est("ALTER TABLE t UPDATE n = 1 WHERE 1, DELETE WHERE n = 1")
	if u == nil || d == nil || both == nil || both.LowSeconds != u.LowSeconds+d.LowSeconds || both.HighSeconds != u.HighSeconds+d.HighSeconds {
		t.Errorf("UPDATE %+v and DELETE %+v do not add up to %+v", u, d, both)
	}
}

// checkEstimate fails when took is outside e and logs the measured rate and
// how far took is from each end of the range.
func checkEstimate(t *testing.T, what string, size int64, took time.Duration, e *migrate.Estimate) {
	t.Helper()
	sec := took.Seconds()
	t.Logf("%s: %.1f MB in %.3fs = %.0f MB/s; estimate %.3f-%.3fs (low end %.1fx below, high end %.1fx above)",
		what, float64(size)/1e6, sec, float64(size)/1e6/sec, e.LowSeconds, e.HighSeconds, sec/e.LowSeconds, e.HighSeconds/sec)
	if sec < e.LowSeconds || sec > e.HighSeconds {
		t.Errorf("%s took %.3fs, outside the estimate %.3f-%.3fs (%s)", what, sec, e.LowSeconds, e.HighSeconds, e.Basis)
	}
}

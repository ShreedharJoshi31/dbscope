package mysql

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestEstimateDDL runs an INPLACE index build, an INPLACE rebuild and a COPY
// rebuild on tables of three sizes and checks each real duration falls
// inside the estimate made from a live base. Its log lines are the
// measurements behind the rates in estimate.go.
func TestEstimateDDL(t *testing.T) {
	for _, flavor := range []string{"mysql", "mariadb"} {
		t.Run(flavor, func(t *testing.T) {
			db, err := Open(mytest.Server(t, flavor), mytest.Password, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			exec := func(q string) {
				t.Helper()
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			for _, doublings := range []int{16, 18, 20} {
				exec("DROP TABLE IF EXISTS t")
				exec("CREATE TABLE t (id INT AUTO_INCREMENT PRIMARY KEY, a INT NOT NULL, b VARCHAR(64) NOT NULL, c INT NOT NULL) ENGINE=InnoDB")
				exec("INSERT INTO t (a, b, c) VALUES (1, MD5(1), 1)")
				for range doublings {
					exec("INSERT INTO t (a, b, c) SELECT id, MD5(id), id % 1000 FROM t")
				}
				for _, q := range []string{
					"ALTER TABLE t ADD INDEX t_b (b)",
					"ALTER TABLE t FORCE",
					"ALTER TABLE t MODIFY a BIGINT NOT NULL",
				} {
					exec("ANALYZE TABLE t")
					base, err := ReadBase(ctx, db, nil, "app")
					if err != nil {
						t.Fatal(err)
					}
					rep := Analyze(base, []ingest.Input{{Name: "m.sql", SQL: q}})
					s := rep.Statements[0]
					if s.Estimate == nil || s.SizeBytes == nil {
						t.Fatalf("%s: no estimate (work %s, findings %v)", q, s.Work, s.Findings)
					}
					start := time.Now()
					exec(q)
					checkEstimate(t, fmt.Sprintf("%d rows: %s", 1<<doublings, q), *s.SizeBytes, time.Since(start), s.Estimate)
				}
			}
			base, err := ReadBase(ctx, db, nil, "app")
			if err != nil {
				t.Fatal(err)
			}
			// Metadata only, a full-text build (not measured), and a table the base does not have.
			for _, q := range []string{"ALTER TABLE t ADD COLUMN z INT", "ALTER TABLE t ADD FULLTEXT INDEX f (b)", "ALTER TABLE missing FORCE"} {
				if s := Analyze(base, []ingest.Input{{Name: "m.sql", SQL: q}}).Statements[0]; s.Estimate != nil {
					t.Errorf("%s: estimate %+v, want none", q, s.Estimate)
				}
			}
		})
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

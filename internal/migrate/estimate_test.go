package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/pgtest"
)

// TestEstimatePostgres runs each kind of work on tables of three sizes (8, 25 and 85 MB of heap) and
// checks the real duration falls inside the estimate made from a live base
// of the same database. Its log lines are the measurements behind pgRates.
func TestEstimatePostgres(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	for _, rows := range []int{100_000, 300_000, 1_000_000} {
		t.Run(fmt.Sprint(rows), func(t *testing.T) {
			d := srv.NewDB(t)
			d.Exec(t, fmt.Sprintf(`CREATE TABLE parent (id int PRIMARY KEY);
				INSERT INTO parent SELECT generate_series(0, 999);
				CREATE TABLE t (id bigint PRIMARY KEY, v int NOT NULL, ref int, s text NOT NULL);
				INSERT INTO t SELECT g, g %% 1000, g %% 1000, md5(g::text) FROM generate_series(1, %d) g;
				ALTER TABLE t ADD CONSTRAINT t_v CHECK (v >= 0) NOT VALID;
				ALTER TABLE t ADD CONSTRAINT t_ref FOREIGN KEY (ref) REFERENCES parent NOT VALID`, rows))
			d.Exec(t, "VACUUM ANALYZE parent")
			d.Exec(t, "VACUUM ANALYZE t")
			for _, sql := range []string{
				"ALTER TABLE t VALIDATE CONSTRAINT t_v",
				"ALTER TABLE t VALIDATE CONSTRAINT t_ref",
				"CREATE INDEX t_s ON t (s)",
				"CREATE INDEX CONCURRENTLY t_v_idx ON t (v, s)",
				"ALTER TABLE t ALTER COLUMN v TYPE bigint",
			} {
				base, err := LiveBase(t.Context(), d.Admin, []string{"public"}, 0)
				if err != nil {
					t.Fatal(err)
				}
				rep, err := Analyze(t.Context(), base, Request{Files: []ingest.Input{{Name: "m.sql", SQL: sql}}, TxMode: ingest.ModeStatement})
				if err != nil {
					t.Fatal(err)
				}
				s := rep.Statements[0]
				if s.Estimate == nil || s.SizeBytes == nil {
					t.Fatalf("%s: no estimate (work %s)", sql, s.Work)
				}
				start := time.Now()
				if _, err := d.Admin.Exec(t.Context(), sql); err != nil {
					t.Fatal(err)
				}
				checkEstimate(t, sql, *s.SizeBytes, time.Since(start), s.Estimate)
			}
			base, err := LiveBase(t.Context(), d.Admin, []string{"public"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Not of a measured kind (a query's cost, catalog only, dead tuples), or on a table this migration made.
			for _, sql := range []string{"UPDATE t SET v = 1", "ALTER TABLE t ADD COLUMN z int", "VACUUM t",
				"CREATE TABLE n (id int); CREATE INDEX ON n (id)"} {
				rep, err := Analyze(t.Context(), base, Request{Files: []ingest.Input{{Name: "m.sql", SQL: sql}}, TxMode: ingest.ModeStatement})
				if err != nil {
					t.Fatal(err)
				}
				if s := rep.Statements[len(rep.Statements)-1]; s.Estimate != nil {
					t.Errorf("%s: estimate %+v, want none", sql, s.Estimate)
				}
			}
		})
	}
}

// checkEstimate fails when took is outside e and logs the measured rate and
// how far took is from each end of the range.
func checkEstimate(t *testing.T, what string, size int64, took time.Duration, e *Estimate) {
	t.Helper()
	sec := took.Seconds()
	t.Logf("%s: %.1f MB in %.3fs = %.0f MB/s; estimate %.3f-%.3fs (low end %.1fx below, high end %.1fx above)",
		what, float64(size)/1e6, sec, float64(size)/1e6/sec, e.LowSeconds, e.HighSeconds, sec/e.LowSeconds, e.HighSeconds/sec)
	if sec < e.LowSeconds || sec > e.HighSeconds {
		t.Errorf("%s took %.3fs, outside the estimate %.3f-%.3fs (%s)", what, sec, e.LowSeconds, e.HighSeconds, e.Basis)
	}
}

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// TestEstimate runs an index build, a full-table UPDATE, a table copy and a
// DROP COLUMN on database files of three sizes and checks each real duration
// falls inside the estimate made from ReadBase. Its log lines are the
// measurements behind the rates in estimate.go.
func TestEstimate(t *testing.T) {
	if testing.Short() {
		t.Skip("writes up to 150 MB of database files")
	}
	if raceDetector {
		t.Skip("the race detector slows the Go SQLite engine about tenfold, so durations cannot be checked")
	}
	ctx := context.Background()
	for _, rows := range []int{100_000, 300_000, 1_000_000} {
		t.Run(fmt.Sprint(rows), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.db")
			w, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			w.SetMaxOpenConns(1)
			exec := func(q string) {
				t.Helper()
				if _, err := w.ExecContext(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			exec("CREATE TABLE t (id INTEGER PRIMARY KEY, a INT NOT NULL, b TEXT NOT NULL, c INT NOT NULL)")
			exec("CREATE TABLE t2 (id INTEGER PRIMARY KEY, a INT NOT NULL, b TEXT NOT NULL, c INT NOT NULL)")
			exec(fmt.Sprintf(`INSERT INTO t (a, b, c) WITH RECURSIVE g(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM g WHERE x < %d)
				SELECT x, hex(randomblob(16)), x %% 1000 FROM g`, rows))
			for _, q := range []string{
				"CREATE INDEX t_b ON t (b)",
				"UPDATE t SET a = a + 1",
				"INSERT INTO t2 (id, a, b, c) SELECT id, a, b, c FROM t",
				"ALTER TABLE t DROP COLUMN c",
			} {
				r, err := Open(path, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				base, err := ReadBase(ctx, r.DB)
				r.Close()
				if err != nil {
					t.Fatal(err)
				}
				rep, err := Analyze(ctx, base, []ingest.Input{{Name: "m.sql", SQL: q}})
				if err != nil {
					t.Fatal(err)
				}
				s := rep.Statements[0]
				if s.Estimate == nil {
					t.Fatalf("%s: no estimate (work %s, base bytes %v, warnings %v)", q, s.Work, base.Bytes, rep.Warnings)
				}
				start := time.Now()
				exec(q)
				checkEstimate(t, q, base.Bytes["t"], time.Since(start), s.Estimate)
			}
		})
	}
}

// TestEstimateNone checks which statements get no estimate: those on a table
// of unknown byte size, and work whose cost was not measured.
func TestEstimateNone(t *testing.T) {
	ctx := context.Background()
	unsized := analyzeBase()
	sized := analyzeBase()
	sized.Bytes = map[string]int64{"users": 200e6, "orders": 5e6}
	for _, c := range []struct {
		base *Base
		sql  string
		want bool
	}{
		{sized, "CREATE INDEX users_email ON users (email)", true},
		{unsized, "CREATE INDEX users_email ON users (email)", false},
		{sized, "VACUUM", false},
		{sized, "UPDATE users SET name = 'x' WHERE id < 10", false},
		{sized, "DELETE FROM users", false},
		{sized, "CREATE TABLE n (x); CREATE INDEX n_x ON n (x)", false},
	} {
		rep, err := Analyze(ctx, c.base, []ingest.Input{{Name: "m.sql", SQL: c.sql}})
		if err != nil {
			t.Fatal(err)
		}
		if s := rep.Statements[len(rep.Statements)-1]; (s.Estimate != nil) != c.want {
			t.Errorf("%s: estimate %+v, want one: %v", c.sql, s.Estimate, c.want)
		}
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

package clickhouse

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestRewriteLive runs each rewrite on real servers: it must leave the same
// rows and columns as the original once its mutation is done, and drop the
// finding it fixes without a higher rating.
func TestRewriteLive(t *testing.T) {
	const sync = " SETTINGS mutations_sync = 2"
	cases := []struct{ stmt, want, fixes string }{
		{"ALTER TABLE t DELETE WHERE id < 100", "DELETE FROM t WHERE id < 100", "DBS-CH-ALTER-DELETE"},
		{"ALTER TABLE `t`\n  DELETE WHERE n % 2 = 0", "DELETE FROM `t` WHERE n % 2 = 0", "DBS-CH-ALTER-DELETE"},
		{"ALTER TABLE tp DELETE WHERE id < 100", "ALTER TABLE tp DELETE WHERE id < 100" + sync, "DBS-CH-ASYNC-MUTATION"},
		{"ALTER TABLE t DELETE WHERE id < 100, UPDATE n = 2 WHERE 1", "ALTER TABLE t DELETE WHERE id < 100, UPDATE n = 2 WHERE 1" + sync, "DBS-CH-ASYNC-MUTATION"},
		{"ALTER TABLE t UPDATE n = 1 WHERE id < 10", "ALTER TABLE t UPDATE n = 1 WHERE id < 10" + sync, "DBS-CH-ASYNC-MUTATION"},
		{"ALTER TABLE t MODIFY COLUMN n UInt64", "ALTER TABLE t MODIFY COLUMN n UInt64" + sync, "DBS-CH-ASYNC-MUTATION"},
		{"ALTER TABLE t DROP COLUMN z", "ALTER TABLE t DROP COLUMN z" + sync, "DBS-CH-ASYNC-MUTATION"},
		{"ALTER TABLE t ADD COLUMN x UInt8", "", ""},
		{"ALTER TABLE t MODIFY COLUMN n UInt64" + sync, "", ""},
		{"ALTER TABLE t DROP COLUMN id", "", ""},
		{"DELETE FROM t WHERE id < 100", "", ""},
	}
	for _, name := range []string{"26.8", "25.8"} {
		t.Run(name, func(t *testing.T) {
			p := mytest.ClickHouse(t, name)
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
			seed := func() {
				t.Helper()
				exec("DROP TABLE IF EXISTS t SYNC")
				exec("DROP TABLE IF EXISTS tp SYNC")
				exec(`CREATE TABLE t (id UInt64, s String, n UInt32, d Date, z UInt8 DEFAULT 0) ENGINE = MergeTree PARTITION BY toYYYYMM(d) ORDER BY id`)
				exec(`CREATE TABLE tp (id UInt64, n UInt32, PROJECTION by_n (SELECT * ORDER BY n)) ENGINE = MergeTree ORDER BY id`)
				for i := 1; i <= 3; i++ {
					exec(fmt.Sprintf("INSERT INTO t SELECT number + %d, toString(number), number, toDate('2026-0%d-01'), 0 FROM numbers(1000)", i*1000, i))
				}
				exec("INSERT INTO tp SELECT number, number FROM numbers(1000)")
			}
			state := func() string {
				t.Helper()
				deadline := time.Now().Add(time.Minute)
				for {
					var n uint64
					if err := c.Conn.QueryRow(ctx, "SELECT count() FROM system.mutations WHERE database = currentDatabase() AND NOT is_done").Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("mutations did not finish")
					}
					time.Sleep(200 * time.Millisecond)
				}
				var out string
				for _, tbl := range []string{"t", "tp"} {
					var rows, hash uint64
					var ddl string
					if err := c.Conn.QueryRow(ctx, "SELECT count(), sum(cityHash64(*)) FROM "+tbl).Scan(&rows, &hash); err != nil {
						t.Fatal(err)
					}
					if err := c.Conn.QueryRow(ctx, "SHOW CREATE TABLE "+tbl).Scan(&ddl); err != nil {
						t.Fatal(err)
					}
					out += fmt.Sprintf("%s rows=%d hash=%d\n%s\n", tbl, rows, hash, ddl)
				}
				return out
			}
			seed()
			if err := c.Conn.Exec(ctx, "DELETE FROM tp WHERE id < 100"); err == nil {
				t.Error("lightweight DELETE ran on a table with a projection, so the rewrite could be offered there")
			}
			for _, tc := range cases {
				t.Run(tc.stmt, func(t *testing.T) {
					seed()
					base, err := ReadBase(ctx, c)
					if err != nil {
						t.Fatal(err)
					}
					analyze := func(q string) migrate.Statement {
						return Analyze(base, []ingest.Input{{Name: "m.sql", SQL: q}}).Statements[0]
					}
					st := analyze(tc.stmt)
					got := ""
					if st.Rewrite != nil {
						got = st.Rewrite.SQL
					}
					if got != tc.want {
						t.Fatalf("rewrite\n got %q\nwant %q\nfindings %+v", got, tc.want, st.Findings)
					}
					if got == "" {
						return
					}
					after := analyze(got)
					has := func(s migrate.Statement, id string) bool {
						return slices.ContainsFunc(s.Findings, func(f migrate.Finding) bool { return f.RuleID == id })
					}
					if after.Rating.Rank() > st.Rating.Rank() || !has(st, tc.fixes) || has(after, tc.fixes) || has(after, "DBS-CH-ASYNC-MUTATION") {
						t.Errorf("rewrite rated %s %+v; original %s %+v", after.Rating, after.Findings, st.Rating, st.Findings)
					}
					t.Logf("%s %d findings -> %s %d findings", st.Rating, len(st.Findings), after.Rating, len(after.Findings))
					exec(tc.stmt)
					want := state()
					seed()
					exec(got)
					if g := state(); g != want {
						t.Errorf("state after rewrite:\n%s\nafter original:\n%s", g, want)
					}
				})
			}
		})
	}
}

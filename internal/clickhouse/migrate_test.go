package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

func TestSplitScript(t *testing.T) {
	ps := splitScript("-- a; comment\nCREATE TABLE t (s String DEFAULT ';') ENGINE = Log;\n\n  ALTER TABLE t\n  DROP COLUMN s;  ")
	if len(ps) != 2 || ps[0].Line != 2 || ps[1].Line != 4 || ps[1].EndLine != 5 || !strings.HasPrefix(ps[1].SQL, "ALTER") {
		t.Errorf("pieces %+v", ps)
	}
}

func hasRule(st migrate.Statement, id string) bool {
	for _, f := range st.Findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func TestAnalyzeWithoutBase(t *testing.T) {
	rep := Analyze(&Base{Database: "app", Tables: map[string]*TableInfo{}}, []ingest.Input{{Name: "001.sql", SQL: `
ALTER TABLE events ADD COLUMN source LowCardinality(String) DEFAULT '', MODIFY COLUMN n DEFAULT 5;
ALTER TABLE events MODIFY COLUMN n UInt64 SETTINGS mutations_sync = 2;
CREATE MATERIALIZED VIEW daily TO daily_t POPULATE AS SELECT 1;
ALTER TABLE events DROP PARTITION 202601;
DELETE FROM events WHERE id = 1;`}})
	want := []struct {
		kind   string
		rating score.Rating
		rule   string
	}{
		{"ALTER TABLE", score.Low, ""}, {"ALTER TABLE", score.Medium, "DBS-CH-COLUMN-REWRITE"},
		{"CREATE MATERIALIZED VIEW", score.High, "DBS-CH-POPULATE"}, {"ALTER TABLE", score.High, "DBS-CH-DROPS-DATA"},
		{"DELETE", score.Medium, "DBS-CH-LIGHTWEIGHT-DELETE"},
	}
	if len(rep.Statements) != len(want) || rep.Base != "none" || rep.Rating != score.High {
		t.Fatalf("report %+v", rep)
	}
	for i, w := range want {
		st := rep.Statements[i]
		if st.Kind != w.kind || st.Rating != w.rating || w.rule != "" && !hasRule(st, w.rule) {
			t.Errorf("statement %d %s: %s %s %+v", i, st.SQL, st.Kind, st.Rating, st.Findings)
		}
	}
	if hasRule(rep.Statements[1], "DBS-CH-ASYNC-MUTATION") || len(rep.Diff.Columns) != 2 || rep.Diff.Added[0] != "app.daily" {
		t.Errorf("mutations_sync or diff %+v %+v", rep.Statements[1].Findings, rep.Diff)
	}
}

// TestMigratePredictions runs each ALTER against real servers and checks
// what dbscope predicted: whether a mutation starts and whether the server
// refuses the statement.
func TestMigratePredictions(t *testing.T) {
	cases := []struct {
		clause         string
		mutation, fail bool
	}{
		{"ADD COLUMN x UInt8 DEFAULT 0", false, false},
		{"ADD INDEX ix n TYPE minmax GRANULARITY 1", false, false},
		{"ADD PROJECTION p (SELECT * ORDER BY n)", false, false},
		{"MODIFY COLUMN n DEFAULT 5", false, false},
		{"MODIFY COLUMN n UInt32 DEFAULT 5", false, false},
		{"MODIFY COLUMN s COMMENT 'c'", false, false},
		{"MODIFY COLUMN n UInt64", true, false},
		{"MODIFY COLUMN s LowCardinality(String)", true, false},
		{"MODIFY COLUMN id UInt32", false, true},
		{"DROP COLUMN z", true, false},
		{"DROP COLUMN id", false, true},
		{"RENAME COLUMN s TO s2", true, false},
		{"RENAME COLUMN id TO id2", false, true},
		{"RENAME COLUMN d TO d2", false, true},
		{"MODIFY TTL d + INTERVAL 10 YEAR", true, false},
		{"UPDATE n = 1 WHERE 1", true, false},
		{"DELETE WHERE id = 1", true, false},
		{"MATERIALIZE COLUMN z", true, false},
		{"CLEAR COLUMN n IN PARTITION 202602", true, false},
		{"DROP PARTITION 202601", false, false},
		{"MODIFY SETTING merge_with_ttl_timeout = 3600", false, false},
		{"ADD COLUMN y UInt8, MODIFY ORDER BY (id, y)", false, false},
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
			mutations := func() uint64 {
				var n uint64
				c.Conn.QueryRow(ctx, "SELECT count() FROM system.mutations WHERE database = currentDatabase() AND table = 't'").Scan(&n)
				return n
			}
			for _, tc := range cases {
				c.Conn.Exec(ctx, "DROP TABLE IF EXISTS t SYNC")
				if err := c.Conn.Exec(ctx, `CREATE TABLE t (id UInt64, s String, n UInt32, d Date, z UInt8 DEFAULT 0)
					ENGINE = MergeTree PARTITION BY toYYYYMM(d) ORDER BY id`); err != nil {
					t.Fatal(err)
				}
				for i := 1; i <= 3; i++ {
					c.Conn.Exec(ctx, fmt.Sprintf("INSERT INTO t SELECT number, toString(number), number, toDate('2026-0%d-01'), 0 FROM numbers(1000)", i))
				}
				base, err := ReadBase(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				st := Analyze(base, []ingest.Input{{Name: "m.sql", SQL: "ALTER TABLE t " + tc.clause}}).Statements[0]
				predictedMutation, predictedFail := hasRule(st, "DBS-CH-ASYNC-MUTATION"), hasRule(st, "DBS-CH-KEY-COLUMN")
				before := mutations()
				err = c.Conn.Exec(ctx, "ALTER TABLE t "+tc.clause+" SETTINGS mutations_sync = 2")
				gotMutation, gotFail := mutations() > before, err != nil
				if gotMutation != tc.mutation || gotFail != tc.fail {
					t.Errorf("%s: the server did mutation=%v fail=%v (%v); the case expects %v %v", tc.clause, gotMutation, gotFail, err, tc.mutation, tc.fail)
				}
				if predictedMutation != tc.mutation || predictedFail != tc.fail {
					t.Errorf("%s: dbscope predicted mutation=%v fail=%v; the server did %v %v (%+v)", tc.clause, predictedMutation, predictedFail, gotMutation, gotFail, st.Findings)
				}
			}
		})
	}
}

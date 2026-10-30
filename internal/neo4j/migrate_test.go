package neo4j

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

func TestSplitCypher(t *testing.T) {
	ps := splitCypher("// header\nCREATE INDEX a FOR (n:A) ON (n.x);\n\nMATCH (n) WHERE n.s = 'a;b' // c;\nSET n.y = 1;  ")
	if len(ps) != 2 || ps[0].line != 2 || ps[1].line != 4 || ps[1].endLine != 5 || !strings.HasPrefix(ps[1].Text, "MATCH") || !strings.HasSuffix(ps[1].Text, "1") {
		t.Fatalf("pieces %+v", ps)
	}
}

// TestMigratePredictions runs each statement for real after predicting it,
// on 5.26 and 2026 Community and 2026 Enterprise with a small transaction
// memory limit: a statement predicted to fail must fail, and one predicted
// to run must run.
func TestMigratePredictions(t *testing.T) {
	for _, name := range []string{"5", "2026", "enterprise"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testPredictions(t, name)
		})
	}
}

func testPredictions(t *testing.T, name string) {
	p := mytest.Neo4j(t, name, "NEO4J_db_memory_transaction_max=8m")
	ctx := context.Background()
	c, err := Connect(p, mytest.Neo4jPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	run := func(q string) error {
		s := c.Driver.NewSession(ctx, sessionConfig(c.Database))
		defer s.Close(ctx)
		r, err := s.Run(ctx, q, nil)
		if err == nil {
			_, err = r.Consume(ctx)
		}
		return err
	}
	for _, q := range []string{
		"UNWIND range(1, 30) AS i CREATE (:User {id: i, email: 'u' + i + '@x.io', age: 20 + i, name: CASE WHEN i % 10 = 0 THEN null ELSE 'U' + i END})",
		"MATCH (u:User) WHERE u.id IN [1, 2] SET u.email = 'dup@x.io'",
		"CREATE INDEX user_email_idx FOR (u:User) ON (u.email)",
	} {
		if err := run(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// 150,000 accounts in transactions small enough for the 8 MB limit.
	for i := 0; i < 75; i++ {
		if err := run(fmt.Sprintf("UNWIND range(%d, %d) AS i CREATE (:Account {n: i})", i*2000+1, (i+1)*2000)); err != nil {
			t.Fatal(err)
		}
	}
	community := name != "enterprise"
	cases := []struct {
		stmt    string
		fails   bool
		cleanup string
		rule    string
	}{
		{"CREATE CONSTRAINT user_email FOR (u:User) REQUIRE u.email IS UNIQUE", true, "", "DBS-NEO4J-FAILS"},
		{"CREATE CONSTRAINT user_id FOR (u:User) REQUIRE u.id IS UNIQUE", false, "DROP CONSTRAINT user_id", "DBS-NEO4J-CONSTRAINT-SCAN"},
		{"CREATE CONSTRAINT user_name FOR (u:User) REQUIRE u.name IS NOT NULL", true, "", "DBS-NEO4J-FAILS"},
		{"CREATE CONSTRAINT user_id_exists FOR (u:User) REQUIRE u.id IS NOT NULL", community, "DROP CONSTRAINT user_id_exists IF EXISTS", ""},
		{"CREATE CONSTRAINT user_key FOR (u:User) REQUIRE (u.id) IS NODE KEY", community, "DROP CONSTRAINT user_key IF EXISTS", ""},
		{"CREATE CONSTRAINT user_age FOR (u:User) REQUIRE u.age IS :: INTEGER", community, "DROP CONSTRAINT user_age IF EXISTS", ""},
		{"CREATE INDEX user_email_idx FOR (u:User) ON (u.email)", true, "", "DBS-NEO4J-FAILS"},
		{"CREATE INDEX user_email_idx IF NOT EXISTS FOR (u:User) ON (u.email)", false, "", "DBS-NEO4J-INDEX-POPULATING"},
		{"DROP INDEX nope", true, "", "DBS-NEO4J-FAILS"},
		{"DROP INDEX nope IF EXISTS", false, "", ""},
		{"MATCH (a:Account) SET a.flag = true", true, "", "DBS-NEO4J-UNBATCHED"},
		{"MATCH (a:Account) CALL (a) { SET a.flag = true } IN TRANSACTIONS OF 5000 ROWS", false, "", ""},
	}
	for _, tc := range cases {
		base, err := ReadBase(ctx, c, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		rep := Analyze(ctx, base, []ingest.Input{{Name: "V1__x.cypher", SQL: tc.stmt}})
		st := rep.Statements[0]
		predicted := false
		rules := map[string]bool{}
		for _, f := range st.Findings {
			rules[f.RuleID] = true
			predicted = predicted || f.RuleID == "DBS-NEO4J-FAILS"
		}
		// An unbatched write past the memory limit is predicted High, not as a refusal.
		if tc.rule == "DBS-NEO4J-UNBATCHED" {
			predicted = rules["DBS-NEO4J-UNBATCHED"] && st.Rating == score.High
		}
		err = run(tc.stmt)
		if predicted != tc.fails || (err != nil) != tc.fails {
			t.Errorf("%s\n  predicted fail %v, expected %v, ran with error %v; findings %+v", tc.stmt, predicted, tc.fails, err, st.Findings)
		}
		if tc.rule != "" && !rules[tc.rule] {
			t.Errorf("%s: no %s in %+v", tc.stmt, tc.rule, st.Findings)
		}
		if tc.rule == "DBS-NEO4J-UNBATCHED" && (err == nil || !strings.Contains(strings.ToLower(err.Error()), "memory")) {
			t.Errorf("unbatched write: %v", err)
		}
		if tc.cleanup != "" && err == nil {
			if err := run(tc.cleanup); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A destructive statement is rated High whatever its size.
	base, _ := ReadBase(ctx, c, 30*time.Second)
	rep := Analyze(ctx, base, []ingest.Input{{Name: "V2.cypher", SQL: "MATCH (u:User) WHERE u.age > 40 DETACH DELETE u;\nMATCH (n) RETURN count(n)"}})
	if len(rep.Statements) != 2 || rep.Statements[0].Rating != score.High || rep.Statements[0].Findings[0].RuleID != "DBS-NEO4J-DROPS-DATA" ||
		rep.Statements[1].Rating != score.Low || rep.Statements[1].Line != 2 || rep.Rating != score.High {
		t.Errorf("report %+v", rep.Statements)
	}
	if base.TxMemoryMax == "" {
		t.Error("transaction memory limit not read")
	}
}

func TestPlural(t *testing.T) {
	if plural(1, "value") != "1 value" || plural(3, "value") != "3 values" || plural(2, "User entry") != "2 User entries" {
		t.Error(plural(3, "value"), plural(2, "User entry"))
	}
}

// A zero db.memory.transaction.max means no limit, not a zero-byte one.
func TestUnbatchedLimitWords(t *testing.T) {
	for limit, want := range map[string]string{
		"0B":  "nothing caps it here",
		"8m":  "(db.memory.transaction.max is 8m here)",
		"":    "if one is set",
		"0":   "nothing caps it here",
		"10k": "(db.memory.transaction.max is 10k here)",
	} {
		a := &cypherAnalysis{base: &MigrationBase{Labels: map[string]int64{"Person": 30000}, Types: map[string]int64{}, TxMemoryMax: limit}}
		st := a.statement(context.Background(), "1.cypher", splitCypher("MATCH (p:Person) SET p.active = true;")[0])
		msg := ""
		for _, f := range st.Findings {
			if f.RuleID == "DBS-NEO4J-UNBATCHED" {
				msg = f.Message
			}
		}
		if !strings.Contains(msg, want) {
			t.Errorf("limit %q: %q, want it to contain %q", limit, msg, want)
		}
		if len(st.Relations) != 1 || st.Relations[0] != "Person" {
			t.Errorf("relations %v", st.Relations)
		}
	}
}

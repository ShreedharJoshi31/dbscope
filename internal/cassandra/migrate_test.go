package cassandra

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

func hasRule(st migrate.Statement, id string) bool {
	for _, f := range st.Findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func TestSplitCQL(t *testing.T) {
	ps := splitCQL("-- x; y\nCREATE FUNCTION f() RETURNS NULL ON NULL INPUT RETURNS int LANGUAGE java AS $$ return 1; $$;\n\nALTER TABLE t\n ADD 'a;b' int;")
	if len(ps) != 2 || ps[0].Line != 2 || ps[1].Line != 4 || ps[1].EndLine != 5 {
		t.Errorf("pieces %+v", ps)
	}
}

func TestColumnLists(t *testing.T) {
	if got := columnTypes(tokens("(a int, \"B\" frozen<map<text, int>>)")); len(got) != 2 || got[1][0] != "B" || got[1][1] != "frozen<map<text,int>>" {
		t.Errorf("types %v", got)
	}
	if got := columnList(tokens("(a, b)")); strings.Join(got, ",") != "a,b" {
		t.Errorf("drop list %v", got)
	}
}

func TestAnalyzeCQLWithoutSizes(t *testing.T) {
	base := &Base{Server: Server{Flavor: "cassandra"}, Keyspace: "app", Tablets: map[string]bool{},
		Tables: map[string]*TableInfo{"app.users": {KeyColumns: map[string]bool{"id": true}, Dropped: map[string]string{"nick": "text"}}}}
	rep := Analyze(base, []ingest.Input{{Name: "001.cql", SQL: `
ALTER TABLE users ADD nick int;
ALTER TABLE users ADD (age int, tags set<text>);
CREATE INDEX ON users (age);
ALTER KEYSPACE app WITH replication = {'class': 'NetworkTopologyStrategy', 'dc1': 3};
ALTER TABLE users WITH gc_grace_seconds = 0;`}})
	want := []struct {
		rating score.Rating
		rule   string
	}{{score.High, "DBS-CQL-FAILS"}, {score.Low, ""}, {score.Medium, "DBS-CQL-INDEX-BUILD"}, {score.High, "DBS-CQL-REPLICATION"}, {score.High, "DBS-CQL-GC-GRACE-ZERO"}}
	if len(rep.Statements) != len(want) {
		t.Fatalf("statements %+v", rep.Statements)
	}
	for i, w := range want {
		if st := rep.Statements[i]; st.Rating != w.rating || st.Score != map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8}[w.rating] ||
			w.rule != "" && !hasRule(st, w.rule) {
			t.Errorf("%s: %s %+v", st.SQL, st.Rating, st.Findings)
		}
	}
	if len(rep.Diff.Columns) != 3 {
		t.Errorf("diff %+v", rep.Diff.Columns)
	}
}

// TestMigratePredictions checks dbscope's "this fails" against what
// Cassandra and ScyllaDB (with and without tablets) actually refuse.
func TestMigratePredictions(t *testing.T) {
	cases := []struct {
		setup []string
		stmt  string
	}{
		{nil, "ALTER TABLE t ALTER v TYPE bigint"},
		{nil, "ALTER TABLE t RENAME v TO v2"},
		{nil, "ALTER TABLE t RENAME ck TO ck2"},
		{nil, "ALTER TABLE t DROP ck"},
		{nil, "ALTER TABLE t DROP w"},
		{[]string{"ALTER TABLE t DROP w"}, "ALTER TABLE t ADD w int"},
		{[]string{"ALTER TABLE t DROP w"}, "ALTER TABLE t ADD w text"},
		{nil, "ALTER TABLE t ADD (x int, y text)"},
		{nil, "CREATE INDEX ON t (v)"},
		{nil, "CREATE CUSTOM INDEX ON t (w) USING 'StorageAttachedIndex'"},
		{nil, "CREATE MATERIALIZED VIEW mv AS SELECT * FROM t WHERE pk IS NOT NULL AND ck IS NOT NULL AND v IS NOT NULL PRIMARY KEY (v, pk, ck)"},
		{nil, "ALTER TABLE t WITH compaction = {'class': 'LeveledCompactionStrategy'}"},
		{nil, "ALTER TABLE t WITH default_time_to_live = 86400"},
		{nil, "TRUNCATE t"},
	}
	for _, flavor := range []string{"cassandra", "scylla"} {
		t.Run(flavor, func(t *testing.T) {
			p := mytest.Cassandra(t, flavor)
			p.Database = ""
			s, err := Connect(p, mytest.CassandraPassword)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			keyspaces := map[string]string{"vks": ""}
			if flavor == "scylla" {
				keyspaces = map[string]string{"vks": " AND tablets = {'enabled': false}", "tks": " AND tablets = {'enabled': true}"}
			}
			for ks, with := range keyspaces {
				if err := s.Query("CREATE KEYSPACE " + ks + " WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': 1}" + with).Exec(); err != nil {
					t.Fatal(err)
				}
				for _, tc := range cases {
					s.Query("DROP MATERIALIZED VIEW IF EXISTS " + ks + ".mv").Exec()
					s.Query("DROP TABLE IF EXISTS " + ks + ".t").Exec()
					if err := s.Query("CREATE TABLE " + ks + ".t (pk int, ck int, v int, w text, PRIMARY KEY (pk, ck))").Exec(); err != nil {
						t.Fatal(err)
					}
					for _, q := range tc.setup {
						if err := s.Query(strings.Replace(q, "TABLE t", "TABLE "+ks+".t", 1)).Exec(); err != nil {
							t.Fatal(q, err)
						}
					}
					base, err := ReadBase(ctx, s, ks)
					if err != nil {
						t.Fatal(err)
					}
					st := Analyze(base, []ingest.Input{{Name: "m.cql", SQL: tc.stmt}}).Statements[0]
					conn, _ := Connect(withKeyspace(p, ks), mytest.CassandraPassword)
					err = conn.Query(tc.stmt).Exec()
					conn.Close()
					if predicted := hasRule(st, "DBS-CQL-FAILS"); predicted != (err != nil) {
						t.Errorf("%s %s: dbscope predicted fails=%v; the server said %v (%+v)", ks, tc.stmt, predicted, err, st.Findings)
					}
				}
			}
		})
	}
}

func withKeyspace(p conn.Profile, ks string) conn.Profile {
	p.Database = ks
	return p
}

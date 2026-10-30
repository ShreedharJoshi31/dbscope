package cassandra

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestParseMiB(t *testing.T) {
	for in, want := range map[string]float64{"2MiB": 2, "100MiB": 100, "1GiB": 1024, "512KiB": 0.5, "1048576B": 1} {
		if got, ok := parseMiB(in); !ok || got != want {
			t.Errorf("parseMiB(%q) = %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "null", "100", "xMiB"} {
		if _, ok := parseMiB(in); ok {
			t.Errorf("parseMiB(%q) parsed", in)
		}
	}
}

// TestFindings seeds one partition of 3,000 rows of 1 KiB (about 3 MiB) on
// servers whose large partition threshold is lowered to 2 MiB, since the
// defaults (100 MiB on Cassandra, 1000 MB on ScyllaDB) are too big to seed,
// and on Cassandra reads a partition through 1,500 deleted rows.
func TestFindings(t *testing.T) {
	for flavor, setting := range map[string]string{
		"cassandra": "partition_size_warn_threshold: 2MiB",
		"scylla":    "compaction_large_partition_warning_threshold_mb: 2",
	} {
		t.Run(flavor, func(t *testing.T) { testFindings(t, flavor, setting) })
	}
}

func testFindings(t *testing.T, flavor, setting string) {
	var nodetool func(args ...string) error
	p := mytest.Cassandra(t, flavor, mytest.CassandraOptions{Settings: []string{setting}, Nodetool: &nodetool})
	p.Database = ""
	s, err := Connect(p, mytest.CassandraPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if err := s.Query(q, args...).ExecContext(ctx); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		"CREATE KEYSPACE f WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': 1}",
		"CREATE TABLE f.big (pk int, ck int, v text, PRIMARY KEY (pk, ck))",
		"CREATE TABLE f.small (pk int PRIMARY KEY, v text)",
		"CREATE TABLE f.tomb (pk int, ck int, v int, PRIMARY KEY (pk, ck))",
		"CREATE TABLE f.clean (pk int, ck int, v int, PRIMARY KEY (pk, ck))",
		"CREATE TABLE f.expiring (pk int PRIMARY KEY, v int) WITH default_time_to_live = 3600",
		"CREATE TABLE f.windowed (pk int PRIMARY KEY, v int) WITH default_time_to_live = 3600 AND compaction = {'class': 'TimeWindowCompactionStrategy'}",
		"CREATE ROLE ro WITH LOGIN = true AND PASSWORD = 'ro-pass'",
		"GRANT SELECT ON KEYSPACE f TO ro",
	} {
		exec(q)
	}
	kib := strings.Repeat("x", 1024)
	for i := 0; i < 3000; i++ {
		exec("INSERT INTO f.big (pk, ck, v) VALUES (1, ?, ?)", i, kib)
	}
	for i := 0; i < 100; i++ {
		exec("INSERT INTO f.small (pk, v) VALUES (?, ?)", i, kib)
	}
	if flavor == "cassandra" {
		for i := 0; i < 1500; i++ {
			exec("INSERT INTO f.tomb (pk, ck, v) VALUES (1, ?, 0)", i)
			exec("DELETE FROM f.tomb WHERE pk = 1 AND ck = ?", i)
			exec("INSERT INTO f.clean (pk, ck, v) VALUES (1, ?, 0)", i)
		}
		for _, table := range []string{"tomb", "clean"} {
			if err := s.Query("SELECT * FROM f." + table + " WHERE pk = 1").IterContext(ctx).Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Both servers measure partitions when they write SSTables.
	if err := nodetool("flush", "f"); err != nil {
		t.Fatal(err)
	}

	findings := func(m *schema.Model, name string) []schema.Finding {
		t.Helper()
		for _, r := range m.Relations {
			if r.Name == name {
				return r.Findings
			}
		}
		t.Fatalf("no table %s", name)
		return nil
	}
	m, err := Introspect(ctx, s, "f")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Relations {
		for _, f := range r.Findings {
			t.Logf("%s %s %s: %s", r.Name, f.RuleID, f.Level, f.Message)
		}
	}
	want := "the largest partition on this node is about 4 MiB, above partition_size_warn_threshold 2MiB"
	if flavor == "scylla" {
		want = "the largest partition ScyllaDB recorded on this node is 3.0 MiB with 3000 rows " +
			"(system.large_partitions, which keeps partitions above compaction_large_partition_warning_threshold_mb 2 or compaction_rows_count_warning_threshold 100000 rows)"
	}
	if f := findings(m, "big"); len(f) != 1 || f[0].RuleID != "DBS-CQL-LARGE-PARTITION" || f[0].Level != "warn" || !strings.HasPrefix(f[0].Message, want) {
		t.Errorf("big %+v", f)
	}
	// 100 partitions of 1 KiB: none large.
	if f := findings(m, "small"); len(f) != 0 {
		t.Errorf("small %+v", f)
	}
	if f := findings(m, "expiring"); len(f) != 1 || f[0].RuleID != "DBS-CQL-TTL-NO-TWCS" || f[0].Level != "info" ||
		!strings.HasPrefix(f[0].Message, "default TTL 1 hour under ") {
		t.Errorf("expiring %+v", f)
	}
	if f := findings(m, "windowed"); len(f) != 0 {
		t.Errorf("windowed %+v", f)
	}
	if f := findings(m, "clean"); len(f) != 0 {
		t.Errorf("clean %+v", f)
	}
	tomb := findings(m, "tomb")
	if flavor == "scylla" && len(tomb) != 0 {
		t.Errorf("scylla tomb %+v", tomb)
	}
	if flavor == "cassandra" && (len(tomb) != 1 || tomb[0].RuleID != "DBS-CQL-TOMBSTONES" || tomb[0].Level != "warn" ||
		!strings.HasPrefix(tomb[0].Message, "recent reads on this node scanned up to about ") ||
		!strings.Contains(tomb[0].Message, "above tombstone_warn_threshold 1000; at tombstone_failure_threshold 100000")) {
		t.Errorf("tomb %+v", tomb)
	}

	t.Run("danger", func(t *testing.T) {
		defer func(v float64) { largePartitionDangerMiB = v }(largePartitionDangerMiB)
		largePartitionDangerMiB = 2
		m, err := Introspect(ctx, s, "f")
		if err != nil {
			t.Fatal(err)
		}
		if f := findings(m, "big"); len(f) != 1 || f[0].Level != "danger" {
			t.Errorf("big %+v", f)
		}
		if f := findings(m, "small"); len(f) != 0 {
			t.Errorf("small %+v", f)
		}
	})

	// A role with SELECT on the keyspace alone: the statistics it may not read are skipped.
	t.Run("plain role", func(t *testing.T) {
		ro := p
		ro.User = "ro"
		rs, err := Connect(ro, "ro-pass")
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		m, err := Introspect(ctx, rs, "f")
		if err != nil {
			t.Fatal(err)
		}
		if f := findings(m, "expiring"); len(f) != 1 || f[0].RuleID != "DBS-CQL-TTL-NO-TWCS" {
			t.Errorf("expiring %+v", f)
		}
		for _, r := range m.Relations {
			for _, f := range r.Findings {
				t.Logf("as ro: %s %s %s", r.Name, f.RuleID, f.Level)
				if flavor == "cassandra" && f.RuleID != "DBS-CQL-TTL-NO-TWCS" {
					t.Errorf("%s %+v without a grant on system_views", r.Name, f)
				}
			}
		}
	})
}

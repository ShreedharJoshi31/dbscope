package cassandra

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/mytest"
)

func healthCheck(t *testing.T, r *health.Report, id string) health.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %+v", id, r.Checks)
	return health.Check{}
}

// TestHealth reads health on Cassandra and ScyllaDB; on Cassandra it first
// reads a partition through 1,500 deleted rows so the tombstone check sees it.
func TestHealth(t *testing.T) {
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
			with := ""
			if flavor == "scylla" {
				with = " AND tablets = {'enabled': false}"
			}
			for _, q := range []string{
				"CREATE KEYSPACE hk WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': 1}" + with,
				"CREATE TABLE hk.t (pk int, ck int, v int, PRIMARY KEY (pk, ck))",
			} {
				if err := s.Query(q).Exec(); err != nil {
					t.Fatal(q, err)
				}
			}
			if flavor == "cassandra" {
				for i := 0; i < 1500; i++ {
					s.Query("INSERT INTO hk.t (pk, ck, v) VALUES (1, ?, 0)", i).Exec()
					s.Query("DELETE FROM hk.t WHERE pk = 1 AND ck = ?", i).Exec()
				}
				s.Query("SELECT * FROM hk.t WHERE pk = 1").Iter().Close()
			}
			r, err := Health(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if c := healthCheck(t, r, "cql.nodes"); c.Status != health.OK || !strings.Contains(c.Value, "1") {
				t.Errorf("nodes %+v", c)
			}
			if c := healthCheck(t, r, "cql.large_partitions"); c.Status == health.Unknown {
				t.Errorf("partitions %+v", c)
			}
			tomb := healthCheck(t, r, "cql.tombstones")
			if flavor == "scylla" {
				if tomb.Status != health.Unknown {
					t.Errorf("scylla tombstones %+v", tomb)
				}
				return
			}
			if tomb.Status != health.Warn || !strings.Contains(tomb.Value, "hk.t") {
				t.Errorf("tombstones %+v", tomb)
			}
			for _, id := range []string{"cql.thread_pools", "cql.hints"} {
				if c := healthCheck(t, r, id); c.Status == health.Unknown {
					t.Errorf("%s %+v", id, c)
				}
			}
		})
	}
}

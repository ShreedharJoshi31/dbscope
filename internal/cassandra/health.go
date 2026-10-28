package cassandra

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/dbscope/dbscope/internal/health"
)

// systemKeyspaces are Cassandra's and ScyllaDB's own; their tables are left out of table checks.
func systemKeyspace(ks string) bool {
	return strings.HasPrefix(ks, "system") || ks == "audit"
}

// Health reads the node's health checks (docs/HEALTH.md). Cassandra 5
// exposes them as system_views virtual tables; ScyllaDB has none of those
// but keeps cluster_status and large_partitions in its system keyspace.
func Health(ctx context.Context, s *gocql.Session) (*health.Report, error) {
	start := time.Now()
	srv, err := describe(ctx, s)
	if err != nil {
		return nil, err
	}
	name := map[string]string{"cassandra": "Cassandra", "scylla": "ScyllaDB"}[srv.Flavor]
	r := &health.Report{Engine: srv.Flavor, Server: strings.TrimSpace(name + " " + srv.Version), Checks: []health.Check{}}
	if srv.Flavor == "scylla" {
		scyllaHealth(ctx, s, r)
	} else {
		cassandraHealth(ctx, s, r)
	}
	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

func cassandraHealth(ctx context.Context, s *gocql.Session, r *health.Report) {
	iter := s.Query(`SELECT address, status FROM system_views.gossip_info`).WithContext(ctx).Iter()
	var addr, status string
	var n int
	var odd []string
	for iter.Scan(&addr, &status) {
		n++
		if state, _, _ := strings.Cut(status, ","); state != "NORMAL" {
			odd = append(odd, addr+" "+state)
		}
	}
	if err := iter.Close(); err != nil {
		r.Unavailable("cql.nodes", "Nodes", err.Error())
	} else {
		c := health.Check{ID: "cql.nodes", Title: "Nodes", Status: health.OK, Value: fmt.Sprintf("%d in gossip, all NORMAL", n),
			Detail: "Gossip does not say whether a node is up; nodetool status does."}
		if len(odd) > 0 {
			c.Status, c.Value = health.Warn, fmt.Sprintf("%d of %d not NORMAL", len(odd), n)
			c.Detail = "Joining, leaving or moving nodes: " + strings.Join(odd, ", ")
		}
		r.Add(c)
	}

	iter = s.Query(`SELECT name, pending_tasks, blocked_tasks FROM system_views.thread_pools`).WithContext(ctx).Iter()
	var pool string
	var pending, blocked int64
	var busy []string
	worst := 0.0
	for iter.Scan(&pool, &pending, &blocked) {
		if pending > 100 || blocked > 0 {
			busy = append(busy, fmt.Sprintf("%s %d pending, %d blocked", pool, pending, blocked))
			worst = max(worst, float64(pending))
			if blocked > 0 {
				worst = max(worst, 1000)
			}
		}
	}
	if err := iter.Close(); err != nil {
		r.Unavailable("cql.thread_pools", "Thread pools", err.Error())
	} else {
		c := health.Check{ID: "cql.thread_pools", Title: "Thread pools", Status: health.Level(worst, 100, 1000), Value: "no backlog"}
		if len(busy) > 0 {
			c.Value, c.Detail = fmt.Sprintf("%d backed up", len(busy)), strings.Join(busy, "; ")
			c.Fix = "A backlog in MutationStage or ReadStage means the node cannot keep up; check disk latency and compaction"
		}
		r.Add(c)
	}

	iter = s.Query(`SELECT address, files FROM system_views.pending_hints`).WithContext(ctx).Iter()
	var files int
	var hinted []string
	for iter.Scan(&addr, &files) {
		if files > 0 {
			hinted = append(hinted, fmt.Sprintf("%s (%d files)", addr, files))
		}
	}
	if err := iter.Close(); err != nil {
		r.Unavailable("cql.hints", "Pending hints", err.Error())
	} else {
		c := health.Check{ID: "cql.hints", Title: "Pending hints", Status: health.OK, Value: "none"}
		if len(hinted) > 0 {
			c.Status, c.Value = health.Warn, fmt.Sprintf("for %d nodes", len(hinted))
			c.Detail = "Writes this node holds for replicas that were down: " + strings.Join(hinted, ", ")
			c.Fix = "Bring the nodes back; after max_hint_window (3 h by default) hints stop and a repair is needed"
		}
		r.Add(c)
	}

	partitionCheck(r, func(add func(table string, mib float64)) error {
		iter := s.Query(`SELECT keyspace_name, table_name, mebibytes FROM system_views.max_partition_size`).WithContext(ctx).Iter()
		var ks, t string
		var mib int64
		for iter.Scan(&ks, &t, &mib) {
			if !systemKeyspace(ks) {
				add(ks+"."+t, float64(mib))
			}
		}
		return iter.Close()
	})

	iter = s.Query(`SELECT keyspace_name, table_name, max, p99th FROM system_views.tombstones_per_read`).WithContext(ctx).Iter()
	var ks, t string
	var maxT, p99 float64
	worstT, where := 0.0, ""
	for iter.Scan(&ks, &t, &maxT, &p99) {
		if !systemKeyspace(ks) && maxT > worstT {
			worstT, where = maxT, fmt.Sprintf("%s.%s (p99 %.0f)", ks, t, p99)
		}
	}
	if err := iter.Close(); err != nil {
		r.Unavailable("cql.tombstones", "Tombstones per read", err.Error())
	} else {
		c := health.Check{ID: "cql.tombstones", Title: "Tombstones per read", Status: health.Level(worstT, 1000, 100_000), Value: fmt.Sprintf("up to %.0f", worstT),
			Detail: "Reads past 1,000 tombstones are logged as warnings, and past 100,000 they fail (tombstone_failure_threshold).",
			Fix:    "Avoid deleting inside partitions that are read in ranges; use TTLs with a time-bucketed key"}
		if where != "" {
			c.Value += " in " + where
		}
		r.Add(c)
	}
}

func scyllaHealth(ctx context.Context, s *gocql.Session, r *health.Report) {
	iter := s.Query(`SELECT peer, status, up FROM system.cluster_status`).WithContext(ctx).Iter()
	var peer, status string
	var up bool
	var n int
	var bad []string
	for iter.Scan(&peer, &status, &up) {
		n++
		if !up || status != "NORMAL" {
			bad = append(bad, fmt.Sprintf("%s %s, up=%v", peer, status, up))
		}
	}
	if err := iter.Close(); err != nil {
		r.Unavailable("cql.nodes", "Nodes", err.Error())
	} else {
		c := health.Check{ID: "cql.nodes", Title: "Nodes", Status: health.OK, Value: fmt.Sprintf("%d, all up and NORMAL", n)}
		if len(bad) > 0 {
			c.Status, c.Value, c.Detail = health.Danger, fmt.Sprintf("%d of %d not up and NORMAL", len(bad), n), strings.Join(bad, "; ")
			c.Fix = "nodetool status and the node's log show why; writes for it are hinted until it returns"
		}
		r.Add(c)
	}
	partitionCheck(r, func(add func(table string, mib float64)) error {
		iter := s.Query(`SELECT keyspace_name, table_name, partition_size FROM system.large_partitions`).WithContext(ctx).Iter()
		var ks, t string
		var size int64
		for iter.Scan(&ks, &t, &size) {
			if !systemKeyspace(ks) {
				add(ks+"."+t, float64(size)/(1<<20))
			}
		}
		return iter.Close()
	})
	r.Add(health.Check{ID: "cql.tombstones", Title: "Tombstones per read", Status: health.Unknown, Value: "not exposed",
		Detail: "ScyllaDB reports tombstone counts through its metrics endpoint, not through CQL."})
}

// partitionCheck rates the largest partition per table: reads of a large
// partition load it through the heap, and compaction rewrites it whole.
func partitionCheck(r *health.Report, read func(add func(table string, mib float64)) error) {
	sizes := map[string]float64{}
	err := read(func(table string, mib float64) { sizes[table] = max(sizes[table], mib) })
	if err != nil {
		r.Unavailable("cql.large_partitions", "Largest partitions", err.Error())
		return
	}
	tables := make([]string, 0, len(sizes))
	for t := range sizes {
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return sizes[tables[i]] > sizes[tables[j]] })
	c := health.Check{ID: "cql.large_partitions", Title: "Largest partitions", Status: health.OK, Value: "none recorded"}
	if len(tables) > 0 {
		c.Status = health.Level(sizes[tables[0]], 100, 1000)
		c.Value = fmt.Sprintf("%.0f MiB in %s", sizes[tables[0]], tables[0])
		c.Detail = "Partitions above 100 MiB slow reads and repairs and pressure the heap."
		c.Fix = "Add a time bucket or another component to the partition key"
	}
	r.Add(c)
}

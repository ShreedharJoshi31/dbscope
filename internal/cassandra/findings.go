package cassandra

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/dbscope/dbscope/internal/schema"
)

// Thresholds; variables so tests can lower them. The node's own settings
// replace the warning levels when it exposes them.
var (
	// largePartitionWarnMiB applies when Cassandra sets no partition size
	// guardrail. 100MiB was compaction_large_partition_warning_threshold's
	// default in 4.1; 5.0 replaced it by partition_size_warn_threshold,
	// which is null (off) by default (conf/cassandra.yaml of each release).
	largePartitionWarnMiB = 100.0
	// largePartitionDangerMiB raises the finding to danger. It is dbscope's
	// choice, near ScyllaDB's own warning default (1000 MB), so that warn and
	// danger differ on Cassandra's 100 MiB default.
	largePartitionDangerMiB = 1024.0
	// tombstoneWarn and tombstoneFail are Cassandra's tombstone_warn_threshold
	// and tombstone_failure_threshold defaults in 4.1 and 5.0 (cassandra.yaml).
	tombstoneWarn = 1000.0
	tombstoneFail = 100000.0
)

const (
	// findingsTimeout bounds the node statistics reads; what is not read by
	// then is skipped.
	findingsTimeout = 5 * time.Second
	// largePartitionRows bounds the system.large_partitions rows read per table.
	largePartitionRows = 100
)

// ttlFinding flags a default TTL without TimeWindowCompactionStrategy. Under
// size-tiered or leveled compaction, expired rows share SSTables with live
// ones and stay on disk until a compaction merges them; TWCS groups rows by
// write time so a whole SSTable is dropped once it has expired
// (cassandra.apache.org/doc/5.0/cassandra/managing/operating/compaction/twcs.html,
// docs.scylladb.com/manual/stable/architecture/compaction/compaction-strategies.html).
func ttlFinding(r *schema.Relation, row map[string]any) {
	ttl, _ := row["default_time_to_live"].(int)
	if ttl <= 0 {
		return
	}
	cm, _ := row["compaction"].(map[string]string)
	cls := cm["class"]
	if strings.HasSuffix(cls, "TimeWindowCompactionStrategy") {
		return
	}
	cls = cls[strings.LastIndex(cls, ".")+1:]
	r.AddFinding("DBS-CQL-TTL-NO-TWCS", "info", fmt.Sprintf("default TTL %s under %s: expired rows share SSTables with live ones "+
		"and stay on disk as tombstones until compaction merges them; TimeWindowCompactionStrategy groups rows by write time "+
		"and drops a whole SSTable once all of it has expired", seconds(ttl), cls), "")
}

// statsFindings adds the findings that come from the node's statistics:
// large partitions on both servers and tombstones per read on Cassandra.
// They describe only the node that answers. A read the role may not run,
// or that times out, adds nothing.
func statsFindings(ctx context.Context, s *gocql.Session, flavor, keyspace string, order []string, byName map[string]*schema.Relation) {
	ctx, cancel := context.WithTimeout(ctx, findingsTimeout)
	defer cancel()
	if flavor == "scylla" {
		scyllaLargePartitions(ctx, s, keyspace, order, byName)
		return // ScyllaDB counts tombstones only in its metrics, not over CQL
	}
	set := cassandraSettings(ctx, s)
	cassandraLargePartitions(ctx, s, keyspace, byName, set)
	cassandraTombstones(ctx, s, keyspace, byName, set)
}

func cassandraSettings(ctx context.Context, s *gocql.Session) map[string]string {
	set := map[string]string{}
	iter := s.Query(`SELECT name, value FROM system_views.settings WHERE name IN ('partition_size_warn_threshold',
		'compaction_large_partition_warning_threshold', 'tombstone_warn_threshold', 'tombstone_failure_threshold')`).WithContext(ctx).Iter()
	var name, value string
	for iter.Scan(&name, &value) {
		if value != "" && value != "null" {
			set[name] = value
		}
	}
	iter.Close() // unreadable settings leave the defaults
	return set
}

// cassandraLargePartitions reads system_views.max_partition_size: per
// table, the largest partition in this node's flushed SSTables, in whole
// mebibytes rounded up. It gives no partition key.
func cassandraLargePartitions(ctx context.Context, s *gocql.Session, keyspace string, byName map[string]*schema.Relation, set map[string]string) {
	warn, source := largePartitionWarnMiB, fmt.Sprintf("%.0f MiB, Cassandra 4.1's default compaction_large_partition_warning_threshold", largePartitionWarnMiB)
	for _, name := range []string{"partition_size_warn_threshold", "compaction_large_partition_warning_threshold"} {
		if mib, ok := parseMiB(set[name]); ok {
			warn, source = mib, name+" "+set[name]
			break
		}
	}
	iter := s.Query(`SELECT table_name, mebibytes FROM system_views.max_partition_size WHERE keyspace_name = ? ALLOW FILTERING`, keyspace).WithContext(ctx).Iter()
	var table string
	var mib int64
	for iter.Scan(&table, &mib) {
		r := byName[table]
		if r == nil || float64(mib) <= warn {
			continue
		}
		level, above := "warn", source
		if float64(mib) > largePartitionDangerMiB {
			level, above = "danger", fmt.Sprintf("%.0f MiB", largePartitionDangerMiB)
		}
		r.AddFinding("DBS-CQL-LARGE-PARTITION", level, fmt.Sprintf("the largest partition on this node is about %d MiB, above %s "+
			"(system_views.max_partition_size: flushed SSTables, rounded up); a large partition slows reads, compaction and repair "+
			"and pressures the heap; add a time bucket or another column to the partition key", mib, above), "")
	}
	iter.Close()
}

// scyllaLargePartitions reads system.large_partitions, where ScyllaDB records
// partitions it wrote above compaction_large_partition_warning_threshold_mb
// (1000 by default) or compaction_rows_count_warning_threshold (100000 rows)
// (docs.scylladb.com/manual/stable/troubleshooting/large-partition-table.html).
// The key's value is row data, so it is left out of the message.
func scyllaLargePartitions(ctx context.Context, s *gocql.Session, keyspace string, order []string, byName map[string]*schema.Relation) {
	var limits string
	for _, table := range order {
		iter := s.Query(`SELECT partition_size, rows FROM system.large_partitions WHERE keyspace_name = ? AND table_name = ? LIMIT ?`,
			keyspace, table, largePartitionRows).WithContext(ctx).Iter()
		var size, rows, top, topRows int64
		for iter.Scan(&size, &rows) {
			if size > top {
				top, topRows = size, rows
			}
		}
		if err := iter.Close(); err != nil {
			return // not granted or out of time: the other tables would fail the same way
		}
		if top == 0 {
			continue
		}
		if limits == "" {
			limits = scyllaLimits(ctx, s)
		}
		mib := float64(top) / (1 << 20)
		level := "warn"
		if mib > largePartitionDangerMiB {
			level = "danger"
		}
		byName[table].AddFinding("DBS-CQL-LARGE-PARTITION", level, fmt.Sprintf("the largest partition ScyllaDB recorded on this node "+
			"is %.1f MiB with %d rows (system.large_partitions, which keeps partitions above %s); a large partition slows reads, "+
			"compaction and repair; add a time bucket or another column to the partition key", mib, topRows, limits), "")
	}
}

// scyllaLimits names the node's large partition thresholds from
// system.config, or only the setting names when it is not readable.
func scyllaLimits(ctx context.Context, s *gocql.Session) string {
	val := map[string]string{}
	iter := s.Query(`SELECT name, value FROM system.config WHERE name IN ('compaction_large_partition_warning_threshold_mb',
		'compaction_rows_count_warning_threshold')`).WithContext(ctx).Iter()
	var name, value string
	for iter.Scan(&name, &value) {
		val[name] = value
	}
	iter.Close()
	mb, rows := val["compaction_large_partition_warning_threshold_mb"], val["compaction_rows_count_warning_threshold"]
	if mb == "" || rows == "" {
		return "compaction_large_partition_warning_threshold_mb or compaction_rows_count_warning_threshold"
	}
	return "compaction_large_partition_warning_threshold_mb " + mb + " or compaction_rows_count_warning_threshold " + rows + " rows"
}

// cassandraTombstones reads system_views.tombstones_per_read, a decaying
// histogram of the tombstones each recent read of a table scanned on this node.
func cassandraTombstones(ctx context.Context, s *gocql.Session, keyspace string, byName map[string]*schema.Relation, set map[string]string) {
	warn, fail := tombstoneWarn, tombstoneFail
	if v, err := strconv.ParseFloat(set["tombstone_warn_threshold"], 64); err == nil && v > 0 {
		warn = v
	}
	if v, err := strconv.ParseFloat(set["tombstone_failure_threshold"], 64); err == nil && v > 0 {
		fail = v
	}
	iter := s.Query(`SELECT table_name, max, p99th FROM system_views.tombstones_per_read WHERE keyspace_name = ? ALLOW FILTERING`, keyspace).WithContext(ctx).Iter()
	var table string
	var top, p99 float64
	for iter.Scan(&table, &top, &p99) {
		r := byName[table]
		if r == nil || top <= warn {
			continue
		}
		r.AddFinding("DBS-CQL-TOMBSTONES", "warn", fmt.Sprintf("recent reads on this node scanned up to about %.0f tombstones (p99 %.0f), "+
			"above tombstone_warn_threshold %.0f; at tombstone_failure_threshold %.0f a read fails; deletes or expired rows inside "+
			"partitions that are read in ranges cause this, so use TTLs with a time-bucketed key", top, p99, warn, fail), "")
	}
	iter.Close()
}

// parseMiB reads a Cassandra data storage value such as 100MiB or 1GiB.
func parseMiB(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	for _, u := range []struct {
		suffix string
		mib    float64
	}{{"KiB", 1.0 / 1024}, {"MiB", 1}, {"GiB", 1024}, {"B", 1.0 / (1 << 20)}} {
		if num, ok := strings.CutSuffix(v, u.suffix); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			return n * u.mib, err == nil && n >= 0
		}
	}
	return 0, false
}

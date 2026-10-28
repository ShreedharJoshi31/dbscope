package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/dbscope/dbscope/internal/health"
)

// Health reads the server's health checks (docs/HEALTH.md) from system
// tables; a table the user may not read makes its check unknown with the
// grant it needs.
func Health(ctx context.Context, c *Client) (*health.Report, error) {
	start := time.Now()
	var version string
	if err := c.Conn.QueryRow(ctx, `SELECT version()`).Scan(&version); err != nil {
		return nil, err
	}
	r := &health.Report{Engine: "clickhouse", Server: "ClickHouse " + version, Checks: []health.Check{}}
	why := func(table string, err error) string {
		var ex *ch.Exception
		if errors.As(err, &ex) && (ex.Code == 497 || ex.Code == 60) {
			return "The user may not read " + table + ": GRANT SELECT ON " + table + " TO it."
		}
		return err.Error()
	}

	// Active parts per partition: inserts slow down at parts_to_delay_insert and fail at parts_to_throw_insert.
	var parts uint64
	var where string
	err := c.Conn.QueryRow(ctx, `SELECT count() AS n, any(database || '.' || table || ' partition ' || partition_id)
		FROM system.parts WHERE active GROUP BY database, table, partition_id ORDER BY n DESC LIMIT 1`).Scan(&parts, &where)
	switch {
	case err != nil && strings.Contains(err.Error(), "no rows"):
		r.Add(health.Check{ID: "ch.parts", Title: "Parts per partition", Status: health.OK, Value: "no parts"})
	case err != nil:
		r.Unavailable("ch.parts", "Parts per partition", why("system.parts", err))
	default:
		r.Add(health.Check{ID: "ch.parts", Title: "Parts per partition", Status: health.Level(float64(parts), 300, 1000),
			Value:  fmt.Sprintf("up to %d (%s)", parts, where),
			Detail: "At 1,000 active parts in a partition inserts are delayed; at 3,000 they fail with TOO_MANY_PARTS.",
			Fix:    "Batch inserts (thousands of rows each) or use async_insert; check merges are not stuck"})
	}

	rows, err := c.Conn.Query(ctx, `SELECT database || '.' || table, mutation_id, command, latest_fail_reason,
			dateDiff('second', create_time, now())
		FROM system.mutations WHERE NOT is_done ORDER BY create_time LIMIT 10`)
	if err != nil {
		r.Unavailable("ch.mutations", "Unfinished mutations", why("system.mutations", err))
	} else {
		var parts []string
		var oldest int64
		failing := false
		for rows.Next() {
			var table, id, command, reason string
			var age int64
			if rows.Scan(&table, &id, &command, &reason, &age) != nil {
				continue
			}
			oldest = max(oldest, age)
			part := fmt.Sprintf("%s %s (%s) for %s", table, id, command, health.Duration(time.Duration(age)*time.Second))
			if reason != "" {
				failing = true
				part += ": " + reason
			}
			parts = append(parts, part)
		}
		rows.Close()
		chk := health.Check{ID: "ch.mutations", Title: "Unfinished mutations", Status: health.Level(time.Duration(oldest*int64(time.Second)).Minutes(), 30, 240),
			Value: fmt.Sprint(len(parts)), Detail: strings.Join(parts, "; "),
			Fix: "A failing mutation blocks the ones after it: fix its cause or KILL MUTATION WHERE mutation_id = '...'"}
		if failing {
			chk.Status = health.Danger
		}
		if len(parts) == 0 {
			chk.Value = "none"
		}
		r.Add(chk)
	}

	rows, err = c.Conn.Query(ctx, `SELECT database || '.' || table, is_readonly, absolute_delay, queue_size FROM system.replicas`)
	if err != nil {
		r.Unavailable("ch.replication", "Replication", why("system.replicas", err))
	} else {
		var n int
		var worstDelay uint64
		var readonly []string
		for rows.Next() {
			var table string
			var ro uint8
			var delay uint64
			var queue uint32
			if rows.Scan(&table, &ro, &delay, &queue) != nil {
				continue
			}
			n++
			worstDelay = max(worstDelay, delay)
			if ro != 0 {
				readonly = append(readonly, table)
			}
		}
		rows.Close()
		chk := health.Check{ID: "ch.replication", Title: "Replication", Status: health.Level(float64(worstDelay), 30, 300), Value: "no replicated tables"}
		if n > 0 {
			chk.Value = fmt.Sprintf("%d replicated tables, up to %s behind", n, health.Duration(time.Duration(worstDelay)*time.Second))
		}
		if len(readonly) > 0 {
			chk.Status = health.Danger
			chk.Detail = "Read-only (lost their Keeper session): " + strings.Join(readonly, ", ")
			chk.Fix = "Check the Keeper or ZooKeeper connection; the tables accept no inserts until it returns"
		}
		r.Add(chk)
	}

	rows, err = c.Conn.Query(ctx, `SELECT name, free_space, total_space FROM system.disks WHERE total_space > 0`)
	if err != nil {
		r.Unavailable("ch.disk", "Disk use", why("system.disks", err))
	} else {
		worst, name := 0.0, ""
		for rows.Next() {
			var disk string
			var free, total uint64
			if rows.Scan(&disk, &free, &total) == nil && total > 0 {
				if used := 1 - float64(free)/float64(total); used > worst {
					worst, name = used, disk
				}
			}
		}
		rows.Close()
		r.Add(health.Check{ID: "ch.disk", Title: "Disk use", Status: health.Level(worst, 0.8, 0.9), Value: fmt.Sprintf("%.0f%% on %s", worst*100, name),
			Detail: "Merges need free space for their output; a full disk stops inserts and merges.",
			Fix:    "Drop old partitions or move them to colder storage with a TTL"})
	}

	var resident, total float64
	err = c.Conn.QueryRow(ctx, `SELECT sumIf(value, metric = 'MemoryResident'), sumIf(value, metric = 'OSMemoryTotal')
		FROM system.asynchronous_metrics WHERE metric IN ('MemoryResident', 'OSMemoryTotal')`).Scan(&resident, &total)
	if err != nil {
		r.Unavailable("ch.memory", "Memory", why("system.asynchronous_metrics", err))
	} else if total > 0 {
		r.Add(health.Check{ID: "ch.memory", Title: "Memory", Status: health.Level(resident/total, 0.8, 0.9),
			Value:  fmt.Sprintf("%s of %s", health.Bytes(int64(resident)), health.Bytes(int64(total))),
			Detail: "Resident memory of the server against the machine's; past max_server_memory_usage queries fail with MEMORY_LIMIT_EXCEEDED.",
			Fix:    "Look for large GROUP BY or JOIN queries in system.query_log; lower max_memory_usage per query"})
	}

	var detached uint64
	if err := c.Conn.QueryRow(ctx, `SELECT count() FROM system.detached_parts WHERE reason <> ''`).Scan(&detached); err != nil {
		r.Unavailable("ch.detached_parts", "Detached parts", why("system.detached_parts", err))
	} else {
		r.Add(health.Check{ID: "ch.detached_parts", Title: "Detached parts", Status: health.Level(float64(detached), 1, 100), Value: fmt.Sprint(detached),
			Detail: "Parts the server set aside as broken or unexpected; their rows are not queried.",
			Fix:    "Read the reasons in system.detached_parts; reattach with ALTER TABLE ... ATTACH PART or drop them"})
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

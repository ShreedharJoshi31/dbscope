package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dbscope/dbscope/internal/health"
)

// The grants the health checks need beyond a user that can scan.
const (
	infoGrant    = "INFO is not allowed for this user: grant it with ACL SETUSER <user> +info."
	slowlogGrant = "SLOWLOG LEN is not allowed for this user: grant it with ACL SETUSER <user> +slowlog|len."
)

// infoChecks are the checks read from INFO, which all go when INFO is refused.
var infoChecks = []struct{ id, title string }{
	{"redis.memory", "Memory"},
	{"redis.evictions", "Evictions and refused writes"},
	{"redis.connections", "Connections"},
	{"redis.rejected_connections", "Rejected connections"},
	{"redis.replication", "Replication"},
	{"redis.persistence", "Persistence"},
}

// Health reads the server's health checks (docs/HEALTH.md) from one INFO
// and SLOWLOG LEN. It changes nothing: no CONFIG SET, no SLOWLOG RESET, no
// CONFIG RESETSTAT. A check whose command the user may not run comes back
// unknown with the ACL rule that would allow it.
func Health(ctx context.Context, c *goredis.Client) (*health.Report, error) {
	start := time.Now()
	r := &health.Report{Checks: []health.Check{}}
	// No PING first: after a failed save or AOF write the server refuses PING with MISCONF.
	info, err := ReadInfo(ctx, c, "server", "memory", "clients", "stats", "replication", "persistence", "errorstats")
	var srv Server
	switch {
	case refused(err):
		srv = hello(ctx, c)
		for _, ch := range infoChecks {
			r.Unavailable(ch.id, ch.title, infoGrant)
		}
	case err != nil:
		return nil, err
	default:
		srv = describe(info)
		memoryChecks(r, info)
		clientChecks(r, info)
		replicationCheck(r, info)
		persistenceCheck(r, info)
	}
	r.Engine, r.Server = srv.Flavor, "Redis "+srv.Version
	if srv.Flavor == "valkey" {
		r.Server = "Valkey " + srv.Version
	}
	slowlogCheck(ctx, c, r)
	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

// memoryChecks compares used memory with maxmemory, and counts evictions
// and commands refused for memory. The 80% and 95% marks are dbscope's,
// as for the other engines; Redis sets none.
func memoryChecks(r *health.Report, info Info) {
	used, limit := int64(info.num("memory", "used_memory")), int64(info.num("memory", "maxmemory"))
	policy := info.get("memory", "maxmemory_policy")
	c := health.Check{ID: "redis.memory", Title: "Memory"}
	if limit == 0 {
		c.Status, c.Value = health.OK, health.Bytes(used)+" used, no maxmemory"
		c.Detail = "maxmemory is 0, so there is no limit: the server grows until the machine runs out of memory."
		c.Fix = "Set maxmemory below the machine's memory, leaving room for buffers and fork copy-on-write"
	} else {
		share := float64(used) / float64(limit)
		c.Status, c.Value = health.Level(share, 0.8, 0.95), fmt.Sprintf("%s of %s (%.0f%%)", health.Bytes(used), health.Bytes(limit), share*100)
		c.Detail = "maxmemory-policy is " + policy + "."
		if c.Status != health.OK {
			switch {
			case policy == "noeviction":
				c.Detail += " Under noeviction nothing is evicted: at maxmemory, writes that need memory fail with an OOM error."
			case strings.HasPrefix(policy, "volatile-"):
				c.Detail += " Only keys with an expiry can be evicted; when none is left, writes fail with an OOM error as under noeviction."
			default:
				c.Detail += " At maxmemory the server evicts keys to make room for writes."
			}
			c.Fix = "Raise maxmemory, or delete or expire keys; for a cache, choose an allkeys-* policy"
		}
	}
	r.Add(c)

	evicted, oom := int64(info.num("stats", "evicted_keys")), errorCount(info, "OOM")
	c = health.Check{ID: "redis.evictions", Title: "Evictions and refused writes", Status: health.OK,
		Value:  fmt.Sprintf("%d evicted, %d refused for memory", evicted, oom),
		Detail: "Since the server started or its statistics were reset; maxmemory-policy is " + policy + "."}
	if oom > 0 {
		c.Status = health.Warn
		c.Detail += fmt.Sprintf(" %s failed with OOM because memory was at maxmemory.", plural(oom, "command"))
		if policy == "noeviction" {
			c.Detail += " Under noeviction the server refuses writes instead of evicting."
		}
		c.Fix = "Raise maxmemory, or delete or expire keys; for a cache, choose an allkeys-* policy"
	}
	if evicted > 0 {
		c.Status = health.Warn
		c.Detail += " Evicted keys are gone: expected for a cache, lost data for anything else."
		if c.Fix == "" {
			c.Fix = "Raise maxmemory if these keys are data, not cache"
		}
	}
	r.Add(c)
}

// clientChecks compares connected clients with maxclients, and counts
// connections refused at the limit. The marks are those of memoryChecks.
func clientChecks(r *health.Report, info Info) {
	connected, limit := int64(info.num("clients", "connected_clients")), int64(info.num("clients", "maxclients"))
	c := health.Check{ID: "redis.connections", Title: "Connections", Status: health.OK, Value: fmt.Sprintf("%d connected", connected),
		Detail: "This server's INFO does not report maxclients."}
	if limit > 0 {
		c.Status, c.Value, c.Detail = health.Level(float64(connected)/float64(limit), 0.8, 0.95), fmt.Sprintf("%d of %d", connected, limit), ""
		if c.Status != health.OK {
			c.Detail = "At maxclients the server refuses new connections."
			c.Fix = "Lower the applications' pool sizes, or raise maxclients and the process's file limit"
		}
	}
	r.Add(c)

	rejected := int64(info.num("stats", "rejected_connections"))
	c = health.Check{ID: "redis.rejected_connections", Title: "Rejected connections", Status: health.OK, Value: fmt.Sprintf("%d", rejected),
		Detail: "Connections refused because maxclients was reached, since the server started or its statistics were reset."}
	if rejected > 0 {
		c.Status = health.Warn
		c.Fix = "Lower the applications' pool sizes, or raise maxclients and the process's file limit"
	}
	r.Add(c)
}

// replicationCheck reports a replica's link to its primary, or a primary's replicas.
func replicationCheck(r *health.Report, info Info) {
	role := info.get("replication", "role")
	c := health.Check{ID: "redis.replication", Title: "Replication", Status: health.OK}
	if role != "slave" && role != "replica" {
		c.Value = fmt.Sprintf("primary, %s connected", plural(int64(info.num("replication", "connected_slaves")), "replica"))
		r.Add(c)
		return
	}
	primary := info.get("replication", "master_host") + ":" + info.get("replication", "master_port")
	if info.get("replication", "master_link_status") == "up" {
		c.Value = "replica of " + primary + ", link up"
		c.Detail = fmt.Sprintf("Last heard from the primary %s s ago.", info.get("replication", "master_last_io_seconds_ago"))
	} else {
		c.Status, c.Value = health.Danger, "replica of "+primary+", link down"
		c.Detail = "The replica is not receiving the primary's writes, so its data is falling behind."
		if s := info.num("replication", "master_link_down_since_seconds"); s > 0 {
			c.Detail = fmt.Sprintf("The link has been down for %s, so the replica is not receiving the primary's writes.", health.Duration(time.Duration(s)*time.Second))
		}
		c.Fix = "Check that the primary is reachable from the replica and that masterauth and masteruser are right; the replica's log says why"
	}
	r.Add(c)
}

// persistenceCheck reports a failed RDB snapshot or AOF write.
func persistenceCheck(r *health.Report, info Info) {
	var failed []string
	if info.get("persistence", "rdb_last_bgsave_status") == "err" {
		failed = append(failed, "the last background save (RDB snapshot) failed; with stop-writes-on-bgsave-error yes, the default, the server refuses writes")
	}
	aof := info.get("persistence", "aof_enabled") == "1"
	if aof && info.get("persistence", "aof_last_write_status") == "err" {
		failed = append(failed, "the last append-only file write failed, and the server refuses writes")
	}
	c := health.Check{ID: "redis.persistence", Title: "Persistence", Status: health.OK, Value: "last RDB save ok"}
	if aof {
		c.Value += ", AOF writes ok"
	}
	if len(failed) > 0 {
		c.Status, c.Value = health.Danger, "failing"
		c.Detail = strings.ToUpper(failed[0][:1]) + failed[0][1:]
		if len(failed) > 1 {
			c.Detail += "; " + failed[1]
		}
		c.Detail += "."
		c.Fix = "Check the free space and permissions of the data directory and the server log"
	}
	r.Add(c)
}

// slowlogCheck counts the slow log's entries with SLOWLOG LEN; it never resets the log.
func slowlogCheck(ctx context.Context, c *goredis.Client, r *health.Report) {
	n, err := c.Do(ctx, "SLOWLOG", "LEN").Int64()
	if err != nil {
		why := err.Error()
		if refused(err) {
			why = slowlogGrant
		}
		r.Unavailable("redis.slowlog", "Slow log", why)
		return
	}
	entries := fmt.Sprintf("%d entries", n)
	if n == 1 {
		entries = "1 entry"
	}
	// Defaults from redis.conf: slowlog-log-slower-than 10000 microseconds, slowlog-max-len 128.
	ch := health.Check{ID: "redis.slowlog", Title: "Slow log", Status: health.OK, Value: entries,
		Detail: "Commands that ran longer than slowlog-log-slower-than (10 ms by default) since the log was last cleared; it keeps the latest slowlog-max-len (128 by default)."}
	if n > 0 {
		ch.Status = health.Warn
		ch.Fix = "Read the entries with SLOWLOG GET (grant +slowlog|get) and look for KEYS, reads of big keys and long scripts"
	}
	r.Add(ch)
}

// errorCount reads count from INFO errorstats' errorstat_<prefix> line (Redis 6.2+).
func errorCount(info Info, prefix string) int64 {
	v := info.get("errorstats", "errorstat_"+prefix)
	for _, kv := range strings.Split(v, ",") {
		if k, val, ok := strings.Cut(kv, "="); ok && k == "count" {
			var n int64
			fmt.Sscan(val, &n)
			return n
		}
	}
	return 0
}

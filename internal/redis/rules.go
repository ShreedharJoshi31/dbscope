package redis

import (
	"fmt"
	"strings"
)

func (r *Report) add(id, rating, msg, alt string) {
	r.Findings = append(r.Findings, Finding{RuleID: id, Rating: rating, Message: msg, SafeAlternative: alt})
}

// rate turns server facts and the scan into findings (docs/research-redis.md, 4).
func (r *Report) rate(info Info) {
	m := r.Memory
	policy := m.Policy
	switch {
	case m.Max == 0 && r.Server.Role != "slave":
		r.add("DBS-REDIS-NO-MAXMEMORY", "medium", "maxmemory is 0, so Redis grows without limit until the operating system runs out of memory",
			"Set maxmemory below the machine's memory, leaving room for buffers and fork copy-on-write")
	case policy == "noeviction" && float64(m.Used) > 0.9*float64(m.Max):
		r.add("DBS-REDIS-NOEVICTION-FULL", "high", fmt.Sprintf("memory is at %.0f%% of maxmemory under noeviction: writes that need memory will fail", 100*float64(m.Used)/float64(m.Max)),
			"Raise maxmemory, free keys, or choose an eviction policy if this is a cache")
	}
	noTTL, scanned := 0, 0
	for _, p := range r.Patterns {
		noTTL += p.NoTTL
		scanned += p.Keys
	}
	if strings.HasPrefix(policy, "volatile-") && scanned > 0 && noTTL*2 > scanned {
		r.add("DBS-REDIS-VOLATILE-NO-TTL", "high",
			fmt.Sprintf("the policy %s evicts only keys with an expiry, but %d of %d scanned keys have none, so at maxmemory it behaves like noeviction", policy, noTTL, scanned),
			"Give cache keys a TTL, or use an allkeys-* policy if every key may be evicted")
	}
	if strings.HasPrefix(policy, "allkeys-") && scanned > 0 && noTTL == scanned {
		r.add("DBS-REDIS-ALLKEYS-EVICTS-DATA", "low",
			"no scanned key has an expiry and the policy "+policy+" may evict any of them; if some are primary data rather than cache, they can disappear at maxmemory", "")
	}
	for _, p := range r.Patterns {
		if p.Keys >= 100 && p.NoTTL == p.Keys && (strings.Contains(p.Pattern, "session") || strings.Contains(p.Pattern, "cache") || strings.Contains(p.Pattern, "tmp")) {
			r.add("DBS-REDIS-PATTERN-NO-TTL", "medium", fmt.Sprintf("none of the %d %s keys has an expiry, though the name suggests temporary data", p.Keys, p.Pattern),
				"Set a TTL when writing them (SET ... EX, or EXPIRE)")
		}
	}

	for i, k := range r.BigKeys {
		if i == 10 {
			break
		}
		size := fmt.Sprintf("%s, %s", bytesText(k.Bytes), plural(k.Elements, "element"))
		if k.Type == "string" {
			size = bytesText(k.Bytes)
		}
		r.add("DBS-REDIS-BIG-KEY", "medium",
			fmt.Sprintf("%s is a big %s (%s): DEL, expiry and eviction of it block the server unless lazy freeing is on, and reading it whole is slow (threshold from cloud vendors' guidance, not Redis)", k.Name, k.Type, size),
			"Split it into smaller keys, and delete it with UNLINK")
	}

	if calls := commandCalls(info, "keys"); calls > 0 {
		r.add("DBS-REDIS-KEYS-USED", "high", fmt.Sprintf("KEYS has been called %d times since the server started; each call blocks the server while it walks every key", calls),
			"Use SCAN in the application")
	}
	for _, cmd := range []string{"flushall", "flushdb"} {
		if calls := commandCalls(info, cmd); calls > 0 {
			r.add("DBS-REDIS-FLUSH-USED", "low", fmt.Sprintf("%s has been called %d times since the server started", strings.ToUpper(cmd), calls), "Use the ASYNC option so it does not block")
		}
	}

	if info.get("persistence", "rdb_last_bgsave_status") == "err" {
		r.add("DBS-REDIS-BGSAVE-FAILED", "critical", "the last background save failed; with stop-writes-on-bgsave-error (the default) the server refuses writes", "Check disk space and the server log, then save again")
	}
	if info.get("persistence", "aof_enabled") == "1" && info.get("persistence", "aof_last_write_status") == "err" {
		r.add("DBS-REDIS-AOF-FAILED", "critical", "the last append-only-file write failed", "Check disk space and the server log")
	}
	if info.get("persistence", "aof_enabled") == "0" && info.num("persistence", "rdb_last_save_time") > 0 && info.num("persistence", "rdb_changes_since_last_save") > 100000 {
		r.add("DBS-REDIS-UNSAVED", "medium", fmt.Sprintf("%.0f changes since the last snapshot, with no append-only file: a crash loses them", info.num("persistence", "rdb_changes_since_last_save")), "")
	}

	if m.Fragmentation >= 1.5 && m.FragBytes >= 100<<20 {
		r.add("DBS-REDIS-FRAGMENTATION", "medium", fmt.Sprintf("memory fragmentation is %.2f (%s wasted)", m.Fragmentation, bytesText(m.FragBytes)),
			"Enable activedefrag, or restart a replica and fail over")
	}
	if m.RSS > 0 && m.Used > 0 && float64(m.RSS) < 0.9*float64(m.Used) && m.Used > 100<<20 {
		r.add("DBS-REDIS-SWAP", "high", "resident memory is below used memory, which suggests the server is swapping and every access may hit disk", "Add memory or lower maxmemory")
	}
	if ev := info.num("stats", "evicted_keys"); ev > 0 {
		r.add("DBS-REDIS-EVICTING", "low", fmt.Sprintf("%.0f keys have been evicted since the server started", ev), "")
	}
}

// commandCalls reads calls from INFO commandstats' cmdstat_<name> line.
func commandCalls(info Info, name string) int64 {
	v := info.get("commandstats", "cmdstat_"+name)
	for _, kv := range strings.Split(v, ",") {
		if k, val, ok := strings.Cut(kv, "="); ok && k == "calls" {
			var n int64
			fmt.Sscan(val, &n)
			return n
		}
	}
	return 0
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

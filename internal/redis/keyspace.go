package redis

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Budget bounds a keyspace scan.
type Budget struct {
	Keys int           // stop after this many keys
	Time time.Duration // or after this long
}

// DefaultBudget keeps a scan to seconds on a busy server.
var DefaultBudget = Budget{Keys: 20000, Time: 10 * time.Second}

// Pattern is a group of keys whose names differ only in id-like segments.
type Pattern struct {
	Pattern string         `json:"pattern"`
	Keys    int            `json:"keys"` // scanned keys in the group
	Types   map[string]int `json:"types"`
	Bytes   int64          `json:"bytes"` // MEMORY USAGE, summed
	NoTTL   int            `json:"noTtl"` // keys without an expiry
	// Largest is the group's largest key by memory.
	Largest *Key `json:"largest,omitempty"`
}

// Key is one key's metadata.
type Key struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Bytes    int64  `json:"bytes"`
	Elements int64  `json:"elements"` // length for collections, bytes for strings
	TTLms    int64  `json:"ttlMs"`    // -1 without expiry
}

// Finding is one risk in the keyspace or the server's settings.
type Finding struct {
	RuleID          string `json:"ruleId"`
	Rating          string `json:"rating"` // low, medium, high, critical
	Message         string `json:"message"`
	SafeAlternative string `json:"safeAlternative,omitempty"`
}

// Report is the keyspace analysis the API returns.
type Report struct {
	Server   Server    `json:"server"`
	Memory   Memory    `json:"memory"`
	DBKeys   int64     `json:"dbKeys"`   // keys in the profile's database (INFO keyspace)
	DBExpire int64     `json:"dbExpire"` // of those, with an expiry
	Scanned  int       `json:"scanned"`
	Complete bool      `json:"complete"` // the scan reached the end of the keyspace
	Patterns []Pattern `json:"patterns"` // by memory, largest first
	BigKeys  []Key     `json:"bigKeys"`
	Findings []Finding `json:"findings"`
	Warnings []string  `json:"warnings"`
	TookMs   int64     `json:"tookMs"`
	At       time.Time `json:"at"` // when the scan started
}

// Memory is INFO memory's figures dbscope reports.
type Memory struct {
	Used          int64   `json:"used"`
	RSS           int64   `json:"rss"`
	Max           int64   `json:"max"` // 0: no limit
	Policy        string  `json:"policy"`
	Fragmentation float64 `json:"fragmentation"` // allocator_frag_ratio when present, else mem_fragmentation_ratio
	FragBytes     int64   `json:"fragBytes"`
}

// Big-key thresholds: Redis sets none; these follow published cloud
// guidance (docs/research-redis.md, 4.2) and are marked as practice.
const (
	bigStringBytes  = 10 << 20
	bigElements     = 10000
	bigCollectionBy = 100 << 20
)

var idSegment = regexp.MustCompile(`^(\d+|[0-9a-fA-F]{8,}|[0-9a-fA-F-]{32,36}|[A-Za-z0-9_-]{20,})$`)

// PatternOf replaces id-like segments of a key, split on ':' and '/', with
// '*': "user:1042:cart" and "user:977:cart" both become "user:*:cart".
func PatternOf(key string) string {
	var b strings.Builder
	start := 0
	for i := 0; i <= len(key); i++ {
		if i < len(key) && key[i] != ':' && key[i] != '/' {
			continue
		}
		seg := key[start:i]
		if idSegment.MatchString(seg) {
			seg = "*"
		}
		b.WriteString(seg)
		if i < len(key) {
			b.WriteByte(key[i])
		}
		start = i + 1
	}
	return b.String()
}

var lengthCommand = map[string]string{"string": "STRLEN", "list": "LLEN", "hash": "HLEN", "set": "SCARD", "zset": "ZCARD", "stream": "XLEN"}

// Analyze scans the keyspace within budget and rates what it finds.
func Analyze(ctx context.Context, c *goredis.Client, budget Budget) (*Report, error) {
	start := time.Now()
	info, err := ReadInfo(ctx, c, "server", "memory", "keyspace", "persistence", "replication", "stats", "commandstats")
	if err != nil {
		return nil, err
	}
	r := &Report{Server: describe(info), Patterns: []Pattern{}, BigKeys: []Key{}, Findings: []Finding{}, Warnings: []string{}, At: start.UTC()}
	r.Memory = Memory{Used: int64(info.num("memory", "used_memory")), RSS: int64(info.num("memory", "used_memory_rss")),
		Max: int64(info.num("memory", "maxmemory")), Policy: info.get("memory", "maxmemory_policy"),
		Fragmentation: info.num("memory", "mem_fragmentation_ratio"), FragBytes: int64(info.num("memory", "mem_fragmentation_bytes"))}
	if f := info.num("memory", "allocator_frag_ratio"); f > 0 {
		r.Memory.Fragmentation, r.Memory.FragBytes = f, int64(info.num("memory", "allocator_frag_bytes"))
	}
	db := c.Options().DB
	if v := info.get("keyspace", "db"+strconv.Itoa(db)); v != "" {
		for _, kv := range strings.Split(v, ",") {
			k, val, _ := strings.Cut(kv, "=")
			n, _ := strconv.ParseInt(val, 10, 64)
			switch k {
			case "keys":
				r.DBKeys = n
			case "expires":
				r.DBExpire = n
			}
		}
	}

	groups := map[string]*Pattern{}
	seen := map[string]bool{}
	var cursor uint64
	deadline := start.Add(budget.Time)
	var baseline time.Duration
	for {
		t0 := time.Now()
		keys, next, err := c.Scan(ctx, cursor, "", 100).Result()
		if err != nil {
			return nil, err
		}
		rtt := time.Since(t0)
		if baseline == 0 || rtt < baseline {
			baseline = rtt
		}
		var fresh []string
		for _, k := range keys {
			if !seen[k] { // SCAN may return a key twice
				seen[k] = true
				fresh = append(fresh, k)
			}
		}
		if err := r.inspect(ctx, c, fresh, groups); err != nil {
			return nil, err
		}
		cursor = next
		if cursor == 0 {
			r.Complete = true
			break
		}
		if len(seen) >= budget.Keys || time.Now().After(deadline) {
			break
		}
		// Back off while the server answers slower than at its best: it is busy.
		if rtt > 4*baseline && rtt > 2*time.Millisecond {
			time.Sleep(rtt)
		}
	}
	r.Scanned = len(seen)
	for _, g := range groups {
		r.Patterns = append(r.Patterns, *g)
	}
	sort.Slice(r.Patterns, func(i, j int) bool {
		if r.Patterns[i].Bytes != r.Patterns[j].Bytes {
			return r.Patterns[i].Bytes > r.Patterns[j].Bytes
		}
		return r.Patterns[i].Pattern < r.Patterns[j].Pattern
	})
	sort.Slice(r.BigKeys, func(i, j int) bool { return r.BigKeys[i].Bytes > r.BigKeys[j].Bytes })
	if len(r.BigKeys) > 50 {
		r.BigKeys = r.BigKeys[:50]
	}
	if !r.Complete {
		r.Warnings = append(r.Warnings, fmt.Sprintf("the scan stopped at its budget after %d of %d keys; pattern figures cover the scanned keys only", r.Scanned, r.DBKeys))
	}
	r.rate(info)
	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

// inspect reads TYPE, PTTL, MEMORY USAGE and the O(1) length of each key in
// two pipelined round trips; a key that changes type or vanishes in between
// counts as size 0.
func (r *Report) inspect(ctx context.Context, c *goredis.Client, keys []string, groups map[string]*Pattern) error {
	if len(keys) == 0 {
		return nil
	}
	pipe := c.Pipeline()
	types := make([]*goredis.StatusCmd, len(keys))
	ttls := make([]*goredis.DurationCmd, len(keys))
	mems := make([]*goredis.IntCmd, len(keys))
	for i, k := range keys {
		types[i] = pipe.Type(ctx, k)
		ttls[i] = pipe.PTTL(ctx, k)
		mems[i] = pipe.MemoryUsage(ctx, k)
	}
	pipe.Exec(ctx) // per-command errors are read below; a vanished key is not an error for the scan
	lens := make([]*goredis.Cmd, len(keys))
	pipe = c.Pipeline()
	for i, k := range keys {
		if cmd, ok := lengthCommand[types[i].Val()]; ok {
			lens[i] = pipe.Do(ctx, cmd, k)
		}
	}
	pipe.Exec(ctx)
	for i, k := range keys {
		typ := types[i].Val()
		if typ == "none" || typ == "" {
			continue
		}
		key := Key{Name: k, Type: typ, Bytes: mems[i].Val(), TTLms: -1}
		if d := ttls[i].Val(); d > 0 {
			key.TTLms = d.Milliseconds()
		}
		if lens[i] != nil {
			key.Elements, _ = lens[i].Int64()
		}
		p := PatternOf(k)
		g := groups[p]
		if g == nil {
			g = &Pattern{Pattern: p, Types: map[string]int{}}
			groups[p] = g
		}
		g.Keys++
		g.Types[typ]++
		g.Bytes += key.Bytes
		if key.TTLms < 0 {
			g.NoTTL++
		}
		if g.Largest == nil || key.Bytes > g.Largest.Bytes {
			kk := key
			g.Largest = &kk
		}
		if big(key) {
			r.BigKeys = append(r.BigKeys, key)
		}
	}
	return nil
}

func big(k Key) bool {
	if k.Type == "string" {
		return k.Bytes >= bigStringBytes
	}
	return k.Elements >= bigElements || k.Bytes >= bigCollectionBy
}

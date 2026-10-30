package redis

import (
	"context"
	"fmt"
	"math"
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
	Bytes   int64          `json:"bytes"` // MEMORY USAGE of the scanned keys, summed
	// EstimatedBytes is Bytes divided by the report's SampleFraction: the
	// whole database's keys of this pattern, if the scan saw a fair share of
	// them. Equal to Bytes after a complete scan; 0 when it cannot be estimated.
	EstimatedBytes int64 `json:"estimatedBytes"`
	NoTTL          int   `json:"noTtl"` // keys without an expiry
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
	// BytesApprox is set when Bytes is MEMORY USAGE's estimate from 5 sampled
	// elements rather than a count of all of them (see samplesAllBelow).
	BytesApprox bool `json:"bytesApprox,omitempty"`
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
	// SampleFraction is Scanned / DBKeys, at most 1, and 1 after a complete
	// scan; 0 when the database's key count is unknown.
	SampleFraction float64 `json:"sampleFraction"`
	// MemorySampled is false when the user may not run MEMORY USAGE: then
	// every bytes figure is 0 and LargestByMemory is empty.
	MemorySampled bool  `json:"memorySampled"`
	BigKeys       []Key `json:"bigKeys"` // keys over the DBS-REDIS-BIG-KEY thresholds
	// LargestByMemory and LargestByElements are the topKeys scanned keys with
	// the most bytes and, among collections, the most elements.
	LargestByMemory   []Key     `json:"largestByMemory"`
	LargestByElements []Key     `json:"largestByElements"`
	Findings          []Finding `json:"findings"`
	Warnings          []string  `json:"warnings"`
	TookMs            int64     `json:"tookMs"`
	At                time.Time `json:"at"` // when the scan started

	approx int // keys whose bytes are sampled estimates
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

// topKeys is how many keys LargestByMemory and LargestByElements keep.
const topKeys = 10

// samplesAllBelow bounds MEMORY USAGE ... SAMPLES 0, which visits every
// element of a collection (O(N), https://redis.io/docs/latest/commands/memory-usage/).
// Measured: about 0.2 ms for a 10,000-field hash on Redis 7.4 and 0.1 ms on
// Valkey 8, but 110 ms for 1,000,000 fields on Redis 7.4. Larger keys keep the
// default of 5 samples.
const samplesAllBelow = bigElements

var lengthCommand = map[string]string{"string": "STRLEN", "list": "LLEN", "hash": "HLEN", "set": "SCARD", "zset": "ZCARD", "stream": "XLEN"}

// Analyze scans the keyspace within budget and rates what it finds.
func Analyze(ctx context.Context, c *goredis.Client, budget Budget) (*Report, error) {
	start := time.Now()
	r := &Report{Patterns: []Pattern{}, MemorySampled: true, BigKeys: []Key{}, LargestByMemory: []Key{}, LargestByElements: []Key{},
		Findings: []Finding{}, Warnings: []string{}, At: start.UTC()}
	info, err := ReadInfo(ctx, c, "server", "memory", "keyspace", "persistence", "replication", "stats", "commandstats")
	switch {
	case refused(err):
		info = nil
		r.Server = hello(ctx, c)
		if n, err := c.DBSize(ctx).Result(); err == nil {
			r.DBKeys = n
		}
		r.Warnings = append(r.Warnings, "INFO is not allowed for this user (grant +info), so server memory, expiry counts and the server findings are missing")
	case err != nil:
		return nil, err
	default:
		r.Server = describe(info)
		r.readInfo(info, c.Options().DB)
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
	switch {
	case r.Complete:
		r.SampleFraction = 1
	case r.DBKeys > 0:
		r.SampleFraction = min(1, float64(r.Scanned)/float64(r.DBKeys))
	}
	for _, g := range groups {
		if r.SampleFraction > 0 {
			g.EstimatedBytes = int64(math.Round(float64(g.Bytes) / r.SampleFraction))
		}
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
	switch {
	case !r.Complete && r.SampleFraction > 0:
		r.Warnings = append(r.Warnings, fmt.Sprintf("the scan stopped at its budget after %d of %d keys (%.1f%%); pattern figures cover the scanned keys only, and estimated bytes scale them to the whole database", r.Scanned, r.DBKeys, 100*r.SampleFraction))
	case !r.Complete:
		r.Warnings = append(r.Warnings, fmt.Sprintf("the scan stopped at its budget after %d keys of an unknown total; pattern figures cover the scanned keys only", r.Scanned))
	}
	if r.approx > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s with %d or more elements were measured from MEMORY USAGE's default 5 samples, so their bytes are estimates", plural(int64(r.approx), "key"), samplesAllBelow))
	}
	r.rate(info)
	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

// readInfo takes the memory figures and the database's key counts from INFO.
func (r *Report) readInfo(info Info, db int) {
	r.Memory = Memory{Used: int64(info.num("memory", "used_memory")), RSS: int64(info.num("memory", "used_memory_rss")),
		Max: int64(info.num("memory", "maxmemory")), Policy: info.get("memory", "maxmemory_policy"),
		Fragmentation: info.num("memory", "mem_fragmentation_ratio"), FragBytes: int64(info.num("memory", "mem_fragmentation_bytes"))}
	if f := info.num("memory", "allocator_frag_ratio"); f > 0 {
		r.Memory.Fragmentation, r.Memory.FragBytes = f, int64(info.num("memory", "allocator_frag_bytes"))
	}
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
}

// inspect reads TYPE and PTTL of each key, then its O(1) length, then its
// MEMORY USAGE, each step one pipelined round trip; a key that changes type
// or vanishes in between counts as size 0. Once MEMORY USAGE is refused, it
// is not sent again.
func (r *Report) inspect(ctx context.Context, c *goredis.Client, keys []string, groups map[string]*Pattern) error {
	if len(keys) == 0 {
		return nil
	}
	pipe := c.Pipeline()
	types := make([]*goredis.StatusCmd, len(keys))
	ttls := make([]*goredis.DurationCmd, len(keys))
	for i, k := range keys {
		types[i] = pipe.Type(ctx, k)
		ttls[i] = pipe.PTTL(ctx, k)
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
	elements := make([]int64, len(keys))
	for i := range keys {
		if lens[i] != nil {
			elements[i], _ = lens[i].Int64()
		}
	}
	mems := make([]*goredis.IntCmd, len(keys))
	approx := make([]bool, len(keys))
	if r.MemorySampled {
		pipe = c.Pipeline()
		for i, k := range keys {
			switch typ := types[i].Val(); {
			case typ == "none" || typ == "":
			case typ == "string":
				mems[i] = pipe.MemoryUsage(ctx, k)
			case lens[i] != nil && elements[i] < samplesAllBelow:
				mems[i] = pipe.MemoryUsage(ctx, k, 0)
			default: // big collections and module types: the server's default sampling
				mems[i], approx[i] = pipe.MemoryUsage(ctx, k), true
			}
		}
		pipe.Exec(ctx)
		for _, m := range mems {
			if m != nil && refused(m.Err()) {
				r.MemorySampled = false
				r.Warnings = append(r.Warnings, "MEMORY USAGE is not allowed for this user (grant +memory|usage), so key and pattern sizes are missing")
				mems, r.LargestByMemory = make([]*goredis.IntCmd, len(keys)), r.LargestByMemory[:0]
				break
			}
		}
	}
	for i, k := range keys {
		typ := types[i].Val()
		if typ == "none" || typ == "" {
			continue
		}
		key := Key{Name: k, Type: typ, Elements: elements[i], TTLms: -1}
		if mems[i] != nil {
			key.Bytes, key.BytesApprox = mems[i].Val(), approx[i] && mems[i].Err() == nil
			if key.BytesApprox {
				r.approx++
			}
		}
		if d := ttls[i].Val(); d > 0 {
			key.TTLms = d.Milliseconds()
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
		if r.MemorySampled && key.Bytes > 0 {
			r.LargestByMemory = append(r.LargestByMemory, key)
		}
		if typ != "string" && key.Elements > 0 {
			r.LargestByElements = append(r.LargestByElements, key)
		}
	}
	r.LargestByMemory = top(r.LargestByMemory, func(k Key) int64 { return k.Bytes })
	r.LargestByElements = top(r.LargestByElements, func(k Key) int64 { return k.Elements })
	return nil
}

// top sorts keys by size, largest first and then by name, and keeps topKeys of them.
func top(keys []Key, size func(Key) int64) []Key {
	sort.Slice(keys, func(i, j int) bool {
		if a, b := size(keys[i]), size(keys[j]); a != b {
			return a > b
		}
		return keys[i].Name < keys[j].Name
	})
	return keys[:min(len(keys), topKeys)]
}

// refused tells whether the server refused a command under the user's ACL.
func refused(err error) bool { return err != nil && strings.Contains(err.Error(), "NOPERM") }

// hello reads the server's name and version from HELLO, for a user that may not run INFO.
func hello(ctx context.Context, c *goredis.Client) Server {
	s := Server{Flavor: "redis"}
	m, err := c.Do(ctx, "HELLO").Result()
	if err != nil {
		return s
	}
	get := func(k string) string {
		switch m := m.(type) {
		case map[any]any:
			v, _ := m[k].(string)
			return v
		case []any: // RESP2 replies with a flat list of names and values
			for i := 0; i+1 < len(m); i += 2 {
				if m[i] == k {
					v, _ := m[i+1].(string)
					return v
				}
			}
		}
		return ""
	}
	info := Info{"server": {"redis_version": get("version")}}
	if get("server") == "valkey" {
		info["server"]["server_name"], info["server"]["valkey_version"] = "valkey", get("version")
	}
	s = describe(info)
	s.Mode, s.Role = get("mode"), get("role")
	return s
}

func big(k Key) bool {
	if k.Type == "string" {
		return k.Bytes >= bigStringBytes
	}
	return k.Elements >= bigElements || k.Bytes >= bigCollectionBy
}

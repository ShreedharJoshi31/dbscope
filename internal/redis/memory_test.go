package redis

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

// minimalACL can scan but may not run INFO, MEMORY USAGE or SLOWLOG.
var minimalACL = []any{"ACL", "SETUSER", "minimal", "on", ">minimal-test", "resetkeys", "%R~*", "resetchannels", "-@all",
	"+ping", "+hello", "+dbsize", "+scan", "+type", "+pttl", "+strlen", "+llen", "+hlen", "+scard", "+zcard", "+xlen", "+client|no-touch"}

// TestKeyspaceMemory checks per-pattern memory, its extrapolation from a
// partial scan and the largest keys against MEMORY USAGE run by the admin
// user, then the same scan as a user without +memory and +info.
func TestKeyspaceMemory(t *testing.T) {
	for _, name := range []string{"redis7", "redis8", "valkey"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := mytest.Redis(t, name)
			ctx := context.Background()
			admin, err := Connect(p, "")
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			var names []string
			pipe := admin.Pipeline()
			for i := 0; i < 300; i++ {
				pipe.HSet(ctx, fmt.Sprintf("user:%d:profile", i), "name", "u", "email", strings.Repeat("e", i%50))
				pipe.Set(ctx, fmt.Sprintf("session:%08x%08x", i, i*7), strings.Repeat("t", 10+i), 0)
				pipe.Set(ctx, fmt.Sprintf("cache:page:%d", i), "html", 0)
				names = append(names, fmt.Sprintf("user:%d:profile", i), fmt.Sprintf("session:%08x%08x", i, i*7), fmt.Sprintf("cache:page:%d", i))
			}
			list := make([]any, 12000) // at the SAMPLES 0 bound: measured with default samples
			for i := range list {
				list[i] = i
			}
			pipe.RPush(ctx, "queue:jobs", list...)
			for i := 0; i < 5000; i++ { // below the bound: every field counted
				pipe.HSet(ctx, "stats:daily", fmt.Sprintf("f%d", i), i)
			}
			pipe.Set(ctx, "blob:report", strings.Repeat("x", 100000), 0)
			names = append(names, "queue:jobs", "stats:daily", "blob:report")
			if _, err := pipe.Exec(ctx); err != nil {
				t.Fatal(err)
			}

			// What dbscope should measure, by the admin user.
			want := map[string]Key{}
			wantPattern := map[string]int64{}
			for _, k := range names {
				typ := admin.Type(ctx, k).Val()
				n, _ := admin.Do(ctx, lengthCommand[typ], k).Int64()
				var b int64
				if k == "queue:jobs" {
					b = admin.MemoryUsage(ctx, k).Val()
				} else {
					b = admin.MemoryUsage(ctx, k, 0).Val()
				}
				want[k] = Key{Name: k, Type: typ, Bytes: b, Elements: n, TTLms: -1, BytesApprox: k == "queue:jobs"}
				wantPattern[PatternOf(k)] += b
			}
			var all []Key
			for _, k := range want {
				all = append(all, k)
			}
			wantByMemory := top(slices.Clone(all), func(k Key) int64 { return k.Bytes })
			var collections []Key
			for _, k := range all {
				if k.Type != "string" {
					collections = append(collections, k)
				}
			}
			wantByElements := top(collections, func(k Key) int64 { return k.Elements })

			if err := admin.Do(ctx, readOnlyACL...).Err(); err != nil {
				t.Fatal(err)
			}
			ro := p
			ro.User = "dbscope"
			c, err := Connect(ro, "dbscope-test")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			r, err := Analyze(ctx, c, DefaultBudget)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Complete || r.SampleFraction != 1 || !r.MemorySampled || r.Scanned != 903 {
				t.Fatalf("scan: complete %v fraction %v memory %v scanned %d", r.Complete, r.SampleFraction, r.MemorySampled, r.Scanned)
			}
			for _, pat := range r.Patterns {
				if pat.Bytes != wantPattern[pat.Pattern] || pat.EstimatedBytes != pat.Bytes {
					t.Errorf("pattern %s: bytes %d estimated %d, admin measured %d", pat.Pattern, pat.Bytes, pat.EstimatedBytes, wantPattern[pat.Pattern])
				}
				if pat.BytesApprox != (pat.Pattern == "queue:jobs") {
					t.Errorf("pattern %s: bytesApprox %v", pat.Pattern, pat.BytesApprox)
				}
			}
			if !slices.Equal(r.LargestByMemory, wantByMemory) {
				t.Errorf("largest by memory\n got %+v\nwant %+v", r.LargestByMemory, wantByMemory)
			}
			if !slices.Equal(r.LargestByElements, wantByElements) {
				t.Errorf("largest by elements\n got %+v\nwant %+v", r.LargestByElements, wantByElements)
			}
			if !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, "1 key with 10000 or more elements was measured") }) {
				t.Errorf("no warning about the sampled key: %v", r.Warnings)
			}
			t.Logf("%s: queue:jobs %d bytes (default samples, %d elements), stats:daily %d bytes (SAMPLES 0, %d fields), blob:report %d bytes",
				r.Server.Version, want["queue:jobs"].Bytes, want["queue:jobs"].Elements, want["stats:daily"].Bytes, want["stats:daily"].Elements, want["blob:report"].Bytes)

			// A partial scan scales each pattern by the share of keys it saw.
			part, err := Analyze(ctx, c, Budget{Keys: 200, Time: DefaultBudget.Time})
			if err != nil {
				t.Fatal(err)
			}
			frac := float64(part.Scanned) / 903
			if part.Complete || part.DBKeys != 903 || part.SampleFraction != frac {
				t.Fatalf("partial scan: complete %v keys %d scanned %d fraction %v", part.Complete, part.DBKeys, part.Scanned, part.SampleFraction)
			}
			for _, pat := range part.Patterns {
				if pat.EstimatedBytes != int64(math.Round(float64(pat.Bytes)/frac)) {
					t.Errorf("pattern %s: bytes %d estimated %d at fraction %v", pat.Pattern, pat.Bytes, pat.EstimatedBytes, frac)
				}
				if pat.Pattern == "user:*:profile" {
					off := float64(pat.EstimatedBytes)/float64(wantPattern[pat.Pattern]) - 1
					t.Logf("partial scan of %d keys (%.1f%%): user:*:profile %d keys, %d bytes, estimated %d, true %d (%+.1f%%)",
						part.Scanned, 100*frac, pat.Keys, pat.Bytes, pat.EstimatedBytes, wantPattern[pat.Pattern], 100*off)
					if math.Abs(off) > 0.35 {
						t.Errorf("estimate off by %.0f%%", 100*off)
					}
				}
			}
			if m := part.Model(0); !slices.ContainsFunc(m.Relations[0].Columns, func(c schema.Column) bool { return strings.HasPrefix(c.Type, "about ") }) {
				t.Errorf("model memory column %+v", m.Relations[0].Columns)
			}

			// Without +memory and +info the scan still runs, without sizes or server findings.
			if err := admin.Do(ctx, minimalACL...).Err(); err != nil {
				t.Fatal(err)
			}
			mu := p
			mu.User = "minimal"
			mc, err := Connect(mu, "minimal-test")
			if err != nil {
				t.Fatal(err)
			}
			defer mc.Close()
			if err := mc.MemoryUsage(ctx, "blob:report").Err(); !refused(err) {
				t.Fatalf("the minimal user could run MEMORY USAGE: %v", err)
			}
			m, err := Analyze(ctx, mc, DefaultBudget)
			if err != nil {
				t.Fatal(err)
			}
			wantFlavor := map[string]string{"redis7": "redis", "redis8": "redis", "valkey": "valkey"}[name]
			if m.MemorySampled || len(m.LargestByMemory) != 0 || m.DBKeys != 903 || m.SampleFraction != 1 || m.Server.Flavor != wantFlavor || m.Server.Version != r.Server.Version {
				t.Errorf("minimal: memory %v largest %d keys %d fraction %v server %+v", m.MemorySampled, len(m.LargestByMemory), m.DBKeys, m.SampleFraction, m.Server)
			}
			for _, pat := range m.Patterns {
				if pat.Bytes != 0 || pat.EstimatedBytes != 0 {
					t.Errorf("minimal: pattern %+v has bytes", pat)
				}
			}
			if !slices.Equal(m.LargestByElements, func() []Key {
				out := slices.Clone(wantByElements)
				for i := range out {
					out[i].Bytes, out[i].BytesApprox = 0, false
				}
				return out
			}()) {
				t.Errorf("minimal: largest by elements %+v", m.LargestByElements)
			}
			for _, grant := range []string{"+memory|usage", "+info"} {
				if !slices.ContainsFunc(m.Warnings, func(w string) bool { return strings.Contains(w, grant) }) {
					t.Errorf("minimal: no warning naming %s in %v", grant, m.Warnings)
				}
			}
			var ids []string
			for _, f := range m.Findings {
				ids = append(ids, f.RuleID)
			}
			if slices.Contains(ids, "DBS-REDIS-NO-MAXMEMORY") || !slices.Contains(ids, "DBS-REDIS-BIG-KEY") {
				t.Errorf("minimal: findings %v", ids)
			}
			sort.Strings(ids)
			t.Logf("minimal user: %d keys scanned, findings %v, warnings %q", m.Scanned, ids, m.Warnings)
		})
	}
}

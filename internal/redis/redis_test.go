package redis

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
)

// readOnlyACL is the user docs/ENGINES.md recommends: dbscope's commands and nothing else.
var readOnlyACL = []any{"ACL", "SETUSER", "dbscope", "on", ">dbscope-test", "resetkeys", "%R~*", "resetchannels", "-@all",
	"+ping", "+hello", "+info", "+dbsize", "+scan", "+type", "+pttl", "+strlen", "+llen", "+hlen", "+scard", "+zcard", "+xlen",
	"+memory|usage", "+client|no-touch"}

func TestRedis(t *testing.T) {
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
			pipe := admin.Pipeline()
			for i := 0; i < 300; i++ {
				pipe.HSet(ctx, fmt.Sprintf("user:%d:profile", i), "name", "u", "email", "e")
				pipe.Set(ctx, fmt.Sprintf("session:%08x%08x", i, i*7), "token", 0)
				pipe.Set(ctx, fmt.Sprintf("cache:page:%d", i), "html", time.Hour)
			}
			big := make([]any, 12000)
			for i := range big {
				big[i] = i
			}
			pipe.RPush(ctx, "queue:jobs", big...)
			if _, err := pipe.Exec(ctx); err != nil {
				t.Fatal(err)
			}
			admin.ConfigSet(ctx, "maxmemory-policy", "volatile-lru")
			admin.Do(ctx, "KEYS", "user:1*") // the anti-pattern the report should notice
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
			if err := c.Get(ctx, "session:00000000").Err(); err == nil || !strings.Contains(err.Error(), "NOPERM") {
				t.Errorf("the read-only user could GET: %v", err)
			}
			if err := c.Set(ctx, "x", "y", 0).Err(); err == nil {
				t.Error("the read-only user could SET")
			}

			r, err := Analyze(ctx, c, DefaultBudget)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Complete || r.Scanned != 901 || r.DBKeys != 901 || r.DBExpire != 300 {
				t.Errorf("scan: complete %v scanned %d keys %d expire %d", r.Complete, r.Scanned, r.DBKeys, r.DBExpire)
			}
			wantFlavor := map[string]string{"redis7": "redis", "redis8": "redis", "valkey": "valkey"}[name]
			if r.Server.Flavor != wantFlavor || r.Server.Num < 70000 {
				t.Errorf("server %+v", r.Server)
			}
			byPattern := map[string]Pattern{}
			for _, pat := range r.Patterns {
				byPattern[pat.Pattern] = pat
			}
			if g := byPattern["user:*:profile"]; g.Keys != 300 || g.Types["hash"] != 300 || g.NoTTL != 300 || g.Bytes == 0 {
				t.Errorf("user pattern %+v", g)
			}
			if g := byPattern["cache:page:*"]; g.Keys != 300 || g.NoTTL != 0 {
				t.Errorf("cache pattern %+v", g)
			}
			if g := byPattern["session:*"]; g.Keys != 300 {
				t.Errorf("session pattern %+v (patterns %v)", g, keys(byPattern))
			}
			if len(r.BigKeys) != 1 || r.BigKeys[0].Name != "queue:jobs" || r.BigKeys[0].Elements != 12000 {
				t.Errorf("big keys %+v", r.BigKeys)
			}
			var ids []string
			for _, f := range r.Findings {
				ids = append(ids, f.RuleID)
			}
			for _, want := range []string{"DBS-REDIS-NO-MAXMEMORY", "DBS-REDIS-BIG-KEY", "DBS-REDIS-PATTERN-NO-TTL", "DBS-REDIS-KEYS-USED"} {
				if !slices.Contains(ids, want) {
					t.Errorf("missing %s in %v", want, ids)
				}
			}
			if res := Test(ctx, ro, "dbscope-test"); !res.OK || len(res.WritePrivileges) != 0 || res.ServerVersion == "" {
				t.Errorf("test as read-only %+v", res)
			}
			if res := Test(ctx, p, ""); !res.OK || !slices.Contains(res.WritePrivileges, "SET") {
				t.Errorf("test as default %+v", res)
			}
			if res := Test(ctx, ro, "wrong"); res.OK || res.Error == nil || res.Error.Kind != "auth" {
				t.Errorf("wrong password %+v", res)
			}
		})
	}
}

func keys(m map[string]Pattern) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

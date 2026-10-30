package redis

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dbscope/dbscope/internal/conn"
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

func connect(t *testing.T, p conn.Profile, user, password string) *goredis.Client {
	t.Helper()
	p.User = user
	c, err := Connect(p, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func readHealth(t *testing.T, c *goredis.Client) *health.Report {
	t.Helper()
	r, err := Health(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// want checks a health check's status and that its text contains each of the given parts.
func want(t *testing.T, r *health.Report, id string, status health.Status, parts ...string) health.Check {
	t.Helper()
	c := healthCheck(t, r, id)
	text := c.Value + " | " + c.Detail
	if c.Status != status {
		t.Errorf("%s: status %s, want %s (%s)", id, c.Status, status, text)
	}
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Errorf("%s: %q does not contain %q", id, text, p)
		}
	}
	return c
}

// waitFor polls an INFO field until it has the value.
func waitFor(t *testing.T, c *goredis.Client, section, field, value string) {
	t.Helper()
	for i := 0; i < 60; i++ {
		info, err := ReadInfo(context.Background(), c, section)
		if err == nil && info.get(section, field) == value {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s did not become %s", field, value)
}

func randomValue(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return string(b)
}

// TestHealth triggers every check's warn or danger state on a real server,
// after reading it healthy, and reads it as users without +info and +slowlog.
func TestHealth(t *testing.T) {
	for _, name := range []string{"redis7", "redis8", "valkey"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var primaryIP string
			p := mytest.Redis(t, name, mytest.RedisIP(&primaryIP))
			admin := connect(t, p, "", "")
			for i := 0; i < 100; i++ {
				admin.Set(ctx, fmt.Sprintf("k:%d", i), "v", 0)
			}
			// The slow log's documented defaults, which the check's text states.
			if cfg := admin.ConfigGet(ctx, "slowlog-*").Val(); cfg["slowlog-log-slower-than"] != "10000" || cfg["slowlog-max-len"] != "128" {
				t.Errorf("slow log defaults %v", cfg)
			}
			admin.Do(ctx, "SLOWLOG", "RESET")
			c := connect(t, p, "", "")

			r := readHealth(t, c)
			if r.Engine != map[string]string{"redis7": "redis", "redis8": "redis", "valkey": "valkey"}[name] || r.Server == "" {
				t.Errorf("report %s %s", r.Engine, r.Server)
			}
			want(t, r, "redis.memory", health.OK, "no maxmemory", "maxmemory is 0")
			want(t, r, "redis.evictions", health.OK, "0 evicted, 0 refused for memory")
			want(t, r, "redis.connections", health.OK, " of 10000")
			want(t, r, "redis.rejected_connections", health.OK, "0")
			want(t, r, "redis.replication", health.OK, "primary, 0 replicas connected")
			want(t, r, "redis.persistence", health.OK, "last RDB save ok")
			want(t, r, "redis.slowlog", health.OK, "0 entries")
			t.Logf("%s healthy: %+v", r.Server, r.Checks)

			// Replication: a replica with its link up, then pointed at a closed port.
			rp := mytest.Redis(t, name)
			radmin := connect(t, rp, "", "")
			if err := radmin.Do(ctx, "REPLICAOF", primaryIP, "6379").Err(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, radmin, "replication", "master_link_status", "up")
			rr := readHealth(t, connect(t, rp, "", ""))
			want(t, rr, "redis.replication", health.OK, "replica of "+primaryIP+":6379, link up")
			want(t, readHealth(t, c), "redis.replication", health.OK, "primary, 1 replica connected")
			radmin.Do(ctx, "REPLICAOF", "127.0.0.1", "1")
			waitFor(t, radmin, "replication", "master_host", "127.0.0.1")
			time.Sleep(2 * time.Second)
			rr = readHealth(t, connect(t, rp, "", ""))
			ch := want(t, rr, "redis.replication", health.Danger, "replica of 127.0.0.1:1, link down")
			t.Logf("replica down: %s | %s", ch.Value, ch.Detail)

			// Slow log: every command is slow at a threshold of 0.
			admin.ConfigSet(ctx, "slowlog-log-slower-than", "0")
			admin.Ping(ctx)
			admin.ConfigSet(ctx, "slowlog-log-slower-than", "10000")
			ch = want(t, readHealth(t, c), "redis.slowlog", health.Warn)
			t.Logf("slow log: %s", ch.Value)

			// Memory near maxmemory under noeviction, then a refused write.
			admin.ConfigSet(ctx, "maxmemory-policy", "noeviction")
			info, _ := ReadInfo(ctx, admin, "memory")
			used := int64(info.num("memory", "used_memory"))
			admin.ConfigSet(ctx, "maxmemory", strconv.FormatInt(used*100/88, 10))
			ch = want(t, readHealth(t, c), "redis.memory", health.Warn, "noeviction", "writes that need memory fail")
			t.Logf("memory warn: %s", ch.Value)
			admin.ConfigSet(ctx, "maxmemory", strconv.FormatInt(used, 10))
			ch = want(t, readHealth(t, c), "redis.memory", health.Danger, "noeviction")
			t.Logf("memory danger: %s", ch.Value)
			if err := admin.Set(ctx, "big", strings.Repeat("x", 1<<20), 0).Err(); err == nil || !strings.HasPrefix(err.Error(), "OOM") {
				t.Fatalf("write at maxmemory under noeviction: %v", err)
			}
			ch = want(t, readHealth(t, c), "redis.evictions", health.Warn, "0 evicted, 1 refused for memory", "refuses writes instead of evicting")
			t.Logf("refused write: %s", ch.Value)

			// Evictions under allkeys-lru.
			admin.ConfigSet(ctx, "maxmemory-policy", "allkeys-lru")
			admin.ConfigSet(ctx, "maxmemory", strconv.FormatInt(used+(4<<20), 10))
			for i := 0; i < 6000; i++ { // one at a time: a pipeline's query buffer counts as used memory too
				admin.Set(ctx, fmt.Sprintf("fill:%d", i), strings.Repeat("y", 1000), 0)
			}
			ch = want(t, readHealth(t, c), "redis.evictions", health.Warn, "Evicted keys are gone")
			t.Logf("evictions: %s", ch.Value)
			admin.ConfigSet(ctx, "maxmemory", "0")

			// Connections at maxclients, then one more refused.
			admin.ConfigSet(ctx, "maxclients", "10")
			info, _ = ReadInfo(ctx, admin, "clients")
			var raw []net.Conn
			defer func() {
				for _, nc := range raw {
					nc.Close()
				}
			}()
			addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
			for i := int(info.num("clients", "connected_clients")); i <= 10; i++ {
				nc, err := net.Dial("tcp", addr)
				if err != nil {
					t.Fatal(err)
				}
				raw = append(raw, nc)
				nc.Write([]byte("PING\r\n"))
				line, _ := bufio.NewReader(nc).ReadString('\n')
				if i == 10 && !strings.Contains(line, "max number of clients") {
					t.Errorf("connection over maxclients: %q", line)
				}
			}
			r = readHealth(t, c)
			ch = want(t, r, "redis.connections", health.Danger, "10 of 10")
			want(t, r, "redis.rejected_connections", health.Warn, "1")
			t.Logf("connections: %s", ch.Value)
			for _, nc := range raw {
				nc.Close()
			}
			raw = nil
			admin.ConfigSet(ctx, "maxclients", "10000")

			// Users that may not run INFO or SLOWLOG get unknown checks naming the grant.
			if err := admin.Do(ctx, minimalACL...).Err(); err != nil {
				t.Fatal(err)
			}
			r = readHealth(t, connect(t, p, "minimal", "minimal-test"))
			for _, ch := range infoChecks {
				want(t, r, ch.id, health.Unknown, "+info")
			}
			want(t, r, "redis.slowlog", health.Unknown, "+slowlog|len")
			if !strings.Contains(r.Server, "8.") && !strings.Contains(r.Server, "7.") {
				t.Errorf("server from HELLO: %q", r.Server)
			}
			acl := append(append([]any{}, readOnlyACL...), "+slowlog|len")
			if err := admin.Do(ctx, acl...).Err(); err != nil {
				t.Fatal(err)
			}
			r = readHealth(t, connect(t, p, "dbscope", "dbscope-test"))
			for _, ch := range r.Checks {
				if ch.Status == health.Unknown {
					t.Errorf("read-only user: %+v", ch)
				}
			}
			if len(r.Checks) != 7 {
				t.Errorf("read-only user: %d checks", len(r.Checks))
			}
		})
	}
}

// TestHealthPersistence fills a 1 MiB data directory so that an AOF write
// fails, then turns the AOF off and lets a background save fail.
func TestHealthPersistence(t *testing.T) {
	for _, name := range []string{"redis7", "redis8", "valkey"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p := mytest.Redis(t, name, mytest.RedisArgs("--appendonly", "yes"), mytest.RedisTmpfsData("1m"))
			admin := connect(t, p, "", "")
			c := connect(t, p, "", "")
			want(t, readHealth(t, c), "redis.persistence", health.OK, "last RDB save ok, AOF writes ok")

			pipe := admin.Pipeline()
			for i := 0; i < 3000; i++ {
				pipe.Set(ctx, fmt.Sprintf("r:%d", i), randomValue(1000), 0)
			}
			pipe.Exec(ctx)
			waitFor(t, admin, "persistence", "aof_last_write_status", "err")
			if err := admin.Set(ctx, "after", "x", 0).Err(); err == nil || !strings.HasPrefix(err.Error(), "MISCONF") {
				t.Errorf("write after a failed AOF write: %v", err)
			}
			ch := want(t, readHealth(t, c), "redis.persistence", health.Danger, "append-only file write failed")
			if strings.Contains(ch.Detail, "background save") {
				t.Errorf("persistence: %s", ch.Detail)
			}
			t.Logf("AOF failed: %s", ch.Detail)

			// With the AOF off, a failed save alone refuses writes, as the check says.
			if v := admin.ConfigGet(ctx, "stop-writes-on-bgsave-error").Val()["stop-writes-on-bgsave-error"]; v != "yes" {
				t.Errorf("stop-writes-on-bgsave-error is %q", v)
			}
			if err := admin.ConfigSet(ctx, "appendonly", "no").Err(); err != nil {
				t.Fatal(err)
			}
			if err := admin.BgSave(ctx).Err(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, admin, "persistence", "rdb_last_bgsave_status", "err")
			if err := admin.Set(ctx, "after", "x", 0).Err(); err == nil || !strings.HasPrefix(err.Error(), "MISCONF") {
				t.Errorf("write after a failed save: %v", err)
			}
			ch = want(t, readHealth(t, c), "redis.persistence", health.Danger, "background save (RDB snapshot) failed", "refuses writes")
			t.Logf("RDB failed: %s", ch.Detail)
			if strings.Contains(ch.Detail, "append-only") {
				t.Errorf("persistence with the AOF off: %s", ch.Detail)
			}
		})
	}
}

// Package redis analyzes Redis and Valkey keyspaces for dbscope
// (docs/ENGINES.md): memory and expiry by key pattern, big keys and server
// risks, read with SCAN and per-key metadata only, never values.
package redis

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dbscope/dbscope/internal/conn"
)

// Connect returns a client for the profile's database (Profile.Database is
// its number, default 0). Each connection turns CLIENT NO-TOUCH on, so
// dbscope's reads do not change keys' LRU or LFU data; servers before 7.2
// reject it, which is ignored.
func Connect(p conn.Profile, password string) (*goredis.Client, error) {
	db := 0
	if p.Database != "" {
		n, err := strconv.Atoi(p.Database)
		if err != nil || n < 0 {
			return nil, errors.New("the database of a Redis profile is its number, such as 0")
		}
		db = n
	}
	o := &goredis.Options{
		Addr: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), Username: p.User, Password: password, DB: db,
		DialTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, MaxRetries: 0, PoolSize: 2,
		DisableIdentity: true, // CLIENT SETINFO needs a grant the read-only user does not have
		OnConnect: func(ctx context.Context, cn *goredis.Conn) error {
			cn.Do(ctx, "CLIENT", "NO-TOUCH", "ON")
			return nil
		},
	}
	cfgs, err := conn.TLSConfigs(p)
	if err != nil {
		return nil, err
	}
	if cfgs[0] != nil && len(cfgs) == 1 { // Redis has no "TLS if offered": prefer means plaintext
		o.TLSConfig = cfgs[0]
	}
	return goredis.NewClient(o), nil
}

// Server is what INFO says about the server.
type Server struct {
	// Flavor is "redis" or "valkey"; Valkey reports redis_version 7.2.4, so server_name decides.
	Flavor  string `json:"flavor"`
	Version string `json:"version"`
	Num     int    `json:"versionNum"` // major*10000 + minor*100 + patch
	Mode    string `json:"mode"`       // standalone, cluster, sentinel
	Role    string `json:"role"`
}

// Info is INFO's output as section → field → value; unknown fields are kept,
// missing ones are absent.
type Info map[string]map[string]string

func (i Info) get(section, field string) string { return i[section][field] }

func (i Info) num(section, field string) float64 {
	v, _ := strconv.ParseFloat(i[section][field], 64)
	return v
}

// ReadInfo runs INFO for the given sections and parses it leniently.
func ReadInfo(ctx context.Context, c *goredis.Client, sections ...string) (Info, error) {
	text, err := c.Info(ctx, sections...).Result()
	if err != nil {
		return nil, err
	}
	out := Info{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "# "):
			section = strings.ToLower(strings.TrimPrefix(line, "# "))
			out[section] = map[string]string{}
		case line == "" || section == "":
		default:
			if k, v, ok := strings.Cut(line, ":"); ok {
				out[section][k] = v
			}
		}
	}
	return out, nil
}

func describe(info Info) Server {
	s := Server{Flavor: "redis", Version: info.get("server", "redis_version"), Mode: info.get("server", "redis_mode"), Role: info.get("replication", "role")}
	if info.get("server", "server_name") == "valkey" || info.get("server", "valkey_version") != "" {
		s.Flavor, s.Version = "valkey", info.get("server", "valkey_version")
	}
	var a, b, d int
	parts := strings.SplitN(s.Version, ".", 3)
	if len(parts) == 3 {
		a, _ = strconv.Atoi(parts[0])
		b, _ = strconv.Atoi(parts[1])
		d, _ = strconv.Atoi(parts[2])
	}
	s.Num = a*10000 + b*100 + d
	return s
}

// writeCommands are commands whose availability means the user can change data or settings.
var writeCommands = []string{"set", "del", "flushall", "config"}

// Test connects with the profile and reports the server and whether the user
// can write, checked with ACL DRYRUN (Redis 7.0+) when the user may run it.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineRedis, Warnings: []string{}}
	if p.SSLMode == conn.SSLDisable && p.Host != "localhost" && p.Host != "127.0.0.1" && p.Host != "::1" {
		res.Warnings = append(res.Warnings, "sslMode is disable on a non-loopback host, so the password and results travel unencrypted.")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := Connect(p, password)
	if err != nil {
		res.Error = &conn.TestError{Kind: "other", Message: err.Error()}
		return res
	}
	defer c.Close()
	start := time.Now()
	if err := c.Ping(ctx).Err(); err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs = true, &ms
	info, err := ReadInfo(ctx, c, "server", "replication")
	if err != nil {
		res.Warnings = append(res.Warnings, "INFO is not allowed for this user, so server facts and most findings are unavailable: "+err.Error())
		return res
	}
	srv := describe(info)
	res.ServerVersion, res.ServerVersionNum = srv.Version, srv.Num
	if srv.Flavor == "valkey" {
		res.Engine = conn.EngineValkey
	}
	replica := srv.Role == "slave" || srv.Role == "replica"
	res.IsReplica = &replica
	// ACL DRYRUN tells whether a user may run a command without running it (Redis 7.0+).
	user := p.User
	if user == "" {
		user = "default"
	}
	checked := true
	for _, cmd := range writeCommands {
		args := []any{"ACL", "DRYRUN", user, cmd}
		switch cmd {
		case "set":
			args = append(args, "dbscope-probe", "x")
		case "del":
			args = append(args, "dbscope-probe")
		case "config":
			args = append(args, "SET", "maxmemory", "0")
		}
		v, err := c.Do(ctx, args...).Text()
		if err != nil && !strings.Contains(err.Error(), "has no permissions") {
			checked = false // DRYRUN itself is an admin command a restricted user cannot run
			break
		}
		if err == nil && strings.EqualFold(v, "OK") && len(res.WritePrivileges) < 20 {
			res.WritePrivileges = append(res.WritePrivileges, strings.ToUpper(cmd))
		}
	}
	if !checked {
		res.Warnings = append(res.Warnings, "dbscope could not check whether this user can write (ACL DRYRUN is not allowed); a user limited to dbscope's read commands is recommended (docs/ENGINES.md)")
	}
	return res
}

func classify(err error) *conn.TestError {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "WRONGPASS"), strings.Contains(msg, "NOAUTH"), strings.Contains(msg, "NOPERM"):
		return &conn.TestError{Kind: "auth", Message: msg}
	case errors.Is(err, context.DeadlineExceeded):
		return &conn.TestError{Kind: "timeout", Message: msg}
	}
	return conn.Classify(err)
}

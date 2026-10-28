package mongo

import (
	"context"
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/dbscope/dbscope/internal/conn"
)

// Connect returns a client for the profile. Credentials are set as options,
// never in the URI, and reads prefer secondaries so a production primary is
// left alone where a replica can answer. Timeouts come from each call's
// context, which the driver also sends to the server as maxTimeMS.
func Connect(p conn.Profile, password string) (*mongo.Client, error) {
	q, err := url.ParseQuery(p.Options)
	if err != nil {
		return nil, err
	}
	srv := q.Get("srv") == "true"
	q.Del("srv") // dbscope's own: use the mongodb+srv scheme
	host := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	scheme := "mongodb"
	if srv {
		scheme, host = "mongodb+srv", p.Host
	}
	authSource := q.Get("authSource")
	q.Del("authSource")
	u := url.URL{Scheme: scheme, Host: host, Path: "/" + p.Database, RawQuery: q.Encode()}
	o := options.Client().ApplyURI(u.String()).
		SetAppName("dbscope").
		SetReadPreference(readpref.SecondaryPreferred()).
		SetRetryWrites(false).
		SetServerSelectionTimeout(10 * time.Second).
		SetConnectTimeout(10 * time.Second)
	if p.User != "" {
		if authSource == "" {
			authSource = "admin"
		}
		o.SetAuth(options.Credential{Username: p.User, Password: password, AuthSource: authSource})
	}
	// mongodb+srv implies TLS; for the rest sslMode decides, as for the other engines.
	if !srv || p.SSLMode != conn.SSLPrefer {
		cfgs, err := conn.TLSConfigs(p)
		if err != nil {
			return nil, err
		}
		if cfgs[0] != nil && len(cfgs) == 1 { // MongoDB has no "use TLS if offered", so prefer means plaintext
			o.SetTLSConfig(cfgs[0])
		}
	}
	return mongo.Connect(o)
}

// Server is what buildInfo says about the server.
type Server struct {
	Version string
	Num     int // major*10000 + minor*100 + patch
}

func describe(ctx context.Context, c *mongo.Client, db string) (Server, error) {
	var info struct {
		Version string `bson:"version"`
	}
	if err := c.Database(db).RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info); err != nil {
		return Server{}, err
	}
	var a, b, d int
	parse := func(s string) int { n, _ := strconv.Atoi(s); return n }
	parts := strings.SplitN(info.Version, ".", 3)
	if len(parts) == 3 {
		a, b, d = parse(parts[0]), parse(parts[1]), parse(strings.SplitN(parts[2], "-", 2)[0])
	}
	return Server{Version: info.Version, Num: a*10000 + b*100 + d}, nil
}

// readRoles are roles that cannot write data or schema.
var readRoles = []string{"read", "readAnyDatabase", "clusterMonitor"}

// Test connects with the profile and reports the server and any roles that can write.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineMongoDB, Warnings: []string{}}
	if p.SSLMode == conn.SSLDisable && p.Host != "localhost" && p.Host != "127.0.0.1" && p.Host != "::1" {
		res.Warnings = append(res.Warnings, "sslMode is disable on a non-loopback host, so the password and query results travel unencrypted.")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := Connect(p, password)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	defer c.Disconnect(context.Background())
	start := time.Now()
	srv, err := describe(ctx, c, p.Database)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs, res.ServerVersion, res.ServerVersionNum = true, &ms, srv.Version, srv.Num
	var status struct {
		AuthInfo struct {
			Roles []struct {
				Role string `bson:"role"`
				DB   string `bson:"db"`
			} `bson:"authenticatedUserRoles"`
		} `bson:"authInfo"`
	}
	if err := c.Database(p.Database).RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&status); err == nil {
		for _, r := range status.AuthInfo.Roles {
			if !slices.Contains(readRoles, r.Role) && len(res.WritePrivileges) < 20 {
				res.WritePrivileges = append(res.WritePrivileges, r.Role+" ON "+r.DB)
			}
		}
	}
	return res
}

func classify(err error) *conn.TestError {
	var ce mongo.CommandError
	switch {
	case errors.As(err, &ce) && (ce.Code == 18 || ce.Code == 13): // AuthenticationFailed, Unauthorized
		return &conn.TestError{Kind: "auth", Message: ce.Message}
	case errors.As(err, &ce):
		return &conn.TestError{Kind: "database", Message: ce.Message}
	case mongo.IsTimeout(err), errors.Is(err, context.DeadlineExceeded):
		return &conn.TestError{Kind: "timeout", Message: err.Error()}
	case strings.Contains(err.Error(), "auth error"), strings.Contains(err.Error(), "AuthenticationFailed"):
		return &conn.TestError{Kind: "auth", Message: err.Error()}
	}
	return conn.Classify(err)
}

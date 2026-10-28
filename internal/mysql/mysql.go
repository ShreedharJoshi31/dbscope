package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/dbscope/dbscope/internal/awsauth"
	"github.com/dbscope/dbscope/internal/conn"
)

// Server describes the server a pool reaches.
type Server struct {
	// Flavor is "mysql" or "mariadb".
	Flavor string
	// Version is the version string without its suffix, e.g. "8.4.3" or "11.8.2".
	Version string
	// Num is major*10000 + minor*100 + patch, e.g. 80403.
	Num int
}

// Open returns a pool for the profile. Nothing is read until first use.
func Open(p conn.Profile, password string, timeout time.Duration) (*sql.DB, error) {
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = p.User, password, p.Database
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	cfg.Timeout, cfg.ReadTimeout = 10*time.Second, timeout+10*time.Second
	cfg.ParseTime = true
	cfg.MultiStatements = false // one statement per call: a read-only transaction holds only if nothing can commit it
	tlsCfgs, err := conn.TLSConfigs(p)
	if err != nil {
		return nil, err
	}
	switch {
	case tlsCfgs[0] == nil:
		cfg.TLSConfig = "false"
	case len(tlsCfgs) > 1: // prefer: TLS when the server offers it
		cfg.TLSConfig = "preferred"
	default:
		cfg.TLS = tlsCfgs[0]
	}
	if s := awsauth.FromOptions(p.Options); s.Auth == "rds-iam" {
		// RDS sends the token through mysql_clear_password, which validation allows only over TLS.
		cfg.AllowCleartextPasswords = true
		if err := cfg.Apply(mysql.BeforeConnect(func(ctx context.Context, c *mysql.Config) error {
			aws, err := awsauth.Load(ctx, s)
			if err != nil {
				return err
			}
			c.Passwd, err = awsauth.RDSToken(ctx, aws, p.Host, p.Port, p.User)
			return err
		})); err != nil {
			return nil, err
		}
	}
	c, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

// Describe reads the server's flavor and version.
func Describe(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Server, error) {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&v); err != nil {
		return Server{}, err
	}
	return parseVersion(v), nil
}

// parseVersion reads "8.4.3", "8.0.36-log" or "11.8.2-MariaDB-ubu2404".
func parseVersion(v string) Server {
	s := Server{Flavor: "mysql"}
	if strings.Contains(strings.ToLower(v), "mariadb") {
		s.Flavor = "mariadb"
	}
	// MariaDB 10 replication servers once reported "5.5.5-10.x"; the real version follows.
	v = strings.TrimPrefix(v, "5.5.5-")
	s.Version, _, _ = strings.Cut(v, "-")
	var major, minor, patch int
	fmt.Sscanf(s.Version, "%d.%d.%d", &major, &minor, &patch)
	s.Num = major*10000 + minor*100 + patch
	return s
}

// systemSchemas are the server's own databases.
var systemSchemas = []string{"information_schema", "mysql", "performance_schema", "sys"}

func isSystem(schema string) bool {
	for _, s := range systemSchemas {
		if strings.EqualFold(s, schema) {
			return true
		}
	}
	return false
}

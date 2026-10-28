// Package oracle reads Oracle Database schemas for dbscope (docs/ENGINES.md):
// the ERD from the ALL_ dictionary views with design risks as findings, and
// read-only queries in a READ ONLY transaction behind a plan and PL/SQL gate.
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/dbscope/dbscope/internal/conn"
)

// Server describes the server a pool reaches.
type Server struct {
	// Version is the full version, e.g. "23.26.3.0.0".
	Version string
	// Num is major*10000 + minor*100, e.g. 232600.
	Num int
}

// Open returns a pool for the profile, whose database is the service name
// (FREEPDB1, ORCLPDB1). Oracle's TLS listener (TCPS) is a port of its own
// with no "TLS if offered", so sslMode disable and prefer are plaintext.
func Open(p conn.Profile, password string) (*sql.DB, error) {
	opts := map[string]string{"TIMEOUT": "0", "PROGRAM": "dbscope"}
	cfgs, err := conn.TLSConfigs(p)
	if err != nil {
		return nil, err
	}
	tlsOn := p.SSLMode != conn.SSLDisable && p.SSLMode != conn.SSLPrefer
	if tlsOn {
		opts["SSL"] = "true"
		opts["SSL VERIFY"] = strconv.FormatBool(!cfgs[0].InsecureSkipVerify)
	}
	c := go_ora.NewConnector(go_ora.BuildUrl(p.Host, p.Port, p.Database, p.User, password, opts)).(*go_ora.OracleConnector)
	if tlsOn {
		c.WithTLSConfig(cfgs[0])
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

// Describe reads the server's version.
func Describe(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Server, error) {
	var s Server
	if err := q.QueryRowContext(ctx, `SELECT version_full FROM product_component_version WHERE rownum = 1`).Scan(&s.Version); err != nil {
		return Server{}, err
	}
	var major, minor int
	fmt.Sscanf(s.Version, "%d.%d", &major, &minor)
	s.Num = major*10000 + minor*100
	return s, nil
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

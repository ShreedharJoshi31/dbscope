// Package mssql reads Microsoft SQL Server and Azure SQL databases for
// dbscope (docs/ENGINES.md): the ERD from the sys catalog views with design
// risks as findings, and read-only queries gated by their estimated plan.
package mssql

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"strconv"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/dbscope/dbscope/internal/conn"
)

// Server describes the server a pool reaches.
type Server struct {
	// Version is SERVERPROPERTY('ProductVersion'), e.g. "16.0.4305.5".
	Version string
	// Num is major*10000 + minor*100, e.g. 160000 for SQL Server 2022.
	Num int
	// Edition is SERVERPROPERTY('Edition'), e.g. "Developer Edition (64-bit)".
	Edition string
	// Azure is true for Azure SQL Database (EngineEdition 5), which has no server-level catalog.
	Azure bool
}

// Open returns a pool for the profile. Nothing is read until first use.
//
// TDS always encrypts the login packet, so sslMode disable still sends the
// password over TLS and only the session is plaintext; prefer and require
// encrypt the session without checking the certificate, as libpq does.
func Open(p conn.Profile, password string) (*sql.DB, error) {
	q := url.Values{"app name": {"dbscope"}}
	if p.Database != "" {
		q.Set("database", p.Database)
	}
	u := url.URL{Scheme: "sqlserver", User: url.UserPassword(p.User, password),
		Host: p.Host + ":" + strconv.Itoa(p.Port), RawQuery: q.Encode()}
	cfg, err := msdsn.Parse(u.String())
	if err != nil {
		return nil, fmt.Errorf("sql server settings: %w", err)
	}
	cfgs, err := conn.TLSConfigs(p)
	if err != nil {
		return nil, err
	}
	switch {
	case cfgs[0] == nil:
		cfg.Encryption = msdsn.EncryptionOff
		cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true, ServerName: p.Host} // the login packet only
	default:
		cfg.Encryption = msdsn.EncryptionRequired
		cfg.TLSConfig = cfgs[0]
	}
	cfg.DialTimeout = 10 * time.Second
	db := sql.OpenDB(mssql.NewConnectorConfig(cfg))
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

// Describe reads the server's version and edition.
func Describe(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Server, error) {
	var s Server
	var engineEdition int
	err := q.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)),
		CAST(SERVERPROPERTY('Edition') AS nvarchar(128)), CAST(SERVERPROPERTY('EngineEdition') AS int)`).
		Scan(&s.Version, &s.Edition, &engineEdition)
	if err != nil {
		return Server{}, err
	}
	var major, minor int
	fmt.Sscanf(s.Version, "%d.%d", &major, &minor)
	s.Num = major*10000 + minor*100
	s.Azure = engineEdition == 5
	return s, nil
}

// discard closes c instead of returning it to the pool, for a session whose
// SET options could not be put back.
func discard(c *sql.Conn) {
	c.Raw(func(any) error { return driver.ErrBadConn })
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

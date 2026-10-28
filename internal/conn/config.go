package conn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// baseConnString pins every setting pgx would otherwise take from PG*
// environment variables or from ~/.pgpass and ~/.postgresql, so a profile
// connects exactly as configured. The host is a placeholder that can never
// resolve; configure overwrites it and every other profile field on the
// parsed struct, so no user input is ever formatted into a string.
const baseConnString = "host=dbscope.invalid port=1 dbname=postgres user=postgres passfile='' " +
	"sslmode=disable sslcert='' sslkey='' sslrootcert='' sslpassword='' sslsni=1 sslnegotiation=postgres " +
	"connect_timeout=10 target_session_attrs=any min_protocol_version=3.0 max_protocol_version=3.0 " +
	"channel_binding=prefer require_auth=''"

// errTLSSetup marks failures preparing TLS (e.g. unreadable sslRootCert) so
// they classify as "tls" rather than "other".
var errTLSSetup = errors.New("tls setup")

func connConfig(p Profile, password string) (*pgx.ConnConfig, error) {
	cc, err := pgx.ParseConfig(baseConnString)
	if err != nil {
		return nil, err
	}
	return cc, configure(cc, p, password)
}

// NewPool opens a small pool for a profile. Cancelling a query's context
// sends a server-side cancel immediately and closes the socket 5s later if
// the server has not responded.
func NewPool(ctx context.Context, p Profile, password string) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(baseConnString)
	if err != nil {
		return nil, err
	}
	if err := configure(pc.ConnConfig, p, password); err != nil {
		return nil, err
	}
	pc.MaxConns = 2
	return pgxpool.NewWithConfig(ctx, pc)
}

func configure(cc *pgx.ConnConfig, p Profile, password string) error {
	tlsConfigs, err := tlsConfigsFor(p)
	if err != nil {
		return err
	}
	port := uint16(p.Port)
	cc.Host = p.Host
	cc.Port = port
	cc.Database = p.Database
	cc.User = p.User
	cc.Password = password
	cc.TLSConfig = tlsConfigs[0]
	cc.Fallbacks = nil
	for _, t := range tlsConfigs[1:] {
		cc.Fallbacks = append(cc.Fallbacks, &pgconn.FallbackConfig{Host: p.Host, Port: port, TLSConfig: t})
	}
	// Only parameters PgBouncer tracks: by default it rejects others
	// (statement_timeout among them). client_encoding must be explicit because a
	// non-UTF8 database would otherwise send results pgx decodes as UTF-8.
	cc.RuntimeParams = map[string]string{"application_name": "dbscope", "client_encoding": "UTF8"}
	cc.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, CancelRequestDelay: 0, DeadlineDelay: 5 * time.Second}
	}
	return nil
}

// TLSConfigs is tlsConfigsFor for other engines' drivers, so a profile's
// sslMode means the same thing whatever the engine.
func TLSConfigs(p Profile) ([]*tls.Config, error) { return tlsConfigsFor(p) }

// tlsConfigsFor mirrors libpq's sslmode semantics as implemented by pgconn:
// the first config is tried first, later ones are fallbacks (nil = plaintext).
func tlsConfigsFor(p Profile) ([]*tls.Config, error) {
	if p.SSLMode == SSLDisable {
		return []*tls.Config{nil}, nil
	}
	cfg := &tls.Config{}
	if net.ParseIP(p.Host) == nil {
		cfg.ServerName = p.Host // SNI; also the name checked by verify-full
	}
	if p.SSLRootCert != "" {
		pem, err := os.ReadFile(p.SSLRootCert)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot read sslRootCert: %w", errTLSSetup, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%w: sslRootCert contains no PEM certificates", errTLSSetup)
		}
		cfg.RootCAs = pool
	}
	mode := p.SSLMode
	// libpq: require with a root certificate behaves like verify-ca.
	if mode == SSLRequire && p.SSLRootCert != "" {
		mode = SSLVerifyCA
	}
	switch mode {
	case SSLPrefer:
		cfg.InsecureSkipVerify = true
		return []*tls.Config{cfg, nil}, nil
	case SSLRequire:
		cfg.InsecureSkipVerify = true
	case SSLVerifyCA:
		// Verify the chain but not the hostname; crypto/tls has no such mode.
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("tls: server sent no certificate")
			}
			certs := make([]*x509.Certificate, len(raw))
			for i, b := range raw {
				c, err := x509.ParseCertificate(b)
				if err != nil {
					return fmt.Errorf("tls: bad server certificate: %w", err)
				}
				certs[i] = c
			}
			opts := x509.VerifyOptions{Roots: cfg.RootCAs, Intermediates: x509.NewCertPool()}
			for _, c := range certs[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := certs[0].Verify(opts)
			return err
		}
	case SSLVerifyFull:
		cfg.ServerName = p.Host
	}
	return []*tls.Config{cfg}, nil
}

package conn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type TestError struct {
	Kind     string `json:"kind"` // auth | network | tls | timeout | database | other
	Message  string `json:"message"`
	SQLState string `json:"sqlstate,omitempty"`
}

type TestResult struct {
	OK               bool       `json:"ok"`
	LatencyMs        *int64     `json:"latencyMs,omitempty"`
	ServerVersion    string     `json:"serverVersion,omitempty"`
	ServerVersionNum int        `json:"serverVersionNum,omitempty"`
	IsReplica        *bool      `json:"isReplica,omitempty"`
	DefaultReadOnly  *bool      `json:"defaultReadOnly,omitempty"`
	WritePrivileges  []string   `json:"writePrivileges,omitzero"`
	PgStatStatements *bool      `json:"pgStatStatements,omitempty"`
	Warnings         []string   `json:"warnings"`
	Error            *TestError `json:"error,omitempty"`
}

const (
	queryTimeout  = 10 * time.Second
	maxPrivileges = 20
)

const notSystemSchema = `n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'`

// Test connects with the profile and reports what dbscope needs to know
// before using it. It only runs catalog queries. Failures to connect are
// reported in the result, not as an error.
func Test(ctx context.Context, p Profile, password string) TestResult {
	res := TestResult{Warnings: []string{}}
	if p.SSLMode == SSLDisable && !isLoopback(p.Host) {
		res.Warnings = append(res.Warnings, "sslMode is disable on a non-loopback host, so the password and query results travel unencrypted.")
	}
	cc, err := connConfig(p, password)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	// The simple protocol works through every pooler, including PgBouncer in transaction mode.
	cc.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	start := time.Now()
	c, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	latency := time.Since(start).Milliseconds()
	res.LatencyMs = &latency
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		c.Close(closeCtx)
	}()

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var version string
	var replica, readOnly bool
	err = c.QueryRow(ctx, `SELECT current_setting('server_version'), current_setting('server_version_num')::int,
		pg_is_in_recovery(), current_setting('default_transaction_read_only')::bool`).
		Scan(&version, &res.ServerVersionNum, &replica, &readOnly)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	// Distribution builds append e.g. " (Debian 17.2-1.pgdg120+1)".
	res.ServerVersion, _, _ = strings.Cut(version, " ")
	res.IsReplica = &replica
	res.DefaultReadOnly = &readOnly
	res.OK = true

	if privs, err := writePrivileges(ctx, c); err != nil {
		res.Warnings = append(res.Warnings, "Could not check write privileges: "+classify(err).Message)
	} else {
		res.WritePrivileges = privs
	}
	if ok, err := pgStatStatements(ctx, c); err != nil {
		res.Warnings = append(res.Warnings, "Could not check pg_stat_statements: "+classify(err).Message)
	} else {
		res.PgStatStatements = &ok
	}
	// A prod-replica that is a primary, or a role that can write, is already
	// reported through IsReplica and WritePrivileges; the UI warns from those.
	return res
}

// writePrivileges lists role attributes and grants that allow changing the
// database. An empty list means the role can only read.
func writePrivileges(ctx context.Context, c *pgx.Conn) ([]string, error) {
	var out []string
	more := 0
	add := func(s string) {
		if len(out) < maxPrivileges {
			out = append(out, s)
		} else {
			more++
		}
	}

	var super, createRole, createDB bool
	err := c.QueryRow(ctx, `SELECT rolsuper, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = current_user`).
		Scan(&super, &createRole, &createDB)
	if err != nil {
		return nil, err
	}
	for _, a := range []struct {
		set  bool
		name string
	}{{super, "SUPERUSER"}, {createRole, "CREATEROLE"}, {createDB, "CREATEDB"}} {
		if a.set {
			add(a.name)
		}
	}

	for _, q := range []string{
		`SELECT format('CREATE on schema %I', n.nspname)
		 FROM pg_namespace n
		 WHERE ` + notSystemSchema + ` AND has_schema_privilege(n.oid, 'CREATE')
		 ORDER BY n.nspname`,
		`SELECT format('%s on %I.%I', string_agg(p.priv, ', ' ORDER BY p.ord), n.nspname, c.relname)
		 FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 CROSS JOIN unnest(ARRAY['INSERT', 'UPDATE', 'DELETE', 'TRUNCATE']) WITH ORDINALITY AS p(priv, ord)
		 WHERE c.relkind IN ('r', 'p', 'v', 'f') AND ` + notSystemSchema + ` AND has_table_privilege(c.oid, p.priv)
		 GROUP BY n.nspname, c.relname
		 ORDER BY n.nspname, c.relname`,
	} {
		rows, err := c.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		var s string
		_, err = pgx.ForEachRow(rows, []any{&s}, func() error { add(s); return nil })
		if err != nil {
			return nil, err
		}
	}

	if more > 0 {
		out = append(out, fmt.Sprintf("... and %d more", more))
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// pgStatStatements reports whether the extension is installed and its view
// can be read (it errors when the library is not preloaded).
func pgStatStatements(ctx context.Context, c *pgx.Conn) (bool, error) {
	var schema string
	err := c.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_stat_statements'`).Scan(&schema)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = c.Exec(ctx, "SELECT 1 FROM "+pgx.Identifier{schema, "pg_stat_statements"}.Sanitize()+" LIMIT 1")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return false, nil
	}
	return err == nil, err
}

func classify(err error) *TestError {
	msg := err.Error()
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) && ce.Unwrap() != nil {
		msg = ce.Unwrap().Error() // drop pgconn's "failed to connect to `user=... database=...`" prefix
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		kind := "database"
		if pgErr.Code == "28P01" || pgErr.Code == "28000" {
			kind = "auth"
		}
		return &TestError{Kind: kind, Message: pgErr.Message, SQLState: pgErr.Code}
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) || isNetTimeout(err) {
		return &TestError{Kind: "timeout", Message: msg}
	}
	if isTLSError(err) {
		return &TestError{Kind: "tls", Message: msg}
	}
	var netErr net.Error
	var opErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &netErr) || errors.As(err, &opErr) || errors.As(err, &dnsErr) {
		return &TestError{Kind: "network", Message: msg}
	}
	return &TestError{Kind: "other", Message: msg}
}

func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isTLSError(err error) bool {
	var (
		verifyErr   *tls.CertificateVerificationError
		recordErr   tls.RecordHeaderError
		unknownAuth x509.UnknownAuthorityError
		hostErr     x509.HostnameError
		invalidErr  x509.CertificateInvalidError
	)
	if errors.Is(err, errTLSSetup) || errors.As(err, &verifyErr) || errors.As(err, &recordErr) ||
		errors.As(err, &unknownAuth) || errors.As(err, &hostErr) || errors.As(err, &invalidErr) {
		return true
	}
	// Remote TLS alerts and pgconn's "server refused TLS connection" have no exported type.
	msg := err.Error()
	return strings.Contains(msg, "tls:") || strings.Contains(msg, "refused TLS")
}

// Classify sorts a connection or query error into the TestError kinds, so
// other packages report failures the same way the connection test does.
func Classify(err error) *TestError { return classify(err) }

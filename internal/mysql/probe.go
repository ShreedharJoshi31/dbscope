package mysql

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/dbscope/dbscope/internal/conn"
)

// Test connects with the profile and reports the server, whether the session
// starts read-only, and write privileges dbscope does not need.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineMySQL, Warnings: []string{}}
	if p.SSLMode == conn.SSLDisable && !loopback(p.Host) {
		res.Warnings = append(res.Warnings, "sslMode is disable on a non-loopback host, so the password and query results travel unencrypted.")
	}
	db, err := Open(p, password, 10*time.Second)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	srv, err := Describe(ctx, db)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs, res.ServerVersion, res.ServerVersionNum = true, &ms, srv.Version, srv.Num
	if srv.Flavor == "mariadb" {
		res.Engine = conn.EngineMariaDB
	}
	var ro int
	if err := db.QueryRowContext(ctx, `SELECT @@global.read_only`).Scan(&ro); err == nil {
		v := ro == 1
		res.DefaultReadOnly = &v
	}
	if privs, err := writePrivileges(ctx, db); err == nil {
		res.WritePrivileges = privs
	}
	return res
}

var grantPrivs = regexp.MustCompile(`(?i)^GRANT (.+?) ON (\S+) TO`)

// writePrivileges reads SHOW GRANTS for privileges that change data or schema.
func writePrivileges(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SHOW GRANTS`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	write := []string{"ALL PRIVILEGES", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX", "FILE", "SUPER"}
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		m := grantPrivs.FindStringSubmatch(g)
		if m == nil {
			continue
		}
		for _, p := range strings.Split(m[1], ",") {
			p = strings.ToUpper(strings.TrimSpace(p))
			if slices.Contains(write, p) && len(out) < 20 {
				out = append(out, p+" ON "+m[2])
			}
		}
	}
	return out, rows.Err()
}

func classify(err error) *conn.TestError {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		kind := "database"
		if me.Number == 1045 || me.Number == 1044 || me.Number == 1698 { // access denied
			kind = "auth"
		}
		return &conn.TestError{Kind: kind, Message: me.Message, SQLState: string(me.SQLState[:])}
	}
	te := conn.Classify(err)
	return te
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

package oracle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sijms/go-ora/v2/network"

	"github.com/dbscope/dbscope/internal/conn"
)

// Test connects with the profile and reports the server, system and object
// privileges that write, tables the user owns (an owner can always write
// them), and autonomous-transaction PL/SQL it can execute.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineOracle, Warnings: []string{}}
	if (p.SSLMode == conn.SSLDisable || p.SSLMode == conn.SSLPrefer) && !loopback(p.Endpoint().Host) {
		res.Warnings = append(res.Warnings, "The connection is not encrypted (Oracle's TLS listener is a port of its own, usually 2484), "+
			"so the password exchange is protected by Oracle's own verifier but results travel in plain text; use sslMode require or verify-full against the TCPS port.")
	}
	db, err := Open(p, password)
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
	res.WritePrivileges = []string{} // checked: an empty list reads as "none"

	collect := func(q string, format func(a, b string) string) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() && len(res.WritePrivileges) < 20 {
			var a, b string
			if rows.Scan(&a, &b) == nil {
				res.WritePrivileges = append(res.WritePrivileges, format(a, b))
			}
		}
	}
	collect(`SELECT privilege, 'system' FROM session_privs
		WHERE privilege LIKE '% ANY %' AND privilege NOT IN ('SELECT ANY TABLE', 'READ ANY TABLE', 'SELECT ANY DICTIONARY', 'SELECT ANY SEQUENCE')
			OR privilege IN ('SYSDBA', 'SYSOPER', 'BECOME USER', 'GRANT ANY PRIVILEGE', 'GRANT ANY ROLE')
		ORDER BY 1`, func(a, _ string) string { return a })
	collect(`SELECT privilege, owner || '.' || table_name FROM all_tab_privs
		WHERE grantee IN (SELECT USER FROM dual UNION SELECT role FROM session_roles UNION SELECT 'PUBLIC' FROM dual)
			AND privilege IN ('INSERT', 'UPDATE', 'DELETE', 'ALTER', 'INDEX') AND type IN ('TABLE', 'VIEW')
			AND owner IN `+userOwners+` ORDER BY 2, 1`, func(a, b string) string { return a + " ON " + b })
	var owned int
	if db.QueryRowContext(ctx, `SELECT count(*) FROM user_tables`).Scan(&owned) == nil && owned > 0 {
		res.WritePrivileges = append(res.WritePrivileges, fmt.Sprintf("owner of %d tables", owned))
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT owner || '.' || name FROM all_source
		WHERE owner IN `+userOwners+` AND upper(text) LIKE '%PRAGMA%AUTONOMOUS_TRANSACTION%' ORDER BY 1`)
	if err == nil {
		var units []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				units = append(units, n)
			}
		}
		rows.Close()
		if len(units) > 0 {
			res.Warnings = append(res.Warnings, "This user can execute PL/SQL that writes in its own transaction ("+strings.Join(units, ", ")+
				"); dbscope refuses queries that reach user PL/SQL, but revoke EXECUTE on it from a read-only user.")
		}
	}
	return res
}

func classify(err error) *conn.TestError {
	var oe *network.OracleError
	if errors.As(err, &oe) {
		kind := "database"
		switch oe.ErrCode {
		case 1017, 28000, 28001, 1045, 1005: // invalid credentials, locked, expired, no CREATE SESSION, null password
			kind = "auth"
		case 12514, 12505: // listener does not know the service or SID
			kind = "database"
		}
		return &conn.TestError{Kind: kind, Message: oe.ErrMsg}
	}
	if strings.Contains(err.Error(), "x509:") || strings.Contains(err.Error(), "tls:") {
		return &conn.TestError{Kind: "tls", Message: err.Error()}
	}
	return conn.Classify(err)
}

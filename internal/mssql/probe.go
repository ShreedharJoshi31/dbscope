package mssql

import (
	"context"
	"errors"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/dbscope/dbscope/internal/conn"
)

// writePermissions are database permissions that change data, schema or
// permissions; CONTROL implies them all.
var writePermissions = map[string]bool{
	"CONTROL": true, "INSERT": true, "UPDATE": true, "DELETE": true, "ALTER": true, "ALTER ANY SCHEMA": true,
	"CREATE TABLE": true, "CREATE PROCEDURE": true, "CREATE VIEW": true, "CREATE FUNCTION": true, "EXECUTE": true,
	"TAKE OWNERSHIP": true, "ALTER ANY USER": true, "ALTER ANY ROLE": true,
}

// Test connects with the profile and reports the server, whether the
// database is read-only, write permissions dbscope does not need, a missing
// SHOWPLAN permission (no query can run without it) and a missing VIEW
// DEFINITION (the diagram then lacks defaults and expressions).
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineSQLServer, Warnings: []string{}}
	if p.SSLMode == conn.SSLDisable && !loopback(p.Endpoint().Host) {
		res.Warnings = append(res.Warnings, "sslMode is disable on a non-loopback host: the login is encrypted, but query results travel unencrypted.")
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
	var updateability string
	if err := db.QueryRowContext(ctx, `SELECT CAST(DATABASEPROPERTYEX(DB_NAME(), 'Updateability') AS nvarchar(20))`).Scan(&updateability); err == nil {
		v := updateability == "READ_ONLY"
		res.DefaultReadOnly = &v
	}
	var sysadmin int
	if !srv.Azure && db.QueryRowContext(ctx, `SELECT coalesce(IS_SRVROLEMEMBER('sysadmin'), 0)`).Scan(&sysadmin) == nil && sysadmin == 1 {
		res.WritePrivileges = append(res.WritePrivileges, "sysadmin")
	}
	showplan := false
	if rows, err := db.QueryContext(ctx, `SELECT permission_name FROM fn_my_permissions(NULL, 'DATABASE')`); err == nil {
		for rows.Next() {
			var perm string
			if rows.Scan(&perm) != nil {
				continue
			}
			if perm == "SHOWPLAN" {
				showplan = true
			}
			if writePermissions[perm] && len(res.WritePrivileges) < 20 {
				res.WritePrivileges = append(res.WritePrivileges, perm+" ON DATABASE")
			}
		}
		rows.Close()
	}
	var viewDef int
	if db.QueryRowContext(ctx, `SELECT coalesce(HAS_PERMS_BY_NAME(DB_NAME(), 'DATABASE', 'VIEW DEFINITION'), 0)`).Scan(&viewDef) == nil && viewDef == 0 {
		res.Warnings = append(res.Warnings, "The user lacks VIEW DEFINITION, so SQL Server hides column defaults and computed column and check constraint expressions from it: GRANT VIEW DEFINITION TO the user.")
	}
	if !showplan {
		res.Warnings = append(res.Warnings, "The user lacks the SHOWPLAN permission, which dbscope needs to check each query before it runs: GRANT SHOWPLAN TO the user.")
	}
	return res
}

func classify(err error) *conn.TestError {
	var me mssql.Error
	if errors.As(err, &me) {
		kind := "database"
		switch me.Number {
		case 18456, 18452, 18486, 18487, 18488: // login failed, untrusted domain, locked out, password expired
			kind = "auth"
		case 4060, 916: // cannot open database, not a user of it
			kind = "auth"
		}
		return &conn.TestError{Kind: kind, Message: me.Message}
	}
	if strings.Contains(err.Error(), "TLS Handshake failed") || strings.Contains(err.Error(), "x509:") {
		return &conn.TestError{Kind: "tls", Message: err.Error()}
	}
	return conn.Classify(err)
}

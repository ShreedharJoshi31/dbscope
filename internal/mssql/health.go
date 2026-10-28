package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/health"
)

// stateGrant is the permission the server-wide DMVs need.
const stateGrant = "The login may not read server DMVs: GRANT VIEW SERVER PERFORMANCE STATE (or VIEW SERVER STATE before 2022) TO it."

// Health reads the server's and the profile database's health checks
// (docs/HEALTH.md); DMV checks the login may not read come back unknown.
func Health(ctx context.Context, db *sql.DB) (*health.Report, error) {
	start := time.Now()
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	r := &health.Report{Engine: "sqlserver", Server: "SQL Server " + srv.Version, Checks: []health.Check{}}
	why := func(err error) string {
		if s := err.Error(); strings.Contains(s, "VIEW SERVER") || strings.Contains(s, "VIEW DATABASE") || strings.Contains(s, "permission") {
			return stateGrant
		}
		return err.Error()
	}

	// Without the permission, sys.dm_exec_requests shows only the login's own requests instead of failing.
	var seesAll int
	db.QueryRowContext(ctx, `SELECT coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE'), 0)
		| coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE'), 0)`).Scan(&seesAll)
	var blocked int
	if seesAll == 0 {
		r.Unavailable("mssql.blocked_sessions", "Sessions waiting on locks", stateGrant)
	} else if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sys.dm_exec_requests WHERE blocking_session_id <> 0`).Scan(&blocked); err != nil {
		r.Unavailable("mssql.blocked_sessions", "Sessions waiting on locks", why(err))
	} else {
		r.Add(health.Check{ID: "mssql.blocked_sessions", Title: "Sessions waiting on locks", Status: health.Level(float64(blocked), 1, 10),
			Value: fmt.Sprint(blocked), Fix: "sys.dm_exec_requests.blocking_session_id names the blocker; sp_who2 shows what it runs"})
	}

	var oldestSecs sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT max(DATEDIFF(SECOND, at.transaction_begin_time, SYSDATETIME()))
		FROM sys.dm_tran_active_transactions at JOIN sys.dm_tran_session_transactions st ON st.transaction_id = at.transaction_id
		WHERE st.is_user_transaction = 1 AND st.session_id <> @@SPID`).Scan(&oldestSecs)
	if err != nil {
		r.Unavailable("mssql.oldest_transaction", "Oldest open transaction", why(err))
	} else {
		c := health.Check{ID: "mssql.oldest_transaction", Title: "Oldest open transaction", Status: health.OK, Value: "none",
			Fix: "DBCC OPENTRAN shows it; end it with KILL <session_id>"}
		if oldestSecs.Valid {
			d := time.Duration(oldestSecs.Int64) * time.Second
			c.Value, c.Status = health.Duration(d), health.Level(d.Minutes(), 5, 60)
			c.Detail = "While it is open, the transaction log cannot be truncated past it, so the log grows."
		}
		r.Add(c)
	}

	// Log use of the profile's database and what keeps it from being reused.
	var usedPct float64
	var logBytes int64
	var reuse, recovery string
	err = db.QueryRowContext(ctx, `SELECT u.used_log_space_in_percent, u.total_log_size_in_bytes, d.log_reuse_wait_desc, d.recovery_model_desc
		FROM sys.dm_db_log_space_usage u CROSS JOIN sys.databases d WHERE d.database_id = DB_ID()`).Scan(&usedPct, &logBytes, &reuse, &recovery)
	if err != nil {
		r.Unavailable("mssql.log_space", "Transaction log", why(err))
	} else {
		c := health.Check{ID: "mssql.log_space", Title: "Transaction log", Status: health.Level(usedPct, 70, 90),
			Value:  fmt.Sprintf("%.0f%% of %s used", usedPct, health.Bytes(logBytes)),
			Detail: fmt.Sprintf("Recovery model %s; the log is waiting on %s.", recovery, reuse)}
		switch reuse {
		case "LOG_BACKUP":
			c.Fix = "The FULL recovery model keeps the log until a log backup: schedule BACKUP LOG"
		case "ACTIVE_TRANSACTION":
			c.Fix = "A long transaction holds the log: find it with DBCC OPENTRAN"
		case "REPLICATION", "AVAILABILITY_REPLICA":
			c.Fix = "A replica or replication agent has not read the log yet: check its state"
		}
		r.Add(c)
	}

	// Last full backup of the database; msdb is readable by default only to some roles.
	var lastBackup sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT max(backup_finish_date) FROM msdb.dbo.backupset WHERE database_name = DB_NAME() AND type = 'D'`).Scan(&lastBackup); err != nil {
		r.Unavailable("mssql.last_backup", "Last full backup", "msdb.dbo.backupset is not readable: "+err.Error())
	} else {
		c := health.Check{ID: "mssql.last_backup", Title: "Last full backup", Status: health.Warn, Value: "never",
			Detail: "No full backup of this database is recorded in msdb.", Fix: "Schedule BACKUP DATABASE, and test restoring it"}
		if lastBackup.Valid {
			age := time.Since(lastBackup.Time)
			c.Value, c.Status, c.Detail = health.Duration(age)+" ago", health.Level(age.Hours(), 24*7, 24*30), ""
		}
		r.Add(c)
	}

	// Availability group replicas of the database, when it is in one.
	rows, err := db.QueryContext(ctx, `SELECT ar.replica_server_name, drs.synchronization_state_desc, drs.synchronization_health_desc,
			coalesce(drs.log_send_queue_size, 0), coalesce(drs.redo_queue_size, 0)
		FROM sys.dm_hadr_database_replica_states drs JOIN sys.availability_replicas ar ON ar.replica_id = drs.replica_id
		WHERE drs.database_id = DB_ID()`)
	if err != nil {
		r.Unavailable("mssql.availability", "Availability group", why(err))
	} else {
		var parts []string
		worst := health.OK
		for rows.Next() {
			var name, state, healthDesc string
			var sendKB, redoKB int64
			if rows.Scan(&name, &state, &healthDesc, &sendKB, &redoKB) != nil {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %s (%s, %s to send, %s to redo)", name, state, healthDesc, health.Bytes(sendKB<<10), health.Bytes(redoKB<<10)))
			switch healthDesc {
			case "NOT_HEALTHY":
				worst = health.Danger
			case "PARTIALLY_HEALTHY":
				if worst != health.Danger {
					worst = health.Warn
				}
			}
		}
		rows.Close()
		c := health.Check{ID: "mssql.availability", Title: "Availability group", Status: worst, Value: "not in an availability group"}
		if len(parts) > 0 {
			c.Value, c.Detail = fmt.Sprintf("%d replicas", len(parts)), strings.Join(parts, "; ")
			c.Fix = "sys.dm_hadr_database_replica_states and the AG dashboard show why a replica is behind"
		}
		r.Add(c)
	}

	var ple, upSecs int64
	if err := db.QueryRowContext(ctx, `SELECT TOP 1 cntr_value, (SELECT DATEDIFF(SECOND, sqlserver_start_time, SYSDATETIME()) FROM sys.dm_os_sys_info)
		FROM sys.dm_os_performance_counters
		WHERE counter_name = 'Page life expectancy' AND object_name LIKE '%Buffer Manager%'`).Scan(&ple, &upSecs); err != nil {
		r.Unavailable("mssql.page_life", "Page life expectancy", why(err))
	} else if up := time.Duration(upSecs) * time.Second; up < time.Hour {
		// The counter starts at zero with the server, so it says nothing yet.
		r.Add(health.Check{ID: "mssql.page_life", Title: "Page life expectancy", Status: health.OK,
			Value: "too early to say", Detail: "The server started " + health.Duration(up) + " ago."})
	} else {
		r.Add(health.Check{ID: "mssql.page_life", Title: "Page life expectancy", Status: health.Level(-float64(ple), -300, -60),
			Value:  health.Duration(time.Duration(ple) * time.Second),
			Detail: "How long a page stays in the buffer pool; a low value means memory pressure and more disk reads.",
			Fix:    "Find the queries that read the most (Query Store or sys.dm_exec_query_stats), or raise max server memory"})
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

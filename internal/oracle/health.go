package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/health"
)

// catalogGrant is the role the V$ and DBA_ views need.
const catalogGrant = "The user may not read V$ and DBA_ views: GRANT SELECT_CATALOG_ROLE TO it."

// Health reads the instance's and the service's health checks
// (docs/HEALTH.md); views the user may not read come back unknown.
func Health(ctx context.Context, db *sql.DB) (*health.Report, error) {
	start := time.Now()
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	r := &health.Report{Engine: "oracle", Server: "Oracle " + srv.Version, Checks: []health.Check{}}
	why := func(err error) string {
		if s := err.Error(); strings.Contains(s, "ORA-00942") || strings.Contains(s, "ORA-01031") || strings.Contains(s, "ORA-41900") {
			return catalogGrant
		}
		return err.Error()
	}

	rows, err := db.QueryContext(ctx, `SELECT resource_name, current_utilization, to_number(limit_value)
		FROM v$resource_limit WHERE resource_name IN ('processes', 'sessions') AND trim(limit_value) <> 'UNLIMITED'`)
	if err != nil {
		r.Unavailable("ora.sessions", "Processes and sessions", why(err))
	} else {
		worst, parts := 0.0, []string{}
		for rows.Next() {
			var name string
			var cur, limit float64
			if rows.Scan(&name, &cur, &limit) == nil && limit > 0 {
				worst = max(worst, cur/limit)
				parts = append(parts, fmt.Sprintf("%s %.0f of %.0f", name, cur, limit))
			}
		}
		rows.Close()
		r.Add(health.Check{ID: "ora.sessions", Title: "Processes and sessions", Status: health.Level(worst, 0.8, 0.95), Value: strings.Join(parts, ", "),
			Detail: "At the processes limit new connections fail with ORA-00020.", Fix: "Use a connection pool (or DRCP), or raise PROCESSES"})
	}

	var blocked int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM v$session WHERE blocking_session IS NOT NULL AND type = 'USER'`).Scan(&blocked); err != nil {
		r.Unavailable("ora.blocked_sessions", "Sessions waiting on locks", why(err))
	} else {
		r.Add(health.Check{ID: "ora.blocked_sessions", Title: "Sessions waiting on locks", Status: health.Level(float64(blocked), 1, 10),
			Value: fmt.Sprint(blocked), Fix: "v$session.blocking_session names the blocker; end it with ALTER SYSTEM KILL SESSION"})
	}

	var oldest sql.NullFloat64
	if err := db.QueryRowContext(ctx, `SELECT max((SYSDATE - t.start_date) * 86400) FROM v$transaction t
		JOIN v$session s ON s.taddr = t.addr WHERE s.sid <> SYS_CONTEXT('USERENV', 'SID')`).Scan(&oldest); err != nil {
		r.Unavailable("ora.oldest_transaction", "Oldest open transaction", why(err))
	} else {
		c := health.Check{ID: "ora.oldest_transaction", Title: "Oldest open transaction", Status: health.OK, Value: "none",
			Fix: "Find its session through v$transaction.addr = v$session.taddr and commit or end it"}
		if oldest.Valid {
			d := time.Duration(oldest.Float64) * time.Second
			c.Value, c.Status = health.Duration(d), health.Level(d.Minutes(), 5, 60)
			c.Detail = "Its undo must be kept; long queries then fail with ORA-01555 (snapshot too old) and undo grows."
		}
		r.Add(c)
	}

	rows, err = db.QueryContext(ctx, `SELECT tablespace_name, used_percent FROM dba_tablespace_usage_metrics ORDER BY used_percent DESC`)
	if err != nil {
		r.Unavailable("ora.tablespaces", "Tablespace use", why(err))
	} else {
		worst, name, n := 0.0, "", 0
		for rows.Next() {
			var ts string
			var pct float64
			if rows.Scan(&ts, &pct) == nil {
				n++
				if pct > worst {
					worst, name = pct, ts
				}
			}
		}
		rows.Close()
		r.Add(health.Check{ID: "ora.tablespaces", Title: "Tablespace use", Status: health.Level(worst, 85, 95), Value: fmt.Sprintf("%.0f%% in %s", worst, name),
			Detail: fmt.Sprintf("The fullest of %d tablespaces, counting room to autoextend.", n),
			Fix:    "Add a data file or raise MAXSIZE, or purge and shrink what it holds"})
	}

	var limit, used, reclaimable sql.NullFloat64
	if err := db.QueryRowContext(ctx, `SELECT sum(space_limit), sum(space_used), sum(space_reclaimable) FROM v$recovery_file_dest`).
		Scan(&limit, &used, &reclaimable); err != nil {
		r.Unavailable("ora.recovery_area", "Fast Recovery Area", why(err))
	} else if !limit.Valid || limit.Float64 == 0 {
		r.Add(health.Check{ID: "ora.recovery_area", Title: "Fast Recovery Area", Status: health.OK, Value: "not configured"})
	} else {
		pct := (used.Float64 - reclaimable.Float64) / limit.Float64 * 100
		r.Add(health.Check{ID: "ora.recovery_area", Title: "Fast Recovery Area", Status: health.Level(pct, 80, 90),
			Value:  fmt.Sprintf("%.0f%% of %s", pct, health.Bytes(int64(limit.Float64))),
			Detail: "When it fills, archiving stops, and with it every write (ORA-00257).",
			Fix:    "Back up and delete archived logs with RMAN, or raise DB_RECOVERY_FILE_DEST_SIZE"})
	}

	// Invalid objects in user schemas: readable by anyone for the objects they can see.
	rows, err = db.QueryContext(ctx, `SELECT owner || '.' || object_name || ' (' || lower(object_type) || ')' FROM all_objects
		WHERE status = 'INVALID' AND owner IN `+userOwners+` ORDER BY 1 FETCH FIRST 20 ROWS ONLY`)
	if err != nil {
		r.Unavailable("ora.invalid_objects", "Invalid objects", err.Error())
	} else {
		var names []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		rows.Close()
		c := health.Check{ID: "ora.invalid_objects", Title: "Invalid objects", Status: health.OK, Value: "none"}
		if len(names) > 0 {
			c.Status, c.Value = health.Warn, fmt.Sprint(len(names))
			c.Detail = "They fail or recompile on first use: " + strings.Join(names, ", ")
			c.Fix = "Recompile with ALTER ... COMPILE or UTL_RECOMP.RECOMP_SERIAL, and read the errors in ALL_ERRORS"
		}
		r.Add(c)
	}

	var role string
	if err := db.QueryRowContext(ctx, `SELECT database_role FROM v$database`).Scan(&role); err != nil {
		r.Unavailable("ora.data_guard", "Data Guard", why(err))
	} else if !strings.Contains(role, "STANDBY") {
		r.Add(health.Check{ID: "ora.data_guard", Title: "Data Guard", Status: health.OK, Value: strings.ToLower(role)})
	} else {
		var lag sql.NullString
		db.QueryRowContext(ctx, `SELECT value FROM v$dataguard_stats WHERE name = 'apply lag'`).Scan(&lag)
		c := health.Check{ID: "ora.data_guard", Title: "Data Guard", Status: health.Warn, Value: "standby, apply lag unknown"}
		if d, ok := intervalSeconds(lag.String); ok {
			c.Value, c.Status = "standby, "+health.Duration(d)+" behind", health.Level(d.Seconds(), 30, 300)
		}
		r.Add(c)
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

// intervalSeconds reads v$dataguard_stats' "+DD HH:MI:SS" interval text.
func intervalSeconds(s string) (time.Duration, bool) {
	var d, h, m, sec int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "+%d %d:%d:%d", &d, &h, &m, &sec); err != nil {
		return 0, false
	}
	return time.Duration(((d*24+h)*60+m)*60+sec) * time.Second, true
}

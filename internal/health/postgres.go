package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Querier is what the PostgreSQL checks need: a pool or a connection.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// idleAfter is how long a session may sit idle in a transaction before it counts.
var idleAfter = time.Minute

// statsRole is the grant that lets a role see other users' sessions and replication state.
const statsRole = "Other users' sessions are hidden from this role: GRANT pg_read_all_stats (or pg_monitor) TO it."

// Postgres reads the health of the database the connection is on and of
// its server. Every query is a catalog or statistics read; checks the role
// may not read come back as unknown.
func Postgres(ctx context.Context, db Querier) (*Report, error) {
	start := time.Now()
	var version string
	var seesAll bool
	if err := db.QueryRow(ctx, `SELECT current_setting('server_version'),
			pg_has_role(current_user, 'pg_read_all_stats', 'MEMBER')`).Scan(&version, &seesAll); err != nil {
		return nil, err
	}
	r := &Report{Engine: "postgres", Server: "PostgreSQL " + version, Checks: []Check{}}
	limited := func(c Check) Check {
		if !seesAll && c.Status == OK {
			c.Detail = strings.TrimSpace(c.Detail + " Only this role's own sessions were counted.")
		}
		return c
	}

	// Connections against max_connections, counting every backend the role can see.
	var used, maxConn, reserved int
	if err := db.QueryRow(ctx, `SELECT count(*), current_setting('max_connections')::int,
			current_setting('superuser_reserved_connections')::int
		FROM pg_stat_activity WHERE backend_type = 'client backend'`).Scan(&used, &maxConn, &reserved); err != nil {
		r.Unavailable("pg.connections", "Connections", err.Error())
	} else {
		usable := maxConn - reserved
		c := Check{ID: "pg.connections", Title: "Connections", Status: Level(float64(used)/float64(usable), 0.8, 0.95),
			Value: fmt.Sprintf("%d of %d", used, usable), Detail: fmt.Sprintf("%d more are reserved for superusers.", reserved),
			Fix: "Put a pooler (PgBouncer) in front, or lower each application's pool size"}
		r.Add(limited(c))
	}

	// The oldest open transaction holds back vacuum everywhere.
	var oldest *time.Duration
	var idleInTx int
	var oldestState string
	err := db.QueryRow(ctx, `SELECT (SELECT now() - xact_start FROM pg_stat_activity
				WHERE xact_start IS NOT NULL AND backend_type = 'client backend' AND pid <> pg_backend_pid() ORDER BY xact_start LIMIT 1),
			coalesce((SELECT state FROM pg_stat_activity
				WHERE xact_start IS NOT NULL AND backend_type = 'client backend' AND pid <> pg_backend_pid() ORDER BY xact_start LIMIT 1), ''),
			(SELECT count(*) FROM pg_stat_activity WHERE state LIKE 'idle in transaction%' AND now() - state_change > make_interval(secs => $1))`,
		idleAfter.Seconds()).Scan(&oldest, &oldestState, &idleInTx)
	if err != nil {
		r.Unavailable("pg.oldest_transaction", "Oldest open transaction", err.Error())
	} else {
		c := Check{ID: "pg.oldest_transaction", Title: "Oldest open transaction", Status: OK, Value: "none",
			Fix: "Find it in pg_stat_activity and end it with pg_terminate_backend; set idle_in_transaction_session_timeout"}
		if oldest != nil {
			c.Value = Duration(*oldest) + " (" + oldestState + ")"
			c.Status = Level(oldest.Minutes(), 5, 60)
			c.Detail = "While it is open, vacuum cannot remove rows deleted after it began, so tables and indexes grow."
		}
		r.Add(limited(c))
		c = Check{ID: "pg.idle_in_transaction", Title: "Sessions idle in a transaction", Status: Level(float64(idleInTx), 1, 5),
			Value: fmt.Sprintf("%d for over a minute", idleInTx), Detail: "They hold their locks and snapshot while doing nothing.",
			Fix: "Set idle_in_transaction_session_timeout, and fix the code paths that open a transaction and wait"}
		r.Add(limited(c))
	}

	// Transaction ID wraparound: at 2^31 the server stops accepting writes.
	var xidAge int64
	var freezeMax int64
	if err := db.QueryRow(ctx, `SELECT max(age(datfrozenxid)), current_setting('autovacuum_freeze_max_age')::bigint FROM pg_database`).
		Scan(&xidAge, &freezeMax); err != nil {
		r.Unavailable("pg.xid_age", "Transaction ID age", err.Error())
	} else {
		pct := float64(xidAge) / float64(1<<31)
		r.Add(Check{ID: "pg.xid_age", Title: "Transaction ID age", Status: Level(pct, 0.5, 0.75),
			Value:  fmt.Sprintf("%d million (%.0f%% of the 2,147 million limit)", xidAge/1_000_000, pct*100),
			Detail: fmt.Sprintf("Autovacuum starts forcing freezes at %d million; at the limit the server refuses writes.", freezeMax/1_000_000),
			Fix:    "Find the oldest database and table by age(datfrozenxid) and age(relfrozenxid), and VACUUM (FREEZE) them; check for long transactions and abandoned slots holding it back"})
	}

	// Replication: lag on a replica, or each standby's replay lag on a primary.
	var inRecovery bool
	db.QueryRow(ctx, `SELECT pg_is_in_recovery()`).Scan(&inRecovery)
	if inRecovery {
		var lag *time.Duration
		if err := db.QueryRow(ctx, `SELECT now() - pg_last_xact_replay_timestamp()`).Scan(&lag); err != nil {
			r.Unavailable("pg.replication_lag", "Replication lag", err.Error())
		} else {
			c := Check{ID: "pg.replication_lag", Title: "Replication lag", Status: OK, Value: "nothing replayed yet"}
			if lag != nil {
				c.Value, c.Status = Duration(*lag), Level(lag.Seconds(), 30, 300)
				c.Detail = "Time since the last replayed transaction; on an idle primary this grows without real lag."
			}
			r.Add(c)
		}
	} else {
		rows, err := db.Query(ctx, `SELECT coalesce(application_name, ''), coalesce(client_addr::text, 'local'), state,
				coalesce(extract(epoch FROM replay_lag), 0) FROM pg_stat_replication`)
		if err != nil {
			r.Unavailable("pg.replication_lag", "Replication lag", err.Error())
		} else {
			worst, n, names := 0.0, 0, []string{}
			for rows.Next() {
				var app, addr, state string
				var lag float64
				if rows.Scan(&app, &addr, &state, &lag) == nil {
					n++
					worst = max(worst, lag)
					names = append(names, fmt.Sprintf("%s (%s, %s, %s)", app, addr, state, Duration(time.Duration(lag*float64(time.Second)))))
				}
			}
			rows.Close()
			c := Check{ID: "pg.replication_lag", Title: "Replication lag", Status: Level(worst, 30, 300), Value: fmt.Sprintf("%d standbys", n),
				Detail: strings.Join(names, "; ")}
			if n == 0 {
				c.Value, c.Detail = "no standbys connected", ""
				if !seesAll {
					c.Detail = statsRole
					c.Status = Unknown
				}
			}
			r.Add(c)
		}
	}

	// Inactive replication slots keep WAL forever.
	rows, err := db.Query(ctx, `SELECT slot_name, slot_type, coalesce(pg_wal_lsn_diff(
			CASE WHEN pg_is_in_recovery() THEN pg_last_wal_receive_lsn() ELSE pg_current_wal_lsn() END, restart_lsn), 0)::bigint
		FROM pg_replication_slots WHERE NOT active`)
	if err != nil {
		r.Unavailable("pg.inactive_slots", "Inactive replication slots", err.Error())
	} else {
		var names []string
		var retained int64
		for rows.Next() {
			var name, kind string
			var bytes int64
			if rows.Scan(&name, &kind, &bytes) == nil {
				names = append(names, fmt.Sprintf("%s (%s, %s of WAL)", name, kind, Bytes(bytes)))
				retained = max(retained, bytes)
			}
		}
		rows.Close()
		c := Check{ID: "pg.inactive_slots", Title: "Inactive replication slots", Status: OK, Value: "none"}
		if len(names) > 0 {
			c.Status = Level(float64(retained), 0, 10<<30)
			if c.Status == OK {
				c.Status = Warn
			}
			c.Value = fmt.Sprintf("%d, holding up to %s", len(names), Bytes(retained))
			c.Detail = "An inactive slot keeps every WAL file from its position on, until the disk fills: " + strings.Join(names, "; ")
			c.Fix = "Drop slots nothing uses with pg_drop_replication_slot, or set max_slot_wal_keep_size"
		}
		r.Add(c)
	}

	// Sessions waiting on another session's lock.
	var blocked int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid)) > 0`).Scan(&blocked); err != nil {
		r.Unavailable("pg.blocked_sessions", "Sessions waiting on locks", err.Error())
	} else {
		r.Add(limited(Check{ID: "pg.blocked_sessions", Title: "Sessions waiting on locks", Status: Level(float64(blocked), 1, 10),
			Value: fmt.Sprintf("%d", blocked), Fix: "pg_blocking_pids(pid) names the blocker; find what it is doing in pg_stat_activity"}))
	}

	// Invalid indexes: a failed CREATE INDEX CONCURRENTLY leaves one behind, still maintained on every write.
	rows, err = db.Query(ctx, `SELECT n.nspname || '.' || c.relname FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace WHERE NOT i.indisvalid ORDER BY 1 LIMIT 20`)
	if err != nil {
		r.Unavailable("pg.invalid_indexes", "Invalid indexes", err.Error())
	} else {
		var names []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		rows.Close()
		c := Check{ID: "pg.invalid_indexes", Title: "Invalid indexes", Status: OK, Value: "none"}
		if len(names) > 0 {
			c.Status, c.Value = Warn, fmt.Sprintf("%d", len(names))
			c.Detail = "Queries never use them, but every write still updates them: " + strings.Join(names, ", ")
			c.Fix = "DROP INDEX CONCURRENTLY each one, then create it again"
		}
		r.Add(c)
	}

	// Tables autovacuum is behind on.
	rows, err = db.Query(ctx, `SELECT schemaname || '.' || relname, n_dead_tup, n_live_tup FROM pg_stat_user_tables
		WHERE n_dead_tup > 10000 AND n_dead_tup > 0.2 * greatest(n_live_tup, 1) ORDER BY n_dead_tup DESC LIMIT 10`)
	if err != nil {
		r.Unavailable("pg.dead_tuples", "Tables with many dead rows", err.Error())
	} else {
		var names []string
		for rows.Next() {
			var n string
			var dead, live int64
			if rows.Scan(&n, &dead, &live) == nil {
				names = append(names, fmt.Sprintf("%s (%d dead, %d live)", n, dead, live))
			}
		}
		rows.Close()
		c := Check{ID: "pg.dead_tuples", Title: "Tables with many dead rows", Status: OK, Value: "none"}
		if len(names) > 0 {
			c.Status, c.Value = Warn, fmt.Sprintf("%d", len(names))
			c.Detail = "More than 20% of their rows are dead: autovacuum is behind or blocked. " + strings.Join(names, "; ")
			c.Fix = "Look for long transactions first; then lower autovacuum_vacuum_scale_factor on these tables"
		}
		r.Add(c)
	}

	// Cache hit ratio of this database, when there has been enough reading to say.
	var hit, read int64
	if err := db.QueryRow(ctx, `SELECT blks_hit, blks_read FROM pg_stat_database WHERE datname = current_database()`).Scan(&hit, &read); err != nil {
		r.Unavailable("pg.cache_hit", "Cache hit ratio", err.Error())
	} else if total := hit + read; total < 100_000 {
		r.Add(Check{ID: "pg.cache_hit", Title: "Cache hit ratio", Status: OK, Value: "too little traffic to say",
			Detail: fmt.Sprintf("%d block reads since the statistics were reset.", total)})
	} else {
		ratio := float64(hit) / float64(total)
		r.Add(Check{ID: "pg.cache_hit", Title: "Cache hit ratio", Status: Level(1-ratio, 0.05, 0.2), Value: fmt.Sprintf("%.1f%%", ratio*100),
			Detail: "Share of block reads served from shared buffers; the rest went to the OS cache or disk.",
			Fix:    "Raise shared_buffers if memory allows, or find the queries that read the most (pg_stat_statements)"})
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

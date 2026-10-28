package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/health"
)

// processGrant is what lets a user see other sessions' InnoDB transactions.
const processGrant = "The user lacks the PROCESS privilege, so InnoDB's transaction list is hidden: GRANT PROCESS ON *.* TO it."

// Health reads the server's health checks (docs/HEALTH.md). Global status
// and variables are readable by every user; InnoDB's transaction list and
// metrics need PROCESS, and replica status needs REPLICATION CLIENT (MySQL)
// or SLAVE MONITOR (MariaDB).
func Health(ctx context.Context, db *sql.DB) (*health.Report, error) {
	start := time.Now()
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	name := map[string]string{"mysql": "MySQL", "mariadb": "MariaDB"}[srv.Flavor]
	r := &health.Report{Engine: srv.Flavor, Server: name + " " + srv.Version, Checks: []health.Check{}}
	status, err := globals(ctx, db, "SHOW GLOBAL STATUS")
	if err != nil {
		return nil, err
	}
	vars, err := globals(ctx, db, "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('max_connections', 'innodb_buffer_pool_size')")
	if err != nil {
		return nil, err
	}
	num := func(m map[string]string, k string) float64 { f, _ := strconv.ParseFloat(m[k], 64); return f }

	used, maxConn := num(status, "Threads_connected"), num(vars, "max_connections")
	r.Add(health.Check{ID: "mysql.connections", Title: "Connections", Status: health.Level(used/maxConn, 0.8, 0.95),
		Value:  fmt.Sprintf("%.0f of %.0f", used, maxConn),
		Detail: fmt.Sprintf("The most at once since start: %.0f.", num(status, "Max_used_connections")),
		Fix:    "Lower the applications' pool sizes or put ProxySQL in front; raising max_connections costs memory per connection"})

	// The oldest transaction holds back purge, so undo history grows.
	var oldest sql.NullInt64
	var waiting int
	err = db.QueryRowContext(ctx, `SELECT max(TIMESTAMPDIFF(SECOND, trx_started, NOW())),
			coalesce(sum(trx_state = 'LOCK WAIT'), 0)
		FROM information_schema.INNODB_TRX WHERE trx_mysql_thread_id <> CONNECTION_ID()`).Scan(&oldest, &waiting)
	if err != nil {
		why := err.Error()
		if strings.Contains(why, "PROCESS") {
			why = processGrant
		}
		r.Unavailable("mysql.oldest_transaction", "Oldest open transaction", why)
		r.Unavailable("mysql.lock_waits", "Transactions waiting on locks", why)
	} else {
		c := health.Check{ID: "mysql.oldest_transaction", Title: "Oldest open transaction", Status: health.OK, Value: "none",
			Fix: "Find it in information_schema.INNODB_TRX and end it with KILL; set wait_timeout for idle sessions"}
		if oldest.Valid {
			d := time.Duration(oldest.Int64) * time.Second
			c.Value, c.Status = health.Duration(d), health.Level(d.Minutes(), 5, 60)
			c.Detail = "While it is open, purge cannot remove older row versions, so undo logs grow and reads slow down."
		}
		r.Add(c)
		r.Add(health.Check{ID: "mysql.lock_waits", Title: "Transactions waiting on locks", Status: health.Level(float64(waiting), 1, 10),
			Value: strconv.Itoa(waiting), Fix: "sys.innodb_lock_waits names the blocking thread and what it ran"})
	}

	// Purge lag: undo history not yet purged.
	var history sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT COUNT FROM information_schema.INNODB_METRICS WHERE NAME = 'trx_rseg_history_len'`).Scan(&history); err != nil || !history.Valid {
		why := processGrant
		if err != nil && !strings.Contains(err.Error(), "PROCESS") && !strings.Contains(err.Error(), "denied") {
			why = fmt.Sprint(err)
		}
		r.Unavailable("mysql.history_length", "Undo history not yet purged", why)
	} else {
		r.Add(health.Check{ID: "mysql.history_length", Title: "Undo history not yet purged", Status: health.Level(float64(history.Int64), 100_000, 1_000_000),
			Value:  fmt.Sprintf("%d transactions", history.Int64),
			Detail: "A long history means purge is behind, usually because of a long-running transaction; reads walk more versions.",
			Fix:    "End long transactions; raise innodb_purge_threads if purge cannot keep up with writes"})
	}

	replica(ctx, db, srv, r)

	reads, requests := num(status, "Innodb_buffer_pool_reads"), num(status, "Innodb_buffer_pool_read_requests")
	if requests < 100_000 {
		r.Add(health.Check{ID: "mysql.buffer_pool_hit", Title: "Buffer pool hit ratio", Status: health.OK, Value: "too little traffic to say",
			Detail: fmt.Sprintf("%.0f page reads since start.", requests)})
	} else {
		miss := reads / requests
		r.Add(health.Check{ID: "mysql.buffer_pool_hit", Title: "Buffer pool hit ratio", Status: health.Level(miss, 0.01, 0.05),
			Value:  fmt.Sprintf("%.2f%%", (1-miss)*100),
			Detail: fmt.Sprintf("Share of page reads served from the %s buffer pool; the rest went to disk.", health.Bytes(int64(num(vars, "innodb_buffer_pool_size")))),
			Fix:    "Raise innodb_buffer_pool_size if memory allows (up to about 70% of RAM on a dedicated server)"})
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

// replica reads SHOW REPLICA STATUS: no rows means the server is not a replica.
func replica(ctx context.Context, db *sql.DB, srv Server, r *health.Report) {
	rows, err := db.QueryContext(ctx, "SHOW REPLICA STATUS")
	if err != nil {
		grant := "REPLICATION CLIENT"
		if srv.Flavor == "mariadb" {
			grant = "SLAVE MONITOR"
		}
		why := err.Error()
		if strings.Contains(why, "denied") {
			why = "The user may not read replica status: GRANT " + grant + " ON *.* TO it."
		}
		r.Unavailable("mysql.replication", "Replication", why)
		return
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		r.Add(health.Check{ID: "mysql.replication", Title: "Replication", Status: health.OK, Value: "not a replica"})
		return
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		r.Unavailable("mysql.replication", "Replication", err.Error())
		return
	}
	get := func(names ...string) sql.NullString {
		for _, n := range names {
			for i, c := range cols {
				if strings.EqualFold(c, n) {
					return vals[i]
				}
			}
		}
		return sql.NullString{}
	}
	io, sqlThread := get("Replica_IO_Running", "Slave_IO_Running").String, get("Replica_SQL_Running", "Slave_SQL_Running").String
	lag := get("Seconds_Behind_Source", "Seconds_Behind_Master")
	c := health.Check{ID: "mysql.replication", Title: "Replication", Status: health.OK}
	switch {
	case io != "Yes" || sqlThread != "Yes":
		c.Status = health.Danger
		c.Value = fmt.Sprintf("stopped (I/O %s, SQL %s)", io, sqlThread)
		c.Detail = strings.TrimSpace(get("Last_IO_Error").String + " " + get("Last_SQL_Error").String)
		c.Fix = "Read the last error, fix its cause, then START REPLICA"
	case !lag.Valid:
		c.Status, c.Value = health.Warn, "lag unknown"
	default:
		s, _ := strconv.Atoi(lag.String)
		c.Value, c.Status = health.Duration(time.Duration(s)*time.Second)+" behind", health.Level(float64(s), 30, 300)
	}
	r.Add(c)
}

// globals reads a SHOW ... STATUS or VARIABLES result into a map.
func globals(ctx context.Context, db *sql.DB, q string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

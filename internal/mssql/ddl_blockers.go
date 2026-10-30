package mssql

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
)

// ReadBlockers fills rep.Blockers with other sessions that hold or wait for
// a lock on a table the migration locks: its DDL would queue behind them.
// It matches object ids from the base rather than calling OBJECT_NAME,
// which itself waits behind a Sch-M lock. Without VIEW SERVER STATE (VIEW
// SERVER PERFORMANCE STATE from 2022) the read is skipped with a warning.
func ReadBlockers(ctx context.Context, db *sql.DB, base *DDLBase, rep *migrate.Report) {
	byID := map[int64]string{}
	for _, s := range rep.Statements {
		for _, l := range s.Locks {
			if t := base.Tables[strings.ToLower(l.Relation)]; t != nil && t.ObjectID != 0 {
				byID[t.ObjectID] = l.Relation
			}
		}
	}
	if len(byID) == 0 {
		return
	}
	now := time.Now().UTC()
	var out []migrate.Blocker
	err := each(ctx, db, `SELECT l.resource_associated_entity_id, l.request_session_id, s.login_name, s.status, l.request_status, l.request_mode,
			coalesce(DATEDIFF_BIG(millisecond, at.transaction_begin_time, SYSDATETIME()), 0), coalesce(LEFT(txt.text, 1000), '')
		FROM sys.dm_tran_locks l
		JOIN sys.dm_exec_sessions s ON s.session_id = l.request_session_id
		LEFT JOIN sys.dm_tran_session_transactions st ON st.session_id = l.request_session_id
		LEFT JOIN sys.dm_tran_active_transactions at ON at.transaction_id = st.transaction_id
		LEFT JOIN sys.dm_exec_connections c ON c.session_id = l.request_session_id AND c.parent_connection_id IS NULL
		OUTER APPLY sys.dm_exec_sql_text(c.most_recent_sql_handle) txt
		WHERE l.resource_type = 'OBJECT' AND l.resource_database_id = DB_ID() AND l.request_session_id <> @@SPID
		ORDER BY at.transaction_begin_time`, nil,
		func(r *sql.Rows) error {
			var id, ms int64
			var b migrate.Blocker
			var session int
			var status, reqStatus string
			if err := r.Scan(&id, &session, &b.User, &status, &reqStatus, &b.Lock, &ms, &b.Query); err != nil {
				return err
			}
			rel, ok := byID[id]
			if !ok || len(out) >= 50 {
				return nil
			}
			b.Relation, b.AgeSeconds = rel, float64(ms)/1000
			b.Session = strconv.Itoa(session)
			b.State = strings.ToLower(status) + ", lock " + map[string]string{"GRANT": "held", "WAIT": "waiting", "CONVERT": "converting"}[reqStatus]
			out = append(out, b)
			return nil
		})
	if err != nil {
		rep.Warnings = append(rep.Warnings, "sessions holding locks on these tables were not read: "+err.Error()+
			" (needs VIEW SERVER PERFORMANCE STATE, or VIEW SERVER STATE before SQL Server 2022)")
		return
	}
	rep.Blockers, rep.BlockersAt = out, &now
}

package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
)

// tmModes names the TM lock modes of V$LOCKED_OBJECT.LOCKED_MODE.
var tmModes = map[int]string{1: "NULL", 2: "ROW SHARE", 3: "ROW EXCLUSIVE", 4: "SHARE", 5: "SHARE ROW EXCLUSIVE", 6: "EXCLUSIVE"}

// ReadBlockers names the sessions holding a lock on a table the
// migration's DDL changes: an open transaction that has changed the table
// makes the DDL wait for it, or fail at once with ORA-00054, depending on
// the statement (verified on 23.26 by TestDDLPredictions). It reads
// V$LOCKED_OBJECT, V$SESSION, V$TRANSACTION and V$SQL, which need
// SELECT_CATALOG_ROLE; without it it warns. It never fails the analysis.
func ReadBlockers(ctx context.Context, db *sql.DB, base *Base, rep *migrate.Report) {
	type rel struct{ owner, name string }
	var rels []rel
	seen := map[string]bool{}
	for _, s := range rep.Statements {
		for _, l := range s.Locks {
			owner, name, ok := strings.Cut(l.Relation, ".")
			if !ok || seen[l.Relation] {
				continue
			}
			seen[l.Relation] = true
			rels = append(rels, rel{owner, name})
		}
	}
	if len(rels) == 0 {
		return
	}
	var cond []string
	var args []any
	for i, r := range rels {
		cond = append(cond, fmt.Sprintf("(o.owner = :%d AND o.object_name = :%d)", 2*i+1, 2*i+2))
		args = append(args, r.owner, r.name)
	}
	rows, err := db.QueryContext(ctx, `SELECT o.owner, o.object_name, s.sid, s.serial#, coalesce(s.username, ' '), s.status, lo.locked_mode,
			coalesce((sysdate - t.start_date) * 86400, s.last_call_et), coalesce(s.event, ' '),
			coalesce((SELECT max(q.sql_text) FROM v$sql q WHERE q.sql_id = coalesce(s.sql_id, s.prev_sql_id)), ' ')
		FROM v$locked_object lo
		JOIN all_objects o ON o.object_id = lo.object_id
		JOIN v$session s ON s.sid = lo.session_id
		LEFT JOIN v$transaction t ON t.addr = s.taddr
		WHERE s.sid <> to_number(sys_context('USERENV', 'SID')) AND (`+strings.Join(cond, " OR ")+`)
		ORDER BY t.start_date NULLS LAST, s.sid
		FETCH FIRST 500 ROWS ONLY`, args...)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "sessions that would block the migration were not read (V$LOCKED_OBJECT and V$SESSION need SELECT_CATALOG_ROLE): "+firstLine(err))
		return
	}
	defer rows.Close()
	type held struct {
		relation string
		b        migrate.Blocker
	}
	var locks []held
	for rows.Next() {
		var owner, name, user, status, event, query string
		var sid, serial, mode int
		var age float64
		if err := rows.Scan(&owner, &name, &sid, &serial, &user, &status, &mode, &age, &event, &query); err != nil {
			rep.Warnings = append(rep.Warnings, "sessions that would block the migration were not read: "+firstLine(err))
			return
		}
		state := "in transaction, running: " + strings.TrimSpace(event)
		if status == "INACTIVE" {
			state = "idle in transaction"
		}
		relation := owner + "." + name
		locks = append(locks, held{relation, migrate.Blocker{Relation: relation, Session: fmt.Sprintf("%d,%d", sid, serial),
			User: strings.TrimSpace(user), State: state, AgeSeconds: age, Query: migrate.TruncateQuery(strings.TrimSpace(query)),
			Lock: tmModes[mode] + " table lock"}})
	}
	if err := rows.Err(); err != nil {
		rep.Warnings = append(rep.Warnings, "sessions that would block the migration were not read: "+firstLine(err))
		return
	}
	now := time.Now().UTC()
	rep.BlockersAt = &now
	index := map[string]int{}
	for i := range rep.Statements {
		s := &rep.Statements[i]
		if !s.LockTimeout && !statementHas(*s, ruleDDLLock) {
			continue // it neither waits for nor fails on open transactions (a virtual column, COMMENT)
		}
		var named []string
		for _, need := range s.Locks {
			for _, l := range locks {
				if l.relation != need.Relation {
					continue
				}
				k := l.b.Session + " " + l.relation
				j, ok := index[k]
				if !ok {
					j = len(rep.Blockers)
					index[k] = j
					rep.Blockers = append(rep.Blockers, l.b)
				}
				desc := migrate.DescribeBlocker(rep.Blockers[j], "session "+l.b.Session)
				if !contains(named, desc) {
					named = append(named, desc)
				}
			}
		}
		if len(named) == 0 {
			continue
		}
		effect, alt := "; the DDL waits until that transaction ends", "Wait for the transaction to end, or end it (ALTER SYSTEM KILL SESSION 'sid,serial#'), before running the migration"
		if s.LockTimeout {
			effect = "; the DDL fails with ORA-00054 unless that transaction ends within ddl_lock_timeout"
		}
		migrate.MarkBlocked(rep, s, named, effect, alt)
		s.Score = max(s.Score, 8)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func statementHas(s migrate.Statement, id string) bool {
	for _, f := range s.Findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

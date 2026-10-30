package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
)

// mdlInstrument records metadata locks in performance_schema.metadata_locks
// (on by default in MySQL 8.0 and later, off in MariaDB).
const mdlInstrument = "wait/lock/metadata/sql/mdl"

// ReadBlockers names the sessions holding or queued for a metadata lock on
// a table the migration's DDL locks, and adds DBS-BLOCKED-NOW to each
// statement that would wait for them: every DDL needs an exclusive metadata
// lock at least briefly, and that conflicts with any other lock on the table
// (MySQL manual, "Metadata Locking"). It reads
// performance_schema.metadata_locks, with information_schema.INNODB_TRX for
// transaction age; when metadata locks cannot be read it warns and lists
// the open InnoDB transactions instead. It never fails the analysis.
func ReadBlockers(ctx context.Context, db *sql.DB, base *Base, rep *migrate.Report) {
	var rels []tableRef
	seen := map[string]bool{}
	for _, s := range rep.Statements {
		for _, l := range s.Locks {
			sch, name, ok := strings.Cut(l.Relation, ".")
			if !ok {
				sch, name = base.Schema, l.Relation
			}
			k := strings.ToLower(sch + "." + name)
			if sch == "" || seen[k] {
				continue
			}
			seen[k] = true
			rels = append(rels, tableRef{sch, name})
		}
	}
	if len(rels) == 0 {
		return
	}
	warn := func(msg string) { rep.Warnings = append(rep.Warnings, msg) }
	c, err := db.Conn(ctx)
	if err != nil {
		warn("sessions that would block the migration were not read: " + err.Error())
		return
	}
	defer c.Close()

	trx, trxErr := openTransactions(ctx, c)
	if trxErr != nil {
		warn("open transactions were not read (information_schema.INNODB_TRX needs the PROCESS privilege), so blocker ages are the current statement's: " + trxErr.Error())
	}
	locks, why := metadataLocks(ctx, c, rels)
	if why != "" {
		if trxErr != nil {
			warn(why + "; dbscope cannot name the sessions that would block the migration")
			return
		}
		msg := why + ", so dbscope cannot tell which tables open transactions have used, nor see statements that run outside a transaction or on non-InnoDB tables"
		if len(trx) == 0 {
			warn(msg + "; no InnoDB transaction is open")
			return
		}
		var named []string
		for _, t := range trx {
			d := fmt.Sprintf("connection %d (%s), %s for %s", t.id, t.user, t.state(), migrate.AgeText(t.age))
			if t.query != "" {
				d += " (running: " + t.query + ")"
			}
			named = append(named, d)
		}
		if len(named) > 5 {
			named = append(named[:5], fmt.Sprintf("and %d more", len(named)-5))
		}
		warn(msg + "; any of these open transactions blocks the DDL if it has read or written a table it changes: " + strings.Join(named, "; "))
		return
	}
	now := time.Now().UTC()
	rep.BlockersAt = &now
	byTrx := map[int64]txn{}
	for _, t := range trx {
		byTrx[t.id] = t
	}
	type key struct {
		id  int64
		rel string
	}
	index := map[key]int{}
	for i := range rep.Statements {
		s := &rep.Statements[i]
		var named []string
		for _, need := range s.Locks {
			for _, l := range locks {
				if !strings.EqualFold(l.schema+"."+l.name, need.Relation) && !(strings.EqualFold(l.schema, base.Schema) && strings.EqualFold(l.name, need.Relation)) {
					continue
				}
				k := key{l.id, need.Relation}
				j, ok := index[k]
				if !ok {
					j = len(rep.Blockers)
					index[k] = j
					b := migrate.Blocker{Relation: need.Relation, Session: fmt.Sprint(l.id), User: l.user, AgeSeconds: l.time,
						Query: migrate.TruncateQuery(l.query), Lock: mdlText(l)}
					t, inTrx := byTrx[l.id]
					switch {
					case inTrx:
						b.AgeSeconds = t.age
						b.State = t.state()
						if l.command != "Sleep" {
							b.State = "in transaction, running: " + strings.ToLower(l.command)
						}
					case l.command == "Sleep":
						b.State = "idle, holding a lock (no InnoDB transaction)"
					default:
						b.State = strings.ToLower(l.command)
					}
					if l.state != "" && l.command != "Sleep" {
						b.State += " (" + l.state + ")"
					}
					rep.Blockers = append(rep.Blockers, b)
				} else if bl := &rep.Blockers[j]; l.granted && strings.HasSuffix(bl.Lock, "(waiting)") {
					bl.Lock = mdlText(l)
				}
				desc := migrate.DescribeBlocker(rep.Blockers[j], "connection "+fmt.Sprint(l.id))
				if !contains(named, desc) {
					named = append(named, desc)
				}
			}
		}
		if len(named) == 0 {
			continue
		}
		migrate.MarkBlocked(rep, s, named, "; the DDL's exclusive metadata lock waits for it, and every later query on the table queues behind the DDL",
			"Wait for the transaction to end, or end it (KILL <connection>), and SET SESSION lock_wait_timeout = 5 before the DDL so it gives up instead of queueing others")
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

// txn is an open InnoDB transaction.
type txn struct {
	id          int64
	age         float64
	user, query string
	command     string
}

func (t txn) state() string {
	if t.command == "Sleep" {
		return "idle in transaction"
	}
	return "in transaction, running: " + strings.ToLower(t.command)
}

func openTransactions(ctx context.Context, c *sql.Conn) ([]txn, error) {
	rows, err := c.QueryContext(ctx, `SELECT t.trx_mysql_thread_id, TIMESTAMPDIFF(MICROSECOND, t.trx_started, NOW(6)) / 1e6,
			coalesce(p.USER, ''), coalesce(p.COMMAND, ''), coalesce(t.trx_query, '')
		FROM information_schema.INNODB_TRX t LEFT JOIN information_schema.PROCESSLIST p ON p.ID = t.trx_mysql_thread_id
		WHERE t.trx_mysql_thread_id <> CONNECTION_ID()
		ORDER BY t.trx_started LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []txn
	for rows.Next() {
		var t txn
		if err := rows.Scan(&t.id, &t.age, &t.user, &t.command, &t.query); err != nil {
			return nil, err
		}
		t.query = migrate.TruncateQuery(t.query)
		out = append(out, t)
	}
	return out, rows.Err()
}

type tableRef struct{ schema, name string }

// mdl is one metadata lock on a table, with its owner.
type mdl struct {
	schema, name, lockType      string
	granted                     bool
	id                          int64
	user, command, state, query string
	time                        float64
}

func mdlText(l mdl) string {
	s := l.lockType + " metadata lock"
	if !l.granted {
		s += " (waiting)"
	}
	return s
}

// metadataLocks reads the table metadata locks of other connections on rels;
// why says why they could not be read.
func metadataLocks(ctx context.Context, c *sql.Conn, rels []tableRef) ([]mdl, string) {
	var on int
	if err := c.QueryRowContext(ctx, "SELECT @@performance_schema").Scan(&on); err != nil {
		return nil, "performance_schema was not read: " + err.Error()
	}
	if on == 0 {
		return nil, "performance_schema is off on this server"
	}
	var enabled string
	err := c.QueryRowContext(ctx, "SELECT ENABLED FROM performance_schema.setup_instruments WHERE NAME = ?", mdlInstrument).Scan(&enabled)
	switch {
	case errors.Is(err, sql.ErrNoRows) || err == nil && enabled != "YES":
		return nil, "the " + mdlInstrument + " instrument is off, so performance_schema.metadata_locks is empty"
	case err != nil:
		return nil, "performance_schema.metadata_locks is not readable (" + err.Error() + ")"
	}
	var cond []string
	var args []any
	for _, r := range rels {
		cond = append(cond, "(ml.OBJECT_SCHEMA = ? AND ml.OBJECT_NAME = ?)")
		args = append(args, r.schema, r.name)
	}
	// events_statements_current keeps a connection's last statement after it
	// finishes, which names what an idle transaction ran.
	rows, err := c.QueryContext(ctx, `SELECT ml.OBJECT_SCHEMA, ml.OBJECT_NAME, ml.LOCK_TYPE, ml.LOCK_STATUS = 'GRANTED', t.PROCESSLIST_ID,
			coalesce(t.PROCESSLIST_USER, ''), coalesce(t.PROCESSLIST_COMMAND, ''), coalesce(t.PROCESSLIST_STATE, ''),
			coalesce(t.PROCESSLIST_TIME, 0), coalesce(t.PROCESSLIST_INFO, e.SQL_TEXT, '')
		FROM performance_schema.metadata_locks ml
		JOIN performance_schema.threads t ON t.THREAD_ID = ml.OWNER_THREAD_ID
		LEFT JOIN performance_schema.events_statements_current e ON e.THREAD_ID = t.THREAD_ID
		WHERE ml.OBJECT_TYPE = 'TABLE' AND ml.LOCK_STATUS IN ('GRANTED', 'PENDING')
			AND t.PROCESSLIST_ID IS NOT NULL AND t.PROCESSLIST_ID <> CONNECTION_ID() AND (`+strings.Join(cond, " OR ")+`)
		ORDER BY ml.LOCK_STATUS = 'GRANTED' DESC, t.PROCESSLIST_ID
		LIMIT 500`, args...)
	if err != nil {
		return nil, "performance_schema.metadata_locks is not readable (" + err.Error() + ")"
	}
	defer rows.Close()
	var out []mdl
	for rows.Next() {
		var l mdl
		if err := rows.Scan(&l.schema, &l.name, &l.lockType, &l.granted, &l.id, &l.user, &l.command, &l.state, &l.time, &l.query); err != nil {
			return nil, "performance_schema.metadata_locks is not readable (" + err.Error() + ")"
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, "performance_schema.metadata_locks is not readable (" + err.Error() + ")"
	}
	return out, ""
}

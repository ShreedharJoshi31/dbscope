package migrate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/migrate/rules"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// BlockedNow is the rule id of a statement that would wait, right now, for a
// lock another session holds or is already waiting for.
const BlockedNow = "DBS-BLOCKED-NOW"

// maxQueryText bounds Blocker.Query.
const maxQueryText = 200

// TruncateQuery shortens a session's query text for Blocker.Query.
func TruncateQuery(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > maxQueryText {
		return string(r[:maxQueryText-3]) + "..."
	}
	return q
}

// conflicts is the "Conflicting Lock Modes" table of the PostgreSQL docs
// (Explicit Locking, Table-Level Lock Modes): conflicts[a] holds the modes a conflicts with.
var conflicts = map[rules.Mode][]rules.Mode{
	rules.AccessShare:          {rules.AccessExclusive},
	rules.RowShare:             {rules.Exclusive, rules.AccessExclusive},
	rules.RowExclusive:         {rules.Share, rules.ShareRowExclusive, rules.Exclusive, rules.AccessExclusive},
	rules.ShareUpdateExclusive: {rules.ShareUpdateExclusive, rules.Share, rules.ShareRowExclusive, rules.Exclusive, rules.AccessExclusive},
	rules.Share:                {rules.RowExclusive, rules.ShareUpdateExclusive, rules.ShareRowExclusive, rules.Exclusive, rules.AccessExclusive},
	rules.ShareRowExclusive:    {rules.RowExclusive, rules.ShareUpdateExclusive, rules.Share, rules.ShareRowExclusive, rules.Exclusive, rules.AccessExclusive},
	rules.Exclusive:            {rules.RowShare, rules.RowExclusive, rules.ShareUpdateExclusive, rules.Share, rules.ShareRowExclusive, rules.Exclusive, rules.AccessExclusive},
	rules.AccessExclusive:      {rules.AccessShare, rules.RowShare, rules.RowExclusive, rules.ShareUpdateExclusive, rules.Share, rules.ShareRowExclusive, rules.Exclusive, rules.AccessExclusive},
}

func modeFromSQL(s string) rules.Mode {
	for m := rules.AccessShare; m <= rules.AccessExclusive; m++ {
		if m.String() == s {
			return m
		}
	}
	return rules.NoLock
}

// pgLock is one row of pg_locks on a relation the migration locks, with its session.
type pgLock struct {
	relation, user, state, query string
	pid                          int32
	mode                         rules.Mode
	granted, visible             bool
	age                          float64
}

// readBlockers reads pg_locks and pg_stat_activity for the relations the
// statements lock, fills rep.Blockers and adds DBS-BLOCKED-NOW to every
// statement that would wait. dbscope's own sessions (application_name
// "dbscope...") are left out. A failed read becomes a warning.
func readBlockers(ctx context.Context, b *Base, rep *Report) {
	var schemas, names []string
	seen := map[string]bool{}
	for _, s := range rep.Statements {
		for _, l := range s.Locks {
			if !seen[l.Relation] {
				seen[l.Relation] = true
				q := splitKey(l.Relation)
				schemas, names = append(schemas, q.Schema), append(names, q.Name)
			}
		}
	}
	if len(names) == 0 {
		return
	}
	locks, err := queryLocks(ctx, b, schemas, names)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "sessions that would block the migration were not read: "+err.Error())
		return
	}
	now := time.Now().UTC()
	rep.BlockersAt = &now
	hidden := 0
	for _, l := range locks {
		if !l.visible {
			hidden++
		}
	}
	if hidden > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s on the migration's tables belong to other users' sessions whose state and query are hidden from this role; grant it pg_read_all_stats to see them", plural(hidden, "lock")))
	}
	type key struct {
		pid int32
		rel string
	}
	index := map[key]int{}
	for i := range rep.Statements {
		s := &rep.Statements[i]
		var named []string
		for _, need := range s.Locks {
			want := modeFromSQL(need.Mode)
			for _, l := range locks {
				if l.relation != need.Relation || !slices.Contains(conflicts[want], l.mode) {
					continue
				}
				k := key{l.pid, l.relation}
				j, seen := index[k]
				if !seen {
					j = len(rep.Blockers)
					index[k] = j
					rep.Blockers = append(rep.Blockers, Blocker{Relation: l.relation, Session: fmt.Sprint(l.pid), User: l.user,
						State: l.state, AgeSeconds: l.age, Query: l.query, Lock: heldText(l)})
				} else if bl := &rep.Blockers[j]; l.granted && strings.HasSuffix(bl.Lock, "(waiting)") {
					bl.Lock = heldText(l)
				}
				desc := DescribeBlocker(rep.Blockers[j], fmt.Sprintf("pid %d", l.pid))
				if !slices.Contains(named, desc) {
					named = append(named, desc)
				}
			}
		}
		if len(named) == 0 {
			continue
		}
		MarkBlocked(rep, s, named, "; while it waits, every later query on the table queues behind it",
			"Wait for the session to finish, or end it (SELECT pg_terminate_backend(pid)), and SET lock_timeout = '2s' before the statement so it gives up instead of queueing others")
	}
}

// MarkBlocked adds DBS-BLOCKED-NOW to s, naming the blockers (DescribeBlocker) it would wait behind.
func MarkBlocked(rep *Report, s *Statement, named []string, effect, alt string) {
	s.Findings = append(s.Findings, Finding{RuleID: BlockedNow, Rating: score.High,
		Message: "would wait right now behind " + joinBlockers(named) + effect, SafeAlternative: alt})
	s.Rating = score.Max(s.Rating, score.High)
	rep.Rating = score.Max(rep.Rating, s.Rating)
}

func heldText(l pgLock) string {
	if l.granted {
		return l.mode.String()
	}
	return l.mode.String() + " (waiting)"
}

// DescribeBlocker words one blocker for a finding: who, in what state, for
// how long, and what it holds or waits for. A Lock ending in " (waiting)" is
// a lock the session is itself queued for.
func DescribeBlocker(bl Blocker, who string) string {
	s := who
	if bl.User != "" {
		s += " (" + bl.User + ")"
	}
	state := bl.State
	if state == "" {
		state = "unknown state"
	}
	s += fmt.Sprintf(", %s for %s, ", state, AgeText(bl.AgeSeconds))
	if lock, ok := strings.CutSuffix(bl.Lock, " (waiting)"); ok {
		s += "already waiting for " + lock + " on " + bl.Relation
	} else {
		s += "holding " + bl.Lock + " on " + bl.Relation
	}
	if bl.Query != "" {
		s += fmt.Sprintf(" (last query: %s)", bl.Query)
	}
	return s
}

// joinBlockers names at most three blockers.
func joinBlockers(named []string) string {
	if len(named) > 3 {
		return strings.Join(named[:3], "; ") + fmt.Sprintf("; and %d more", len(named)-3)
	}
	return strings.Join(named, "; ")
}

// AgeText renders seconds as "42 s" or "3 min 5 s".
func AgeText(sec float64) string {
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(sec))
	}
	return d.Round(time.Second).String()
}

func queryLocks(ctx context.Context, b *Base, schemas, names []string) ([]pgLock, error) {
	tx, err := b.live.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if ms := b.timeout.Milliseconds(); ms > 0 {
		if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", fmt.Sprint(ms)); err != nil {
			return nil, err
		}
	}
	// pg_locks shows every session's locks; pg_stat_activity hides other
	// users' state and query unless the role has pg_read_all_stats.
	rows, err := tx.Query(ctx, `SELECT n.nspname || '.' || c.relname, l.pid, l.mode, l.granted,
			coalesce(a.usename::text, ''), coalesce(a.state, ''), a.state IS NOT NULL,
			coalesce(extract(epoch FROM now() - coalesce(a.xact_start, a.query_start))::float8, 0),
			coalesce(a.query, '')
		FROM pg_locks l
		JOIN pg_class c ON c.oid = l.relation
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN unnest($1::text[], $2::text[]) AS want(nsp, rel) ON want.nsp = n.nspname AND want.rel = c.relname
		LEFT JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'relation' AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
			AND l.pid IS NOT NULL AND l.pid <> pg_backend_pid()
			AND coalesce(a.application_name, '') NOT LIKE 'dbscope%'
		ORDER BY l.granted DESC, l.pid
		LIMIT 500`, schemas, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pgLock
	for rows.Next() {
		var l pgLock
		var mode string
		if err := rows.Scan(&l.relation, &l.pid, &mode, &l.granted, &l.user, &l.state, &l.visible, &l.age, &l.query); err != nil {
			return nil, err
		}
		l.mode = rules.ModeFromLockName(mode)
		l.query = TruncateQuery(l.query)
		if !l.granted && l.state != "" {
			l.state += ", waiting for a lock"
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

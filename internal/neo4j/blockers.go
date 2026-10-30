package neo4j

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/dbscope/dbscope/internal/migrate"
)

// waitsOnLabels reports whether a statement waits for other transactions'
// locks on the labels it names: schema commands take an exclusive lock on
// the label, and an unbatched write locks every entry it matches.
func waitsOnLabels(s migrate.Statement) bool {
	switch s.Kind {
	case "CREATE INDEX", "CREATE CONSTRAINT", "DROP INDEX", "DROP CONSTRAINT":
		return true
	}
	return slices.ContainsFunc(s.Findings, func(f migrate.Finding) bool { return f.RuleID == "DBS-NEO4J-UNBATCHED" })
}

// neoTx is one row of SHOW TRANSACTIONS.
type neoTx struct {
	id, user, query, status, details string
	locks                            int64
	age                              float64
	labels                           []string
}

// readBlockers reads SHOW TRANSACTIONS and adds DBS-BLOCKED-NOW to the
// schema commands and unbatched writes that would wait for an open
// transaction. SHOW TRANSACTIONS does not say which labels a transaction
// locked, so dbscope keeps those whose current query names one of the
// statement's labels and holds locks; transactions holding locks between
// queries are only listed in a warning. Neo4j waits for a lock with no time
// limit unless db.lock.acquisition.timeout is set (it defaults to 0, Neo4j
// Operations Manual, "Configuration settings").
func readBlockers(ctx context.Context, b *MigrationBase, rep *migrate.Report) {
	var labels []string
	for _, s := range rep.Statements {
		if waitsOnLabels(s) {
			for _, r := range s.Relations {
				if !slices.Contains(labels, r) {
					labels = append(labels, r)
				}
			}
		}
	}
	if len(labels) == 0 {
		return
	}
	recs, _, err := b.client.read(ctx, b.timeout, `SHOW TRANSACTIONS YIELD transactionId, username, currentQuery, status, statusDetails, activeLockCount, elapsedTime
		WHERE activeLockCount > 0 AND NOT coalesce(currentQuery, '') STARTS WITH 'SHOW TRANSACTIONS'
		RETURN transactionId, username, coalesce(currentQuery, ''), status, coalesce(statusDetails, ''), activeLockCount, elapsedTime
		ORDER BY elapsedTime DESC LIMIT 500`, nil)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "open transactions that would block the migration were not read: "+err.Error())
		return
	}
	now := time.Now().UTC()
	rep.BlockersAt = &now
	var txs []neoTx
	var idle []string
	for _, r := range recs {
		t := neoTx{}
		t.id, _ = r.Values[0].(string)
		t.user, _ = r.Values[1].(string)
		t.query, _ = r.Values[2].(string)
		t.status, _ = r.Values[3].(string)
		t.details, _ = r.Values[4].(string)
		t.locks, _ = r.Values[5].(int64)
		if d, ok := r.Values[6].(dbtype.Duration); ok {
			t.age = float64(d.Days)*86400 + float64(d.Seconds) + float64(d.Nanos)/1e9
		}
		if t.query == "" {
			idle = append(idle, fmt.Sprintf("%s (%s, open for %s, %s held)", t.id, t.user, migrate.AgeText(t.age), plural(t.locks, "lock")))
			continue
		}
		toks := tokens(t.query)
		for i := 0; i+1 < len(toks); i++ {
			if toks[i].Text == ":" {
				t.labels = append(t.labels, strings.Trim(toks[i+1].Text, "`"))
			}
		}
		t.query = migrate.TruncateQuery(t.query)
		txs = append(txs, t)
	}
	if len(idle) > 0 {
		rep.Warnings = append(rep.Warnings, "these open transactions hold locks but run no query, so dbscope cannot tell which labels they wrote; a schema command or large write on one of those labels waits for them with no time limit: "+strings.Join(idle, "; "))
	}
	index := map[string]int{}
	for i := range rep.Statements {
		s := &rep.Statements[i]
		if !waitsOnLabels(*s) {
			continue
		}
		var named []string
		for _, label := range s.Relations {
			for _, t := range txs {
				if !slices.Contains(t.labels, label) {
					continue
				}
				k := t.id + "\x00" + label
				j, ok := index[k]
				if !ok {
					j = len(rep.Blockers)
					index[k] = j
					state := strings.ToLower(t.status[:min(1, len(t.status))]) + t.status[min(1, len(t.status)):]
					if t.details != "" {
						state += ", " + t.details
					}
					lock := plural(t.locks, "lock")
					if strings.HasPrefix(t.status, "Blocked by") {
						lock += " (waiting)"
					}
					rep.Blockers = append(rep.Blockers, migrate.Blocker{Relation: label, Session: t.id, User: t.user, State: state,
						AgeSeconds: t.age, Query: t.query, Lock: lock})
				}
				desc := migrate.DescribeBlocker(rep.Blockers[j], "transaction "+t.id)
				if !slices.Contains(named, desc) {
					named = append(named, desc)
				}
			}
		}
		if len(named) == 0 {
			continue
		}
		migrate.MarkBlocked(rep, s, named, " (matched by its query naming the same label; SHOW TRANSACTIONS does not list the locks themselves), and Neo4j waits for a lock with no time limit unless db.lock.acquisition.timeout is set",
			"Wait for the transaction to finish, or end it (TERMINATE TRANSACTION 'id'), and set db.lock.acquisition.timeout or run the migration with a transaction timeout")
		s.Score = max(s.Score, 8)
	}
}

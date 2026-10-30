package neo4j

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// ErrMemgraphWorkload means Memgraph has no query statistics to read.
var ErrMemgraphWorkload = errors.New("workload is not available for Memgraph: it keeps no per-query statistics")

// maxCollected bounds the collected queries read; the collector itself keeps at most 8,192.
const maxCollected = 2000

// Workload reads what Neo4j's data collector gathered (db.stats.retrieve)
// and attributes each query's calls and time to the labels its text names.
// dbscope never starts or stops the collector: that changes server state, so
// when nothing was collected the snapshot says how an admin starts it.
func Workload(ctx context.Context, c *Client, m *schema.Model) (*workload.Snapshot, error) {
	if mg, err := c.Memgraph(ctx); err != nil {
		return nil, err
	} else if mg {
		return nil, ErrMemgraphWorkload
	}
	snap := &workload.Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*workload.Table{},
		Statements: workload.Statements{Status: workload.StatusOK}}
	status := ""
	if recs, _, err := c.read(ctx, 10*time.Second, "CALL db.stats.status() YIELD section, status WHERE section = 'QUERIES' RETURN status", nil); err == nil && len(recs) > 0 {
		status, _ = recs[0].Values[0].(string)
	}
	recs, _, err := c.read(ctx, 30*time.Second, `CALL db.stats.retrieve('QUERIES') YIELD data
		RETURN data.query AS query, data.invocationSummary.invocationCount AS calls,
		       data.invocationSummary.executionTimeInUs.avg AS avgUs LIMIT $max`, map[string]any{"max": int64(maxCollected)})
	if err != nil {
		snap.Statements.Status, snap.Statements.Message = workload.StatusUnavailable, err.Error()
		return snap, nil
	}
	if len(recs) == 0 {
		snap.Statements.Status = workload.StatusUnavailable
		snap.Statements.Message = "Neo4j's data collector has no queries"
		if status != "collecting" {
			snap.Statements.Message += ": an admin starts it with CALL db.stats.collect('QUERIES', {durationSeconds: 3600}); let the application run, then reload. dbscope only reads what it gathers."
		} else {
			snap.Statements.Message += " yet: it is collecting; reload once the application has run."
		}
		return snap, nil
	}
	labels := map[string]string{}
	for _, r := range m.Relations {
		labels[r.Name] = r.QName().String()
	}
	for _, rec := range recs {
		text, _ := rec.Values[0].(string)
		calls, _ := rec.Values[1].(int64)
		avg, _ := rec.Values[2].(float64)
		if a, ok := rec.Values[2].(int64); ok {
			avg = float64(a)
		}
		snap.Statements.Read++
		// Catalog calls, dbscope's own among them, say nothing about the application's data.
		if lower := strings.ToLower(strings.TrimSpace(text)); strings.HasPrefix(lower, "call db.") || strings.HasPrefix(lower, "call dbms.") ||
			strings.HasPrefix(lower, "show ") || strings.HasPrefix(lower, "explain ") {
			continue
		}
		totalMs := avg * float64(calls) / 1000
		snap.Statements.TotalCalls += calls
		snap.Statements.TotalTimeMs += totalMs
		sum := sha256.Sum256([]byte(text))
		q := workload.Query{QueryID: hex.EncodeToString(sum[:8]), Text: text, Calls: calls, TotalMs: totalMs, MeanMs: avg / 1000}
		seen := map[string]bool{}
		toks := tokens(text)
		for i := 0; i+1 < len(toks); i++ {
			if toks[i].Text != ":" {
				continue
			}
			key := labels[strings.Trim(toks[i+1].Text, "`")]
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			t := snap.Tables[key]
			if t == nil {
				t = &workload.Table{}
				snap.Tables[key] = t
			}
			t.QueryCalls += calls
			t.QueryTimeMs += totalMs
			workload.AddTopQuery(t, q)
		}
		if len(seen) == 0 {
			snap.Statements.UnattributedCalls += calls
		}
	}
	return snap, nil
}

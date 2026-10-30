package elastic

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// statsGroup tags dbscope's own searches ("stats": ["dbscope"] in a search
// body); Workload subtracts the group from each index's search counters.
const statsGroup = "dbscope"

const noQueryHistory = "Elasticsearch and OpenSearch keep no history of search requests, so per-query text is not available " +
	"(the search slow log is a file on each node). Calls, time and writes are per index, from index stats."

// monitorIndexGrant is what a user needs to read index stats.
const monitorIndexGrant = "This user may not read index stats: give it the monitor index privilege on %s " +
	"(OpenSearch: the indices_monitor action group, or indices:monitor/stats)."

type searchStats struct {
	QueryTotal  int64 `json:"query_total"`
	QueryTimeMs int64 `json:"query_time_in_millis"`
}

type indexStats struct {
	Primaries struct {
		Docs struct {
			Count int64 `json:"count"`
		} `json:"docs"`
		Indexing struct {
			IndexTotal  int64 `json:"index_total"`
			DeleteTotal int64 `json:"delete_total"`
		} `json:"indexing"`
	} `json:"primaries"`
	Total struct {
		Search struct {
			searchStats
			Groups map[string]searchStats `json:"groups"`
		} `json:"search"`
	} `json:"total"`
}

const statsFilter = "indices.*.primaries.docs.count,indices.*.primaries.indexing.index_total,indices.*.primaries.indexing.delete_total," +
	"indices.*.total.search.query_total,indices.*.total.search.query_time_in_millis,indices.*.total.search.groups"

// Workload reads each index's search and indexing counters from index stats.
// There is no statement text to attribute, so calls and time come per index
// and TopQueries stays empty.
//
// Counters live on each shard copy and count from when that copy started
// (node restart, relocation, index open), with no reset time. Search uses
// total: replicas serve searches too, and each search runs its query phase
// once on one copy of every shard it targets, so query_total counts shard
// searches, not requests. Writes and documents use primaries: total would
// count each write again on every replica.
//
// A data stream's backing indices add up to the data stream. Aliases get no
// entry: their searches are counted on the indices behind them, which are on
// the canvas already. Dot-prefixed system indices are left out, as they are
// in Introspect.
func Workload(ctx context.Context, c *Client, m *schema.Model) (*workload.Snapshot, error) {
	snap := &workload.Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*workload.Table{},
		Statements: workload.Statements{Status: workload.StatusNotLoaded, Message: noQueryHistory}}
	pattern := url.PathEscape(c.Pattern)
	var stats struct {
		Indices map[string]indexStats `json:"indices"`
	}
	if err := c.do(ctx, "GET", "/"+pattern+"/_stats/search,indexing,docs?groups="+statsGroup+"&filter_path="+statsFilter, nil, &stats); err != nil {
		var e *Error
		if errors.As(err, &e) && (e.Status == 401 || e.Status == 403) {
			snap.Statements.Status, snap.Statements.Message = workload.StatusUnavailable, fmt.Sprintf(monitorIndexGrant, c.Pattern)
			return snap, nil
		}
		return nil, fmt.Errorf("reading index stats: %w", err)
	}
	var resolved struct {
		DataStreams []struct {
			Name           string   `json:"name"`
			BackingIndices []string `json:"backing_indices"`
		} `json:"data_streams"`
	}
	if err := c.do(ctx, "GET", "/_resolve/index/"+pattern, nil, &resolved); err != nil {
		return nil, fmt.Errorf("listing data streams: %w", err)
	}
	stream := map[string]string{}
	for _, ds := range resolved.DataStreams {
		for _, b := range ds.BackingIndices {
			stream[b] = ds.Name
		}
	}
	keys := map[string]string{}
	for _, r := range m.Relations {
		if r.Kind != schema.KindView {
			keys[r.Name] = r.QName().String()
		}
	}
	for name, st := range stats.Indices {
		if ds, ok := stream[name]; ok {
			name = ds
		}
		// OpenSearch's audit log is a system index without the dot.
		if (strings.HasPrefix(name, ".") && !strings.HasPrefix(c.Pattern, ".")) || strings.HasPrefix(name, "security-auditlog-") {
			continue
		}
		search := st.Total.Search
		calls, ms := search.QueryTotal, search.QueryTimeMs
		if g, ok := search.Groups[statsGroup]; ok {
			calls, ms = calls-g.QueryTotal, ms-g.QueryTimeMs
		}
		snap.Statements.TotalCalls += calls
		snap.Statements.TotalTimeMs += float64(ms)
		key := keys[name]
		if key == "" {
			// Created after the model was read, or outside it.
			snap.Statements.UnattributedCalls += calls
			continue
		}
		t := snap.Tables[key]
		if t == nil {
			t = &workload.Table{}
			snap.Tables[key] = t
		}
		t.QueryCalls += calls
		t.QueryTimeMs += float64(ms)
		t.RowsWritten += st.Primaries.Indexing.IndexTotal + st.Primaries.Indexing.DeleteTotal
		// Nested objects are hidden documents and count here too.
		t.LiveRows += st.Primaries.Docs.Count
	}
	return snap, nil
}

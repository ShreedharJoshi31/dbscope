package elastic

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/health"
)

// monitorGrant is the privilege the cluster-wide checks need.
const monitorGrant = "The user may not read cluster state: give it the monitor cluster privilege (OpenSearch: cluster_monitor)."

// Health reads the cluster's health checks (docs/HEALTH.md) from
// _cluster/health, _nodes/stats, _cat/allocation and index settings.
func Health(ctx context.Context, c *Client) (*health.Report, error) {
	start := time.Now()
	srv, err := c.describe(ctx)
	if err != nil {
		return nil, err
	}
	name := map[string]string{"elasticsearch": "Elasticsearch", "opensearch": "OpenSearch"}[srv.Product]
	r := &health.Report{Engine: srv.Product, Server: name + " " + srv.Version, Checks: []health.Check{}}
	why := func(err error) string {
		var e *Error
		if errors.As(err, &e) && (e.Status == 401 || e.Status == 403) {
			return monitorGrant
		}
		return err.Error()
	}

	var ch struct {
		Status     string `json:"status"`
		Unassigned int    `json:"unassigned_shards"`
		Relocating int    `json:"relocating_shards"`
		Pending    int    `json:"number_of_pending_tasks"`
		Nodes      int    `json:"number_of_nodes"`
		Active     int    `json:"active_shards"`
	}
	if err := c.do(ctx, "GET", "/_cluster/health", nil, &ch); err != nil {
		r.Unavailable("es.cluster_status", "Cluster status", why(err))
	} else {
		st := map[string]health.Status{"green": health.OK, "yellow": health.Warn, "red": health.Danger}[ch.Status]
		chk := health.Check{ID: "es.cluster_status", Title: "Cluster status", Status: st, Value: ch.Status,
			Detail: fmt.Sprintf("%d nodes, %d active shards, %d unassigned, %d relocating.", ch.Nodes, ch.Active, ch.Unassigned, ch.Relocating)}
		switch ch.Status {
		case "yellow":
			chk.Detail += " Every primary is assigned, but some replicas are not: losing a node may lose data."
			chk.Fix = "GET _cluster/allocation/explain says why; on one node, replicas can never be assigned, so set number_of_replicas to 0 there"
		case "red":
			chk.Detail += " Some primaries are unassigned: their documents cannot be searched or written."
			chk.Fix = "GET _cluster/allocation/explain for the unassigned primaries"
		}
		r.Add(chk)
		r.Add(health.Check{ID: "es.pending_tasks", Title: "Pending cluster tasks", Status: health.Level(float64(ch.Pending), 10, 100),
			Value: strconv.Itoa(ch.Pending), Detail: "Cluster state changes the master has not applied yet.",
			Fix: "GET _cluster/pending_tasks shows them; a long queue usually means too many shards or mapping updates"})
	}

	// Disk use per node against the allocation watermarks.
	var alloc []struct {
		Node    string `json:"node"`
		Percent string `json:"disk.percent"`
		Shards  string `json:"shards"`
	}
	if err := c.do(ctx, "GET", "/_cat/allocation?format=json&h=node,disk.percent,shards", nil, &alloc); err != nil {
		r.Unavailable("es.disk", "Disk use", why(err))
	} else {
		worst, node, maxShards := 0.0, "", 0
		for _, a := range alloc {
			if p, err := strconv.ParseFloat(a.Percent, 64); err == nil && p > worst {
				worst, node = p, a.Node
			}
			n, _ := strconv.Atoi(a.Shards)
			maxShards = max(maxShards, n)
		}
		r.Add(health.Check{ID: "es.disk", Title: "Disk use", Status: health.Level(worst, 85, 90), Value: fmt.Sprintf("%.0f%% on %s", worst, node),
			Detail: "At 85% (the low watermark) no new shards go to the node, at 90% shards move away, and at 95% indices on it become read-only.",
			Fix:    "Delete or shrink old indices, add disk or nodes; check ILM moves data off hot nodes"})
		var limit struct {
			Persistent, Transient, Defaults map[string]string
		}
		perNode := 1000
		if c.do(ctx, "GET", "/_cluster/settings?include_defaults=true&flat_settings=true&filter_path=*.cluster.max_shards_per_node", nil, &limit) == nil {
			for _, m := range []map[string]string{limit.Defaults, limit.Persistent, limit.Transient} {
				if v, err := strconv.Atoi(m["cluster.max_shards_per_node"]); err == nil {
					perNode = v
				}
			}
		}
		r.Add(health.Check{ID: "es.shards_per_node", Title: "Shards per node", Status: health.Level(float64(maxShards)/float64(perNode), 0.8, 0.95),
			Value:  fmt.Sprintf("up to %d of %d", maxShards, perNode),
			Detail: "At cluster.max_shards_per_node, creating an index fails; many small shards also cost heap.",
			Fix:    "Use fewer primaries per index, roll over by size rather than by day, and shrink or delete small old indices"})
	}

	// JVM heap per node.
	var nodes struct {
		Nodes map[string]struct {
			Name string `json:"name"`
			JVM  struct {
				Mem struct {
					HeapUsedPercent float64 `json:"heap_used_percent"`
				} `json:"mem"`
			} `json:"jvm"`
		} `json:"nodes"`
	}
	if err := c.do(ctx, "GET", "/_nodes/stats/jvm", nil, &nodes); err != nil {
		r.Unavailable("es.heap", "JVM heap", why(err))
	} else {
		worst, node := 0.0, ""
		for _, n := range nodes.Nodes {
			if n.JVM.Mem.HeapUsedPercent > worst {
				worst, node = n.JVM.Mem.HeapUsedPercent, n.Name
			}
		}
		r.Add(health.Check{ID: "es.heap", Title: "JVM heap", Status: health.Level(worst, 75, 90), Value: fmt.Sprintf("%.0f%% on %s", worst, node),
			Detail: "Heap use rises and falls with garbage collection; staying above 75% means long collections and, near 100%, circuit breakers rejecting requests.",
			Fix:    "Reduce shard count and large aggregations, or give the nodes more heap (up to half of RAM, below 32 GB)"})
	}

	// Indices left read-only by the flood-stage watermark stay read-only after disk frees up on older versions.
	var settings map[string]struct {
		Settings map[string]string `json:"settings"`
	}
	if err := c.do(ctx, "GET", "/"+url.PathEscape(c.Pattern)+"/_settings/index.blocks.*?flat_settings=true", nil, &settings); err != nil {
		r.Unavailable("es.blocked_indices", "Indices blocked for writes", why(err))
	} else {
		var names []string
		for ix, s := range settings {
			if s.Settings["index.blocks.read_only_allow_delete"] == "true" || s.Settings["index.blocks.read_only"] == "true" || s.Settings["index.blocks.write"] == "true" {
				names = append(names, ix)
			}
		}
		chk := health.Check{ID: "es.blocked_indices", Title: "Indices blocked for writes", Status: health.OK, Value: "none"}
		if len(names) > 0 {
			sort.Strings(names)
			chk.Status, chk.Value = health.Danger, strconv.Itoa(len(names))
			chk.Detail = "Writes to them fail: " + strings.Join(names, ", ")
			chk.Fix = "Free disk below the flood stage, then PUT <index>/_settings {\"index.blocks.read_only_allow_delete\": null}"
		}
		r.Add(chk)
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

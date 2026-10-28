package elastic

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/mytest"
)

func healthCheck(t *testing.T, r *health.Report, id string) health.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %+v", id, r.Checks)
	return health.Check{}
}

// TestHealth makes a single-node cluster yellow with a replica it cannot
// assign, blocks writes to an index, and checks that a user without the
// monitor privilege is refused outright.
func TestHealth(t *testing.T) {
	for _, name := range []string{"es9", "os3"} {
		t.Run(name, func(t *testing.T) {
			p := mytest.Elastic(t, name)
			ctx := context.Background()
			c, err := Connect(p, mytest.ElasticPassword)
			if err != nil {
				t.Fatal(err)
			}
			for _, req := range [][3]any{
				{"PUT", "/replicated", map[string]any{"settings": map[string]any{"number_of_replicas": 1}}},
				{"PUT", "/frozen", map[string]any{"settings": map[string]any{"number_of_replicas": 0}}},
				{"PUT", "/frozen/_settings", map[string]any{"index.blocks.write": true}},
			} {
				if err := c.do(ctx, req[0].(string), req[1].(string), req[2], nil); err != nil {
					t.Fatal(req[1], err)
				}
			}
			r, err := Health(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			if ch := healthCheck(t, r, "es.cluster_status"); ch.Status != health.Warn || ch.Value != "yellow" || !strings.Contains(ch.Detail, "1 unassigned") {
				t.Errorf("cluster status %+v", ch)
			}
			if ch := healthCheck(t, r, "es.blocked_indices"); ch.Status != health.Danger || !strings.Contains(ch.Detail, "frozen") {
				t.Errorf("blocked %+v", ch)
			}
			for _, id := range []string{"es.pending_tasks", "es.shards_per_node", "es.heap", "es.disk"} {
				if ch := healthCheck(t, r, id); ch.Status == health.Unknown {
					t.Errorf("%s %+v", id, ch)
				}
			}

			opensearch := name == "os3"
			calls := [][3]any{
				{"POST", "/_security/role/indexonly", map[string]any{"indices": []map[string]any{{"names": []string{"*"}, "privileges": []string{"read", "view_index_metadata"}}}}},
				{"POST", "/_security/user/indexonly", map[string]any{"password": "Index-Only-Pass-2026!", "roles": []string{"indexonly"}}},
			}
			if opensearch {
				calls = [][3]any{
					{"PUT", "/_plugins/_security/api/roles/indexonly", map[string]any{"index_permissions": []map[string]any{{"index_patterns": []string{"*"}, "allowed_actions": []string{"read", "indices:admin/settings/get"}}}}},
					{"PUT", "/_plugins/_security/api/internalusers/indexonly", map[string]any{"password": "Index-Only-Pass-2026!"}},
					{"PUT", "/_plugins/_security/api/rolesmapping/indexonly", map[string]any{"users": []string{"indexonly"}}},
				}
			}
			for _, call := range calls {
				if err := c.do(ctx, call[0].(string), call[1].(string), call[2], nil); err != nil {
					t.Fatal(call[1], err)
				}
			}
			limited := p
			limited.User = "indexonly"
			lc, err := Connect(limited, "Index-Only-Pass-2026!")
			if err != nil {
				t.Fatal(err)
			}
			// Without monitor even GET / is refused, so dbscope cannot connect as this user at all.
			if _, err := Health(ctx, lc); err == nil || !strings.Contains(err.Error(), "cluster:monitor/main") {
				t.Errorf("index-only user: %v", err)
			}
		})
	}
}

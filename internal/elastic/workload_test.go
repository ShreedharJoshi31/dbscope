package elastic

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/workload"
)

// TestWorkload runs a known workload on an index with one shard, one with
// two, a data stream and an alias, and reads index stats before and after:
// as the superuser, as a user without the monitor index privilege, and as
// one with it. Servers run one at a time to stay within Docker's memory.
func TestWorkload(t *testing.T) {
	for _, name := range []string{"es9", "os3"} {
		t.Run(name, func(t *testing.T) { testWorkload(t, name) })
	}
}

func testWorkload(t *testing.T, name string) {
	p := mytest.Elastic(t, name)
	ctx := context.Background()
	c, err := Connect(p, mytest.ElasticPassword)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any) {
		t.Helper()
		if err := c.do(ctx, method, path, body, nil); err != nil {
			t.Fatal(method, path, err)
		}
	}
	shards := func(n int) map[string]any {
		return map[string]any{"settings": map[string]any{"number_of_shards": n, "number_of_replicas": 0}}
	}
	do("PUT", "/orders", shards(1))
	do("PUT", "/events", shards(2))
	do("PUT", "/_index_template/applogs", map[string]any{"index_patterns": []string{"applogs*"}, "data_stream": map[string]any{},
		"template": shards(1)})
	for _, id := range []string{"1", "2", "3"} {
		do("PUT", "/orders/_doc/"+id+"?refresh=true", map[string]any{"status": "paid", "n": id})
	}
	do("DELETE", "/orders/_doc/3?refresh=true", nil)
	for _, id := range []string{"a", "b"} {
		do("PUT", "/events/_doc/"+id+"?refresh=true", map[string]any{"kind": "click"})
	}
	do("POST", "/applogs/_doc?refresh=true", map[string]any{"@timestamp": "2026-10-30T00:00:00Z", "msg": "hello"})
	do("POST", "/_aliases", map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": "orders", "alias": "orders_all"}}}})

	m, err := Introspect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Workload(ctx, c, m)
	if err != nil {
		t.Fatal(err)
	}
	app := map[string]any{"query": map[string]any{"term": map[string]any{"status.keyword": "paid"}}}
	for range 4 {
		do("POST", "/orders/_search", app)
	}
	do("POST", "/orders/_count", map[string]any{})
	do("POST", "/orders_all/_search", app)
	for range 3 {
		do("POST", "/events/_search", map[string]any{})
	}
	for range 2 {
		do("POST", "/applogs/_search", map[string]any{})
	}
	do("POST", "/orders/_search", map[string]any{"stats": []string{statsGroup}, "size": 0})
	// dbscope's own schema reads search every index too; they must not count.
	for range 2 {
		if _, err := Introspect(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	after, err := Workload(ctx, c, m)
	if err != nil {
		t.Fatal(err)
	}

	// Each search runs once per shard: 4 + 1 + 1 on one shard, 3 on two, 2 on the data stream's one backing index.
	// The search tagged with dbscope's stats group is left out.
	for key, want := range map[string][3]int64{"indices.orders": {6, 4, 2}, "indices.events": {6, 2, 2}, "indices.applogs": {2, 1, 1}} {
		a, b := after.Tables[key], before.Tables[key]
		if a == nil || b == nil {
			t.Fatalf("%s missing: %v %v", key, a, b)
		}
		if got := a.QueryCalls - b.QueryCalls; got != want[0] || a.RowsWritten != want[1] || a.LiveRows != want[2] {
			t.Errorf("%s: %d calls (want %d), %+v", key, got, want[0], a)
		}
		if a.QueryTimeMs < b.QueryTimeMs || len(a.TopQueries) > 0 {
			t.Errorf("%s %+v", key, a)
		}
	}
	keys := map[string]bool{}
	for _, r := range m.Relations {
		keys[r.QName().String()] = true
	}
	var sum int64
	for key, tb := range after.Tables {
		sum += tb.QueryCalls
		if !keys[key] || strings.HasPrefix(key, "indices.") && strings.HasPrefix(key[len("indices."):], ".") || key == "indices.orders_all" {
			t.Errorf("unexpected table %s %+v", key, tb)
		}
	}
	st := after.Statements
	if st.Status != workload.StatusNotLoaded || !strings.Contains(st.Message, "slow log") || st.TotalCalls != sum+st.UnattributedCalls ||
		st.TotalCalls-before.Statements.TotalCalls < 14 {
		t.Errorf("statements %+v (tables sum %d)", st, sum)
	}

	// Without the monitor index privilege index stats are refused; cluster monitor does not help.
	for _, user := range []struct {
		name    string
		monitor bool
	}{{"nomonitor", false}, {"withmonitor", true}} {
		const pass = "Workload-Pass-2026!"
		if name == "os3" {
			actions := []string{"read", "indices:admin/settings/get"}
			if user.monitor {
				actions = append(actions, "indices_monitor")
			}
			do("PUT", "/_plugins/_security/api/roles/"+user.name, map[string]any{"cluster_permissions": []string{"cluster_monitor"},
				"index_permissions": []map[string]any{{"index_patterns": []string{"*"}, "allowed_actions": actions}}})
			do("PUT", "/_plugins/_security/api/internalusers/"+user.name, map[string]any{"password": pass})
			do("PUT", "/_plugins/_security/api/rolesmapping/"+user.name, map[string]any{"users": []string{user.name}})
		} else {
			privs := []string{"read", "view_index_metadata"}
			if user.monitor {
				privs = append(privs, "monitor")
			}
			do("POST", "/_security/role/"+user.name, map[string]any{"cluster": []string{"monitor"},
				"indices": []map[string]any{{"names": []string{"*"}, "privileges": privs}}})
			do("POST", "/_security/user/"+user.name, map[string]any{"password": pass, "roles": []string{user.name}})
		}
		lp := p
		lp.User = user.name
		lc, err := Connect(lp, pass)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := Workload(ctx, lc, m)
		if err != nil {
			t.Fatalf("%s: %v", user.name, err)
		}
		if user.monitor {
			if snap.Statements.Status != workload.StatusNotLoaded || snap.Tables["indices.orders"] == nil || snap.Tables["indices.orders"].RowsWritten != 4 {
				t.Errorf("%s: %+v %v", user.name, snap.Statements, snap.Tables)
			}
			continue
		}
		if snap.Statements.Status != workload.StatusUnavailable || !strings.Contains(snap.Statements.Message, "monitor index privilege on *") ||
			!strings.Contains(snap.Statements.Message, "indices_monitor") || len(snap.Tables) != 0 {
			t.Errorf("%s: %+v %v", user.name, snap.Statements, snap.Tables)
		}
	}
}

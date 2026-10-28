package elastic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestParse(t *testing.T) {
	for text, ok := range map[string]bool{
		"SELECT * FROM logs": true,
		"show tables;":       true,
		`{"index": "logs", "query": {"match_all": {}}}`:          true,
		"DELETE FROM logs WHERE status = 500":                    false,
		"SELECT 1; SELECT 2":                                     false,
		`{"query": {}} {"query": {}}`:                            false,
		`{"query": {"match_all": {}}, "from": 9990, "size": 20}`: false,
		`{"scroll": "1m", "query": {"match_all": {}}}`:           false,
		`{"query": `: false,
		"":           false,
	} {
		if _, err := Parse(text, "*"); (err == nil) != ok {
			t.Errorf("Parse(%q) = %v", text, err)
		}
	}
	r, _ := Parse(`{"index": "logs-*", "size": 3}`, "*")
	if r.Index != "logs-*" || r.Body["index"] != nil {
		t.Errorf("index %+v", r)
	}
}

func TestSearchFlags(t *testing.T) {
	codes := func(index, text string) []string {
		r, err := Parse(text, index)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range searchFlags(r.Index, r.Body) {
			out = append(out, f.Code)
		}
		return out
	}
	if got := codes("*", `{"query": {"bool": {"must": [{"wildcard": {"sku": "*5"}}, {"script": {"script": "true"}}]}}}`); strings.Join(got, ",") != "all_indices,expensive_wildcard,expensive_script" {
		t.Errorf("flags %v", got)
	}
	if got := codes("logs", `{"size": 0, "aggs": {"a": {"terms": {"field": "x", "size": 50000}}}}`); strings.Join(got, ",") != "large_terms" {
		t.Errorf("terms %v", got)
	}
	if got := codes("logs", `{"query": {"term": {"status": 500}}}`); len(got) != 0 {
		t.Errorf("term %v", got)
	}
}

func TestElastic(t *testing.T) {
	for _, name := range []string{"es9", "es8", "os3", "os2"} {
		t.Run(name, func(t *testing.T) { testServer(t, name) })
	}
}

// readOnly creates a user with only read access: Elasticsearch's read,
// view_index_metadata and monitor; OpenSearch's read plus the mapping and
// index metadata actions its built-in readall roles lack.
func readOnly(t *testing.T, ctx context.Context, admin *Client, opensearch bool) (string, string) {
	t.Helper()
	calls := [][3]any{
		{"POST", "/_security/role/dbscope_ro", map[string]any{"cluster": []string{"monitor"},
			"indices": []map[string]any{{"names": []string{"*"}, "privileges": []string{"read", "view_index_metadata", "monitor"}}}}},
		{"POST", "/_security/user/ro", map[string]any{"password": "ro-test-pass", "roles": []string{"dbscope_ro"}}},
	}
	user, pass := "ro", "ro-test-pass"
	if opensearch {
		pass = "Ro-Test-Pass-2026!"
		calls = [][3]any{
			{"PUT", "/_plugins/_security/api/roles/dbscope_ro", map[string]any{"cluster_permissions": []string{"cluster_monitor"},
				"index_permissions": []map[string]any{{"index_patterns": []string{"*"}, "allowed_actions": []string{"read", "indices_monitor",
					"indices:admin/mappings/get", "indices:admin/get", "indices:admin/resolve/index", "indices:admin/data_stream/get", "indices:admin/validate/query"}}}}},
			{"PUT", "/_plugins/_security/api/internalusers/ro", map[string]any{"password": pass}},
			{"PUT", "/_plugins/_security/api/rolesmapping/dbscope_ro", map[string]any{"users": []string{"ro"}}},
		}
	}
	for _, c := range calls {
		if err := admin.do(ctx, c[0].(string), c[1].(string), c[2], nil); err != nil {
			t.Fatalf("%s %s: %v", c[0], c[1], err)
		}
	}
	return user, pass
}

func testServer(t *testing.T, name string) {
	p := mytest.Elastic(t, name)
	ctx := context.Background()
	opensearch := strings.HasPrefix(name, "os")
	admin, err := Connect(p, mytest.ElasticPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]any{
		{"PUT", "/products", map[string]any{"settings": map[string]any{"number_of_shards": 2, "number_of_replicas": 0},
			"mappings": map[string]any{"properties": map[string]any{
				"sku": map[string]any{"type": "keyword"}, "price": map[string]any{"type": "double"}, "big": map[string]any{"type": "long"},
				"title":    map[string]any{"type": "text", "fields": map[string]any{"keyword": map[string]any{"type": "keyword"}}},
				"variants": map[string]any{"type": "nested", "properties": map[string]any{"color": map[string]any{"type": "keyword"}}},
				"maker":    map[string]any{"properties": map[string]any{"name": map[string]any{"type": "keyword"}}}}}}},
		{"PUT", "/logs-a", map[string]any{"mappings": map[string]any{"properties": map[string]any{"status": map[string]any{"type": "long"}}}}},
		{"PUT", "/logs-b", map[string]any{"mappings": map[string]any{"properties": map[string]any{"status": map[string]any{"type": "keyword"}}}}},
		{"POST", "/_aliases", map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": "logs-*", "alias": "logs"}}}}},
		{"PUT", "/products/_doc/p1?refresh=true", map[string]any{"sku": "A1", "price": 9.5, "big": 9007199254740993, "title": "red mug",
			"variants": []any{map[string]any{"color": "red"}, map[string]any{"color": "blue"}}, "maker": map[string]any{"name": "acme"}}},
		{"PUT", "/products/_doc/p2?refresh=true", map[string]any{"sku": "B2", "price": 20, "title": "blue cup"}},
		{"PUT", "/logs-a/_doc/1?refresh=true", map[string]any{"status": 500}},
	} {
		if err := admin.do(ctx, c[0].(string), c[1].(string), c[2], nil); err != nil {
			t.Fatalf("%s %s: %v", c[0], c[1], err)
		}
	}

	if res := Test(ctx, p, mytest.ElasticPassword); !res.OK || len(res.WritePrivileges) == 0 {
		t.Errorf("admin test %+v %+v", res, res.Error)
	}
	ro := p
	var pass string
	ro.User, pass = readOnly(t, ctx, admin, opensearch)
	res := Test(ctx, ro, pass)
	want := conn.EngineElasticsearch
	if opensearch {
		want = conn.EngineOpenSearch
	}
	ownIndex := len(res.WritePrivileges) == 1 && strings.HasPrefix(res.WritePrivileges[0], "role own_index") && name == "os2"
	if !res.OK || res.Engine != want || (len(res.WritePrivileges) != 0 && !ownIndex) || len(res.Warnings) != 0 {
		t.Errorf("read-only test %+v %+v", res, res.Error)
	}
	if bad := Test(ctx, ro, "wrong-pass"); bad.OK || bad.Error.Kind != "auth" {
		t.Errorf("wrong password %+v %+v", bad, bad.Error)
	}
	c, err := Connect(ro, pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.do(ctx, "POST", "/products/_doc", map[string]any{"x": 1}, nil); err == nil {
		t.Error("the read-only user could write")
	}

	t.Run("introspect", func(t *testing.T) {
		m, err := Introspect(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]int{}
		for i, r := range m.Relations {
			byName[r.Name] = i
		}
		products := m.Relations[byName["products"]]
		if *products.RowEstimate != 2 || products.Column("title.keyword") == nil || products.Column("variants").Type != "nested" ||
			products.Column("maker.name") == nil || !strings.Contains(products.Comment, "2 shards, 0 replicas") || !hasFinding(products.Findings, "DBS-ES-NO-REPLICAS", "") {
			t.Errorf("products %+v", products)
		}
		logs := m.Relations[byName["logs"]]
		if col := logs.Column("status"); col == nil || col.Type != "keyword | long" || !strings.Contains(col.Comment, "long in logs-a") ||
			!hasFinding(logs.Findings, "DBS-ES-TYPE-CONFLICT", "status") {
			t.Errorf("alias %+v", logs)
		}
		for _, r := range m.Relations {
			if strings.HasPrefix(r.Name, ".") {
				t.Errorf("system index %s listed", r.Name)
			}
		}
	})

	t.Run("run", func(t *testing.T) {
		run := func(text string, explain bool) *exec.Result {
			t.Helper()
			req, err := Parse(text, c.Pattern)
			if err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			r, err := Run(ctx, c, req, 10, explain, 10*time.Second)
			if err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			return r
		}
		r := run(`{"index": "products", "query": {"term": {"sku": "A1"}}}`, false)
		if r.RowCount != 1 || r.Columns[0].Name != "_index" || !strings.Contains(string(r.Rows[0][3]), "9007199254740993") {
			t.Errorf("search %+v %s", r.Columns, r.Rows)
		}
		r = run(`{"index": "products", "query": {"wildcard": {"sku": "*1"}}}`, true)
		if !hasFlag(r.Plan, "expensive_wildcard") || r.Plan.Children[0].Condition == "" {
			t.Errorf("explain %+v", r.Plan.Children[0])
		}
		r = run("SELECT sku, price FROM products ORDER BY price DESC", false)
		if r.RowCount != 2 || string(r.Rows[0][0]) != `"B2"` {
			t.Errorf("sql %+v %s", r.Columns, r.Rows)
		}
		if r = run("SELECT sku FROM products WHERE price > 1", true); len(r.Plan.Children) == 0 || r.Plan.Children[0].Condition == "" && len(r.Plan.Children[0].Children) == 0 {
			t.Errorf("sql explain %+v", r.Plan.Children)
		}
		_, err := Parse("DELETE FROM products", "*")
		var ge *guard.Error
		if !errors.As(err, &ge) {
			t.Errorf("delete parsed: %v", err)
		}
	})
}

func hasFinding(fs []schema.Finding, rule, column string) bool {
	for _, f := range fs {
		if f.RuleID == rule && f.Column == column {
			return true
		}
	}
	return false
}

func hasFlag(n *plan.Node, code string) bool {
	for _, f := range n.Flags {
		if f.Code == code {
			return true
		}
	}
	for _, c := range n.Children {
		if hasFlag(c, code) {
			return true
		}
	}
	return false
}

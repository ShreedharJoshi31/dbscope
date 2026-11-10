package elastic

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestBusyTable indexes 1,200 documents into one index and 5 into another on
// real clusters, then checks DBS-HOT-TABLE against what index stats counted:
// a write block, a close or a split of the busy index is rated one step above
// the finding that stops its traffic, and the quiet index or a mapping change
// gets nothing. On Elasticsearch, a user without the monitor privilege leaves
// ratings alone and says what to grant.
func TestBusyTable(t *testing.T) {
	for _, name := range []string{"es9", "os3"} {
		t.Run(name, func(t *testing.T) { testBusyTable(t, name) })
	}
}

func testBusyTable(t *testing.T, name string) {
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
	settings := map[string]any{"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0}}
	do("PUT", "/hot", settings)
	do("PUT", "/quiet", settings)
	for i := range 1200 {
		do("PUT", "/hot/_doc/"+strconv.Itoa(i), map[string]any{"status": "paid"})
	}
	for i := range 5 {
		do("PUT", "/quiet/_doc/"+strconv.Itoa(i), map[string]any{"status": "paid"})
	}
	do("POST", "/_refresh", nil)

	base, err := ReadBase(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if base.Traffic == nil || base.Traffic.Writes["hot"] != 1200 || base.Traffic.Writes["quiet"] != 5 {
		t.Fatalf("traffic: %+v, warnings %v", base.Traffic, base.Warnings)
	}
	find := func(st migrate.Statement, id string) *migrate.Finding {
		for i := range st.Findings {
			if st.Findings[i].RuleID == id {
				return &st.Findings[i]
			}
		}
		return nil
	}
	check := func(base *Base, label, req, stopper string, want score.Rating, stopped string) {
		t.Helper()
		rep := Analyze(base, []ingest.Input{{Name: "m.http", SQL: req}})
		st := rep.Statements[len(rep.Statements)-1]
		f := find(st, "DBS-HOT-TABLE")
		switch {
		case want == "" && f != nil:
			t.Errorf("%s: unexpected %+v", label, *f)
		case want == "":
		case f == nil:
			t.Errorf("%s: no DBS-HOT-TABLE in %+v", label, st.Findings)
		case f.Rating != want || st.Rating != want || !strings.Contains(f.Message, "hot has") || !strings.Contains(f.Message, stopped):
			t.Errorf("%s: %s %q, statement %s; want %s", label, f.Rating, f.Message, st.Rating, want)
		default:
			if s := find(st, stopper); s == nil || s.Rating.Up() != f.Rating {
				t.Errorf("%s: not one step above %s: %+v", label, stopper, st.Findings)
			}
		}
	}
	block := `PUT /hot/_settings
{"index.blocks.write": true}`
	check(base, "write block on the busy index", block, "DBS-ES-WRITE-BLOCK", score.Critical, "writes to it fail")
	check(base, "write block on the quiet index", strings.Replace(block, "hot", "quiet", 1), "", "", "")
	check(base, "close", "POST /hot/_close", "DBS-ES-CLOSE", score.Critical, "searches and writes on it fail")
	check(base, "split", block+"\nPOST /hot/_split/hot-split\n{\"settings\": {\"index.number_of_shards\": 2}}", "DBS-ES-RESIZE", score.Medium, "writes to it fail")
	check(base, "mapping change", `PUT /hot/_mapping
{"properties": {"note": {"type": "keyword"}}}`, "", "", "")

	if !strings.HasPrefix(name, "es") {
		return
	}
	do("POST", "/_security/role/nomon", map[string]any{"cluster": []string{"monitor"}, "indices": []map[string]any{{"names": []string{"*"}, "privileges": []string{"read", "view_index_metadata"}}}})
	do("POST", "/_security/user/nomon", map[string]any{"password": "nomon-test-pass", "roles": []string{"nomon"}})
	np := p
	np.User = "nomon"
	nc, err := Connect(np, "nomon-test-pass")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := ReadBase(ctx, nc)
	if err != nil {
		t.Fatal(err)
	}
	if nb.Traffic != nil || !slices.ContainsFunc(nb.Warnings, func(w string) bool { return strings.Contains(w, "monitor index privilege") }) {
		t.Fatalf("without monitor: traffic %+v, warnings %v", nb.Traffic, nb.Warnings)
	}
	check(nb, "write block without monitor", block, "", "", "")
}

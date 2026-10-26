package plan

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The fixtures were generated on PostgreSQL 14 and 18 by
// internal/query/exec TestWritePlanFixtures (work_mem 64kB, no parallel
// workers) against the exec test schema.

var fixtureRels = map[string]RelInfo{
	"app.orders":      {Root: "app.orders", Rows: 200000},
	"app.accounts":    {Root: "app.accounts", Rows: 1000},
	"app.events_2026": {Root: "app.events", Rows: 10000},
	"app.events_2027": {Root: "app.events", Rows: 10000},
}

func load(t *testing.T, name string) *Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p
}

func TestFixturesParse(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.json")
	if len(files) < 32 {
		t.Fatalf("only %d fixtures", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		p := load(t, name)
		if p.Analyzed != strings.HasSuffix(name, "_analyze") {
			t.Errorf("%s: analyzed %v", name, p.Analyzed)
		}
		p.Annotate(fixtureRels)
		var cost, tm float64
		p.Root.walk(func(n *Node) {
			cost += n.CostShare
			if n.TimeShare != nil {
				tm += *n.TimeShare
			}
			if n.CostShare < 0 || n.CostShare > 1 || n.Flags == nil || n.Children == nil {
				t.Errorf("%s: %s share %v flags %v", name, n.NodeType, n.CostShare, n.Flags)
			}
		})
		if math.Abs(cost-1) > 0.01 {
			t.Errorf("%s: cost shares sum to %v", name, cost)
		}
		if p.Analyzed && math.Abs(tm-1) > 0.01 {
			t.Errorf("%s: time shares sum to %v", name, tm)
		}
		if p.Root.TotalCost <= 0 || p.Root.PlanRows <= 0 {
			t.Errorf("%s: root cost %v rows %v", name, p.Root.TotalCost, p.Root.PlanRows)
		}
	}
}

func eachVersion(t *testing.T, f func(t *testing.T, v string)) {
	for _, v := range []string{"pg14", "pg18"} {
		t.Run(v, func(t *testing.T) { f(t, v) })
	}
}

func flagCodes(p *Plan) []string {
	var out []string
	p.Root.walk(func(n *Node) {
		for _, f := range n.Flags {
			out = append(out, n.NodeType+":"+f.Level+":"+f.Code)
		}
	})
	return out
}

func TestSeqScanRules(t *testing.T) {
	eachVersion(t, func(t *testing.T, v string) {
		p := load(t, v+"_seqscan_filter")
		ov := p.Annotate(fixtureRels)
		ro := ov.Relations["app.orders"]
		if ro.Level != "danger" || !slices.Equal(flagCodes(p), []string{"Seq Scan:danger:seq_scan_filter"}) {
			t.Fatalf("plan only: %+v %v", ro, flagCodes(p))
		}
		if ro.Reasons[0] != "Seq Scan reads about 200k rows to keep an estimated 1" {
			t.Errorf("reason %q", ro.Reasons[0])
		}
		// Without catalog sizes the estimate falls back to Plan Rows (1): good.
		p = load(t, v+"_seqscan_filter")
		if ov := p.Annotate(nil); ov.Relations["app.orders"].Level != "good" {
			t.Errorf("without sizes: %+v", ov.Relations["app.orders"])
		}

		p = load(t, v+"_seqscan_filter_analyze")
		ov = p.Annotate(fixtureRels)
		ro = ov.Relations["app.orders"]
		if ro.Level != "danger" || ro.Reasons[0] != "Seq Scan reads 200k rows and discards 100%" {
			t.Fatalf("analyzed: %+v %v", ro, flagCodes(p))
		}
		if ro.Scans[0].ActualRows == nil || *ro.Scans[0].ActualRows != 0 || p.Root.RowsRemovedByFilter == nil {
			t.Errorf("analyzed scan %+v", ro.Scans[0])
		}

		p = load(t, v+"_index_scan")
		ov = p.Annotate(fixtureRels)
		if ro := ov.Relations["app.orders"]; ro.Level != "good" || ro.Scans[0].NodeType != "Index Scan" || ro.Scans[0].Index != "orders_pkey" {
			t.Errorf("index scan %+v", ro)
		}
		if p.Root.Condition != "(orders.id = 5)" || p.Root.Relation != "app.orders" || p.Root.Alias != "orders" {
			t.Errorf("root %+v", p.Root)
		}
	})
}

func TestPartitionsMapToRoot(t *testing.T) {
	eachVersion(t, func(t *testing.T, v string) {
		for _, suffix := range []string{"", "_analyze"} {
			p := load(t, v+"_partitions"+suffix)
			if got := p.Relations(); len(got) != 2 || got[0].Key() != "app.events_2026" {
				t.Fatalf("relations %v", got)
			}
			ov := p.Annotate(fixtureRels)
			if len(ov.Relations) != 1 || len(ov.Relations["app.events"].Scans) != 2 {
				t.Fatalf("overlay %+v", ov.Relations)
			}
			if p.Root.NodeType != "Append" || p.Root.Children[0].Relation != "app.events_2026" {
				t.Errorf("plan node keeps the partition: %+v", p.Root.Children[0])
			}
		}
	})
}

func TestBitmapAttributedToHeap(t *testing.T) {
	eachVersion(t, func(t *testing.T, v string) {
		p := load(t, v+"_bitmap_analyze")
		ov := p.Annotate(fixtureRels)
		ro := ov.Relations["app.orders"]
		var scans []string
		for _, s := range ro.Scans {
			scans = append(scans, s.NodeType+"/"+s.Index)
		}
		if !slices.Equal(scans, []string{"Bitmap Heap Scan/", "Bitmap Index Scan/orders_acct"}) || ro.Level != "good" || len(ov.Relations) != 1 {
			t.Fatalf("bitmap scans %v level %s", scans, ro.Level)
		}
	})
}

func TestJoins(t *testing.T) {
	eachVersion(t, func(t *testing.T, v string) {
		p := load(t, v+"_nested_loop")
		ov := p.Annotate(fixtureRels)
		want := Join{From: "app.accounts", To: "app.orders", Method: "Nested Loop", JoinType: "Inner"}
		if !slices.Equal(ov.Joins, []Join{want}) {
			t.Errorf("nested loop joins %+v", ov.Joins)
		}
		p = load(t, v+"_join_analyze")
		ov = p.Annotate(fixtureRels)
		if len(ov.Joins) != 1 || ov.Joins[0].Method != "Hash Join" || ov.Joins[0].Condition == "" {
			t.Fatalf("hash join %+v", ov.Joins)
		}
		pair := []string{ov.Joins[0].From, ov.Joins[0].To}
		slices.Sort(pair)
		if !slices.Equal(pair, []string{"app.accounts", "app.orders"}) {
			t.Errorf("hash join pair %v", pair)
		}
	})
}

func TestDiskSpillAndCTE(t *testing.T) {
	eachVersion(t, func(t *testing.T, v string) {
		p := load(t, v+"_disk_sort_analyze")
		p.Annotate(fixtureRels)
		if !slices.Contains(flagCodes(p), "Sort:danger:disk_spill") {
			t.Errorf("disk sort flags %v", flagCodes(p))
		}
		p = load(t, v+"_cte_sort_limit_analyze")
		ov := p.Annotate(fixtureRels)
		var cte *Node
		p.Root.walk(func(n *Node) {
			if n.NodeType == "CTE Scan" {
				cte = n
			}
		})
		if cte == nil || cte.raw.ParentRelationship != "Outer" || cte.Relation != "" {
			t.Fatalf("CTE scan %+v", cte)
		}
		if _, ok := ov.Relations["app.orders"]; !ok || len(ov.Relations) != 1 {
			t.Errorf("cte overlay %v", ov.Relations)
		}
	})
}

// TestToleratesVersionDifferences uses a PostgreSQL 18-style plan with
// fractional Actual Rows, keys dbscope does not know and a parallel plan.
func TestToleratesVersionDifferences(t *testing.T) {
	js := `[{"Plan": {"Node Type": "Gather", "Startup Cost": 0, "Total Cost": 100, "Plan Rows": 10,
		"Actual Rows": 10.00, "Actual Loops": 1, "Actual Total Time": 50, "Workers Planned": 2, "Workers Launched": 1,
		"Disabled": false, "Some Future Key": {"x": [1]},
		"Plans": [{"Node Type": "Seq Scan", "Parent Relationship": "Outer", "Parallel Aware": true,
			"Relation Name": "t", "Schema": "public", "Alias": "t", "Startup Cost": 0, "Total Cost": 90,
			"Plan Rows": 1000, "Actual Rows": 0.5, "Actual Loops": 2, "Actual Total Time": 45,
			"Rows Removed by Filter": 50000, "Filter": "(x = 1)", "Index Searches": 0}]}, "Execution Time": 51}]`
	p, err := Parse([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	ov := p.Annotate(nil)
	scan := p.Root.Children[0]
	if *scan.ActualRows != 0.5 || *p.Root.TimeShare != 0.1 || *scan.TimeShare != 0.9 {
		t.Fatalf("scan rows %v shares %v / %v", *scan.ActualRows, *p.Root.TimeShare, *scan.TimeShare)
	}
	codes := flagCodes(p)
	for _, want := range []string{"Gather:info:workers_shortfall", "Seq Scan:danger:misestimate", "Seq Scan:danger:rows_removed"} {
		if !slices.Contains(codes, want) {
			t.Errorf("flags %v, missing %s", codes, want)
		}
	}
	if ov.Relations["public.t"].Level != "danger" {
		t.Errorf("overlay %+v", ov.Relations["public.t"])
	}
	for _, bad := range []string{`{}`, `[]`, `[{"Plan": null}]`, `not json`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestHelpers(t *testing.T) {
	for f, want := range map[float64]string{0: "0", 950: "950", 12345: "12.3k", 2_100_000: "2.1M", 4e9: "4B", 1000: "1k"} {
		if got := human(f); got != want {
			t.Errorf("human(%v) = %s, want %s", f, got, want)
		}
	}
	for _, c := range []struct {
		text, alias string
		want        bool
	}{
		{"(o.acct_id = a.id)", "o", true},
		{"(o.acct_id = a.id)", "a", true},
		{"(foo.acct_id = 1)", "o", false},
		{`("Order".id = 1)`, "Order", true},
		{"(x.o = 1)", "o", false},
	} {
		if got := mentions(c.text, c.alias); got != c.want {
			t.Errorf("mentions(%q, %q) = %v", c.text, c.alias, got)
		}
	}
	if f, under := estimateFactor(10, 0); f != 10 || under {
		t.Errorf("estimateFactor(10, 0) = %v %v", f, under)
	}
	if f, under := estimateFactor(1, 500); f != 500 || !under {
		t.Errorf("estimateFactor(1, 500) = %v %v", f, under)
	}
}

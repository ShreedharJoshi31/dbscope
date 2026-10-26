package plan

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RelInfo is what the catalog says about a scanned relation.
type RelInfo struct {
	// Root is the "schema.name" of the partition root, or the relation itself.
	Root string
	// Rows is pg_class.reltuples, or a negative number when unknown.
	Rows float64
}

// Overlay summarises the plan per relation for the ERD.
type Overlay struct {
	Relations map[string]*RelOverlay `json:"relations"`
	Joins     []Join                 `json:"joins"`
}

type RelOverlay struct {
	Level   string   `json:"level"` // good | warn | danger
	Scans   []Scan   `json:"scans"`
	Reasons []string `json:"reasons"`
}

type Scan struct {
	NodeType   string   `json:"nodeType"`
	Index      string   `json:"index,omitempty"`
	PlanRows   float64  `json:"planRows"`
	ActualRows *float64 `json:"actualRows,omitempty"`
}

type Join struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Method    string `json:"method"`
	JoinType  string `json:"joinType"`
	Condition string `json:"condition,omitempty"`
}

// Thresholds, after pev2 and pganalyze (docs/research-query-engine.md §5).
const (
	minShare        = 0.10 // only nodes with at least this exclusive share are flagged
	planDangerShare = 0.30 // plan-only: a filtered seq scan this expensive is danger
	planDangerRows  = 100_000
	largeScanRows   = 10_000
	misestimateWarn = 10
	misestimateBad  = 100
	removedFraction = 0.90
	removedMinRows  = 10_000
	scanSuffix      = " Scan"
)

// Annotate computes shares and flags on every node and returns the
// overlay. rels maps scanned relations (as in Relations) to catalog facts;
// missing entries are treated as non-partitions with unknown size.
func (p *Plan) Annotate(rels map[string]RelInfo) Overlay {
	p.shares()
	p.Root.walk(func(n *Node) { p.flag(n, rels) })
	return p.overlay(rels)
}

func (p *Plan) flag(n *Node, rels map[string]RelInfo) {
	share := n.CostShare
	if p.Analyzed && n.TimeShare != nil {
		share = *n.TimeShare
	}
	if share < minShare {
		return
	}
	r := n.raw
	add := func(level, code, format string, args ...any) {
		n.Flags = append(n.Flags, Flag{Level: level, Code: code, Message: fmt.Sprintf(format, args...)})
	}
	if !p.Analyzed {
		if n.NodeType != "Seq Scan" {
			return
		}
		read := n.PlanRows
		if info, ok := rels[n.Relation]; ok && info.Rows >= 0 {
			read = info.Rows
		}
		switch {
		case r.Filter != "" && read > planDangerRows && n.CostShare >= planDangerShare:
			add("danger", "seq_scan_filter", "Seq Scan reads about %s rows to keep an estimated %s", human(read), human(n.PlanRows))
		case read > largeScanRows:
			add("warn", "seq_scan_large", "Seq Scan reads about %s rows", human(read))
		}
		return
	}

	loops := deref(n.ActualLoops)
	if loops == 0 {
		return // never executed
	}
	actual := deref(n.ActualRows)
	if f, under := estimateFactor(n.PlanRows, actual); f >= misestimateWarn {
		level := "warn"
		if f >= misestimateBad {
			level = "danger"
		}
		dir := "over"
		if under {
			dir = "under"
		}
		add(level, "misestimate", "Estimated %s rows per loop but got %s (%sx %sestimate)", human(n.PlanRows), human(actual), human(f), dir)
	}
	removed := deref(r.RowsRemovedFilter) + deref(r.RowsRemovedJoin)
	read := (actual + removed) * loops
	if removed*loops > removedMinRows && read > 0 && removed*loops/read >= removedFraction {
		add("danger", "rows_removed", "%s reads %s rows and discards %s%%", n.NodeType, human(read), pct(removed*loops/read))
	} else if n.NodeType == "Seq Scan" && read > largeScanRows {
		add("warn", "seq_scan_large", "Seq Scan reads %s rows", human(read))
	}
	switch {
	case r.SortSpaceType == "Disk":
		add("danger", "disk_spill", "Sort spilled to disk; work_mem was too small")
	case r.HashBatches > 1:
		add("danger", "disk_spill", "Hash used %s batches, spilling to disk", human(r.HashBatches))
	case r.HashAggBatches > 1 || r.DiskUsage > 0:
		add("danger", "disk_spill", "Hash aggregate spilled to disk")
	case r.TempWrittenBlocks > 0:
		add("danger", "disk_spill", "Wrote %s temporary blocks to disk", human(r.TempWrittenBlocks))
	}
	if r.LossyHeapBlocks > 0 {
		add("warn", "lossy_bitmap", "Bitmap became lossy on %s heap blocks, so rows were rechecked", human(r.LossyHeapBlocks))
	}
	if r.WorkersPlanned != nil && r.WorkersLaunched != nil && *r.WorkersLaunched < *r.WorkersPlanned {
		add("info", "workers_shortfall", "Launched %s of %s planned parallel workers", human(*r.WorkersLaunched), human(*r.WorkersPlanned))
	}
}

// estimateFactor compares per-loop rows like pev2, flooring both at 1.
func estimateFactor(planned, actual float64) (factor float64, under bool) {
	p, a := max(planned, 1), max(actual, 1)
	if a > p {
		return a / p, true
	}
	return p / a, false
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func isScan(n *Node) bool {
	return strings.HasSuffix(n.NodeType, scanSuffix) && (n.Relation != "" || n.NodeType == "Bitmap Index Scan")
}

func rootOf(rel string, rels map[string]RelInfo) string {
	if info, ok := rels[rel]; ok && info.Root != "" {
		return info.Root
	}
	return rel
}

var levelRank = map[string]int{"good": 0, "warn": 1, "danger": 2}

func (p *Plan) overlay(rels map[string]RelInfo) Overlay {
	ov := Overlay{Relations: map[string]*RelOverlay{}, Joins: []Join{}}
	var visit func(n *Node, heap string)
	visit = func(n *Node, heap string) {
		rel := n.Relation
		if n.NodeType == "Bitmap Index Scan" {
			rel = heap // attributed to the Bitmap Heap Scan above it
		}
		if isScan(n) && rel != "" {
			key := rootOf(rel, rels)
			ro := ov.Relations[key]
			if ro == nil {
				ro = &RelOverlay{Level: "good", Scans: []Scan{}, Reasons: []string{}}
				ov.Relations[key] = ro
			}
			ro.Scans = append(ro.Scans, Scan{NodeType: n.NodeType, Index: n.Index, PlanRows: n.PlanRows, ActualRows: n.ActualRows})
			for _, f := range n.Flags {
				if f.Level == "info" {
					continue
				}
				if levelRank[f.Level] > levelRank[ro.Level] {
					ro.Level = f.Level
				}
				if !slices.Contains(ro.Reasons, f.Message) {
					ro.Reasons = append(ro.Reasons, f.Message)
				}
			}
		}
		if n.NodeType == "Bitmap Heap Scan" {
			heap = n.Relation
		} else if n.NodeType != "BitmapAnd" && n.NodeType != "BitmapOr" && n.NodeType != "Bitmap Index Scan" {
			heap = ""
		}
		if j, ok := joinsAt(n, rels); ok {
			for _, x := range j {
				if !slices.Contains(ov.Joins, x) {
					ov.Joins = append(ov.Joins, x)
				}
			}
		}
		for _, c := range n.Children {
			visit(c, heap)
		}
	}
	visit(p.Root, "")
	return ov
}

type aliased struct{ rel, alias string }

// sideRelations lists the relations scanned under n, not entering
// subplans, with their aliases.
func sideRelations(n *Node) []aliased {
	var out []aliased
	var visit func(*Node)
	visit = func(n *Node) {
		if n.Relation != "" && isScan(n) {
			out = append(out, aliased{n.Relation, n.Alias})
		}
		for _, c := range n.Children {
			if !c.isSubplan() {
				visit(c)
			}
		}
	}
	visit(n)
	return out
}

// joinConds collects the condition text under the inner side of a nested
// loop, where a parameterised index scan carries the join condition.
func joinConds(n *Node) string {
	var b strings.Builder
	n.walk(func(n *Node) {
		for _, c := range []string{n.raw.IndexCond, n.raw.RecheckCond, n.raw.Filter, n.raw.JoinFilter, n.raw.HashCond, n.raw.MergeCond} {
			b.WriteString(c)
			b.WriteByte(' ')
		}
	})
	return b.String()
}

// joinsAt returns the relation pairs a join node connects. When the
// condition names aliases, only the relations it mentions are paired;
// otherwise every relation on one side is paired with every one on the other.
func joinsAt(n *Node, rels map[string]RelInfo) ([]Join, bool) {
	switch n.NodeType {
	case "Hash Join", "Merge Join", "Nested Loop":
	default:
		return nil, false
	}
	var inputs []*Node
	for _, c := range n.Children {
		if !c.isSubplan() {
			inputs = append(inputs, c)
		}
	}
	if len(inputs) != 2 {
		return nil, false
	}
	outer, inner := sideRelations(inputs[0]), sideRelations(inputs[1])
	conds := n.raw.HashCond + " " + n.raw.MergeCond + " " + n.raw.JoinFilter
	if n.NodeType == "Nested Loop" {
		conds += " " + joinConds(inputs[1])
	}
	pick := func(side []aliased) []string {
		var named, all []string
		for _, a := range side {
			key := rootOf(a.rel, rels)
			all = append(all, key)
			if a.alias != "" && mentions(conds, a.alias) {
				named = append(named, key)
			}
		}
		if len(named) > 0 {
			return named
		}
		return all
	}
	var out []Join
	for _, from := range pick(outer) {
		for _, to := range pick(inner) {
			j := Join{From: from, To: to, Method: n.NodeType, JoinType: n.JoinType, Condition: n.Condition}
			if !slices.Contains(out, j) {
				out = append(out, j)
			}
		}
	}
	return out, true
}

// mentions reports whether text qualifies a column with alias, as in
// "o.id" or "\"Order\".id".
func mentions(text, alias string) bool {
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_$."])(` + regexp.QuoteMeta(alias) + `|"` +
		regexp.QuoteMeta(strings.ReplaceAll(alias, `"`, `""`)) + `")\.`)
	return re.MatchString(text)
}

// human formats a count: 950, 12.3k, 2.1M, 4B.
func human(f float64) string {
	switch {
	case f >= 1e9:
		return trim(f/1e9) + "B"
	case f >= 1e6:
		return trim(f/1e6) + "M"
	case f >= 1e3:
		return trim(f/1e3) + "k"
	default:
		return trim(f)
	}
}

func trim(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

func pct(f float64) string {
	p := f * 100
	if p > 99 && p < 100 {
		return strconv.FormatFloat(p, 'f', 1, 64)
	}
	return strconv.FormatFloat(p, 'f', 0, 64)
}

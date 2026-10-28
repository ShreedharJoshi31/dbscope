// Package plan reads EXPLAIN (FORMAT JSON) output from PostgreSQL 14-18
// into the API's PlanNode tree, computes each node's exclusive share of the
// cost (and time, with ANALYZE), flags likely problems and summarises the
// plan per relation for the ERD overlay.
package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// Flag is a finding on one plan node.
type Flag struct {
	Level   string `json:"level"` // info | warn | danger
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Node is one plan node in the API's shape.
type Node struct {
	NodeType            string   `json:"nodeType"`
	Relation            string   `json:"relation,omitempty"` // "schema.name" as scanned (a partition stays a partition)
	Alias               string   `json:"alias,omitempty"`
	Index               string   `json:"index,omitempty"`
	JoinType            string   `json:"joinType,omitempty"`
	Condition           string   `json:"condition,omitempty"`
	StartupCost         float64  `json:"startupCost"`
	TotalCost           float64  `json:"totalCost"`
	PlanRows            float64  `json:"planRows"`
	ActualRows          *float64 `json:"actualRows,omitempty"` // per loop, as EXPLAIN prints it
	ActualLoops         *float64 `json:"actualLoops,omitempty"`
	ActualTotalMs       *float64 `json:"actualTotalMs,omitempty"` // per loop
	RowsRemovedByFilter *float64 `json:"rowsRemovedByFilter,omitempty"`
	CostShare           float64  `json:"costShare"`
	TimeShare           *float64 `json:"timeShare,omitempty"`
	Flags               []Flag   `json:"flags"`
	Children            []*Node  `json:"children"`
	// StepsOnly marks the root of a plan that names steps with no estimates
	// (SQLite's EXPLAIN QUERY PLAN, MySQL's tabular EXPLAIN); dbscope checks none of it.
	StepsOnly bool `json:"stepsOnly,omitempty"`

	raw *rawNode
}

// rawNode holds the EXPLAIN keys dbscope reads. Unknown keys are ignored,
// so keys added in later versions do not break parsing.
type rawNode struct {
	NodeType           string    `json:"Node Type"`
	ParentRelationship string    `json:"Parent Relationship"`
	RelationName       string    `json:"Relation Name"`
	Schema             string    `json:"Schema"`
	Alias              string    `json:"Alias"`
	IndexName          string    `json:"Index Name"`
	JoinType           string    `json:"Join Type"`
	HashCond           string    `json:"Hash Cond"`
	MergeCond          string    `json:"Merge Cond"`
	JoinFilter         string    `json:"Join Filter"`
	IndexCond          string    `json:"Index Cond"`
	RecheckCond        string    `json:"Recheck Cond"`
	Filter             string    `json:"Filter"`
	StartupCost        float64   `json:"Startup Cost"`
	TotalCost          float64   `json:"Total Cost"`
	PlanRows           float64   `json:"Plan Rows"`
	ActualRows         *float64  `json:"Actual Rows"`
	ActualLoops        *float64  `json:"Actual Loops"`
	ActualTotalTime    *float64  `json:"Actual Total Time"`
	RowsRemovedFilter  *float64  `json:"Rows Removed by Filter"`
	RowsRemovedJoin    *float64  `json:"Rows Removed by Join Filter"`
	WorkersPlanned     *float64  `json:"Workers Planned"`
	WorkersLaunched    *float64  `json:"Workers Launched"`
	SortSpaceType      string    `json:"Sort Space Type"`
	HashBatches        float64   `json:"Hash Batches"`
	HashAggBatches     float64   `json:"HashAgg Batches"`
	DiskUsage          float64   `json:"Disk Usage"`
	TempWrittenBlocks  float64   `json:"Temp Written Blocks"`
	LossyHeapBlocks    float64   `json:"Lossy Heap Blocks"`
	Plans              []rawNode `json:"Plans"`
}

// Plan is a parsed EXPLAIN result.
type Plan struct {
	Root *Node
	// Analyzed is true for EXPLAIN ANALYZE output.
	Analyzed bool
}

// Parse reads the JSON EXPLAIN prints: an array holding one object with a
// "Plan" key.
func Parse(data []byte) (*Plan, error) {
	var out []struct {
		Plan *rawNode `json:"Plan"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse EXPLAIN output: %w", err)
	}
	if len(out) != 1 || out[0].Plan == nil {
		return nil, errors.New("parse EXPLAIN output: expected one plan")
	}
	root := convert(out[0].Plan)
	return &Plan{Root: root, Analyzed: root.ActualLoops != nil}, nil
}

func convert(r *rawNode) *Node {
	n := &Node{
		NodeType: r.NodeType, Alias: r.Alias, Index: r.IndexName, JoinType: r.JoinType,
		StartupCost: r.StartupCost, TotalCost: r.TotalCost, PlanRows: r.PlanRows,
		ActualRows: r.ActualRows, ActualLoops: r.ActualLoops, ActualTotalMs: r.ActualTotalTime,
		RowsRemovedByFilter: r.RowsRemovedFilter,
		Flags:               []Flag{}, Children: []*Node{}, raw: r,
	}
	if r.RelationName != "" {
		n.Relation = qualify(r.Schema, r.RelationName)
	}
	for _, c := range []string{r.HashCond, r.MergeCond, r.JoinFilter, r.IndexCond, r.Filter} {
		if c != "" {
			n.Condition = c
			break
		}
	}
	for i := range r.Plans {
		n.Children = append(n.Children, convert(&r.Plans[i]))
	}
	return n
}

func qualify(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// RelName is a scanned relation as EXPLAIN (VERBOSE) names it.
type RelName struct{ Schema, Name string }

// Key is the relation's "schema.name", as in Node.Relation.
func (r RelName) Key() string { return qualify(r.Schema, r.Name) }

// Relations lists the distinct relations the plan scans.
func (p *Plan) Relations() []RelName {
	seen := map[string]bool{}
	var out []RelName
	p.Root.walk(func(n *Node) {
		if n.Relation != "" && !seen[n.Relation] {
			seen[n.Relation] = true
			out = append(out, RelName{Schema: n.raw.Schema, Name: n.raw.RelationName})
		}
	})
	return out
}

func (n *Node) walk(f func(*Node)) {
	f(n)
	for _, c := range n.Children {
		c.walk(f)
	}
}

// isSubplan reports whether the node is an InitPlan or SubPlan rather than
// an input of its parent.
func (n *Node) isSubplan() bool {
	r := n.raw.ParentRelationship
	return r == "InitPlan" || r == "SubPlan"
}

// shares sets CostShare and, for analyzed plans, TimeShare. A node's
// exclusive cost is its total cost minus its children's; the shares are
// normalised by the sum over all nodes, so they add up to 1 even under a
// Limit (whose total cost is below its input's).
func (p *Plan) shares() {
	var costSum, timeSum float64
	exclCost := map[*Node]float64{}
	exclTime := map[*Node]float64{}
	var visit func(n *Node, div float64)
	visit = func(n *Node, div float64) {
		c := n.TotalCost
		t := inclusiveTime(n, div)
		childDiv := div
		if n.NodeType == "Gather" || n.NodeType == "Gather Merge" {
			if w := n.raw.WorkersLaunched; w != nil {
				childDiv = *w + 1
			} else if w := n.raw.WorkersPlanned; w != nil {
				childDiv = *w + 1
			}
		}
		for _, ch := range n.Children {
			c -= ch.TotalCost
			cd := childDiv
			if ch.isSubplan() {
				cd = div
			}
			t -= inclusiveTime(ch, cd)
			visit(ch, cd)
		}
		exclCost[n] = math.Max(c, 0)
		exclTime[n] = math.Max(t, 0)
		costSum += exclCost[n]
		timeSum += exclTime[n]
	}
	visit(p.Root, 1)
	p.Root.walk(func(n *Node) {
		if costSum > 0 {
			n.CostShare = round4(exclCost[n] / costSum)
		}
		if p.Analyzed {
			s := 0.0
			if timeSum > 0 {
				s = round4(exclTime[n] / timeSum)
			}
			n.TimeShare = &s
		}
	})
}

// inclusiveTime is the node's time over all loops. Under a Gather the
// loops ran in parallel, so the total is divided by the process count.
func inclusiveTime(n *Node, div float64) float64 {
	if n.ActualTotalMs == nil || n.ActualLoops == nil {
		return 0
	}
	if div < 1 {
		div = 1
	}
	return *n.ActualTotalMs * *n.ActualLoops / div
}

func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

// SetCostShares sets each node's share of the plan's own (exclusive) cost,
// for engines whose plans carry a cumulative cost per node and no timing.
// The root is a statement node whose cost repeats its child's, so it is skipped.
func SetCostShares(root *Node) {
	excl := map[*Node]float64{}
	var sum float64
	var visit func(n *Node)
	visit = func(n *Node) {
		c := n.TotalCost
		for _, ch := range n.Children {
			c -= ch.TotalCost
			visit(ch)
		}
		excl[n] = math.Max(c, 0)
		sum += excl[n]
	}
	for _, ch := range root.Children {
		visit(ch)
	}
	if sum == 0 {
		return
	}
	for n, c := range excl {
		n.CostShare = round4(c / sum)
	}
}

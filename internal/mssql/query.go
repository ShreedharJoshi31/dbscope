package mssql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// readTypes are the statement types the plan gate lets run. SQL Server has
// no read-only transaction, so the compiled statement type is the barrier:
// it sees statements Check cannot, such as a DELETE after a SELECT with no
// semicolon between them.
var readTypes = map[string]bool{"SELECT": true, "SELECT WITHOUT QUERY": true, "JSON SELECT": true, "XML SELECT": true}

// Run compiles the statement under SHOWPLAN_XML (which never executes it),
// refuses it unless every statement it compiles to is a SELECT, then runs it
// in a transaction that is always rolled back, with the row cap on the
// server (SET ROWCOUNT) and lock waits bounded by timeout.
func Run(ctx context.Context, db *sql.DB, sqlText string, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	sqlText = strings.TrimRight(strings.TrimSpace(sqlText), "; \t\n")
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer c.Close()

	stmts, err := showplan(ctx, c, sqlText)
	if err != nil {
		return nil, err
	}
	if len(stmts) != 1 {
		return nil, &guard.Error{Reason: guard.MultipleStatements, Message: fmt.Sprintf("This text compiles to %d statements; run one at a time", len(stmts))}
	}
	if t := stmts[0].attr("StatementType"); !readTypes[t] {
		return nil, &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only read-only queries run here; the server compiled this as %s", t)}
	}
	res := &exec.Result{ExecutedSQL: sqlText, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	res.Plan = queryPlan(stmts[0])
	if explainOnly {
		return res, nil
	}

	settings := fmt.Sprintf("SET ROWCOUNT %d; SET LOCK_TIMEOUT %d", maxRows+1, timeout.Milliseconds())
	if _, err := c.ExecContext(ctx, settings); err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := c.ExecContext(rctx, "SET ROWCOUNT 0; SET LOCK_TIMEOUT -1"); err != nil {
			discard(c)
		}
	}()
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer tx.Rollback()

	start := time.Now()
	rows, err := tx.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = strings.ToLower(t.DatabaseTypeName())
		res.Columns = append(res.Columns, exec.Column{Name: t.Name(), Type: names[i]})
	}
	vals := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Rows) == maxRows {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]json.RawMessage, len(vals))
		for i, v := range vals {
			row[i] = value(names[i], v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

// value renders one driver value as the browser expects every engine's
// values: integers beyond 2^53 and decimals as strings, bytes as \x hex.
func value(typ string, v any) json.RawMessage {
	switch x := v.(type) {
	case nil:
		return json.RawMessage("null")
	case int64:
		return exec.Value(20, []byte(strconv.FormatInt(x, 10)))
	case float64:
		return exec.Value(701, []byte(strconv.FormatFloat(x, 'g', -1, 64)))
	case float32:
		return exec.Value(701, []byte(strconv.FormatFloat(float64(x), 'g', -1, 32)))
	case bool:
		return exec.Value(16, []byte(map[bool]string{true: "t", false: "f"}[x]))
	case time.Time:
		var s string
		switch typ {
		case "date":
			s = x.Format("2006-01-02")
		case "time":
			s = x.Format("15:04:05.9999999")
		case "datetimeoffset":
			s = x.Format("2006-01-02T15:04:05.9999999Z07:00")
		default: // datetime, datetime2 and smalldatetime carry no zone
			s = x.Format("2006-01-02T15:04:05.9999999")
		}
		b, _ := json.Marshal(s)
		return b
	case []byte:
		switch typ {
		case "decimal", "numeric", "money", "smallmoney":
			b, _ := json.Marshal(string(x))
			return b
		case "uniqueidentifier":
			var u mssql.UniqueIdentifier
			if u.Scan(x) == nil {
				b, _ := json.Marshal(u.String())
				return b
			}
		}
		b, _ := json.Marshal(`\x` + hex.EncodeToString(x))
		return b
	case string:
		b, _ := json.Marshal(x)
		return b
	}
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(fmt.Sprint(v))
	}
	return b
}

// showplan returns the StmtSimple elements of the statement's estimated
// plan. SHOWPLAN_XML is a session setting, so a connection that could not
// turn it off again is closed rather than returned to the pool.
func showplan(ctx context.Context, c *sql.Conn, sqlText string) ([]*xnode, error) {
	if _, err := c.ExecContext(ctx, "SET SHOWPLAN_XML ON"); err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := c.ExecContext(rctx, "SET SHOWPLAN_XML OFF"); err != nil {
			discard(c)
		}
	}()
	rows, err := c.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stmts []*xnode
	for {
		for rows.Next() {
			var doc string
			if err := rows.Scan(&doc); err != nil {
				return nil, err
			}
			var root xnode
			if err := xml.Unmarshal([]byte(doc), &root); err != nil {
				return nil, fmt.Errorf("reading the plan: %w", err)
			}
			root.each("StmtSimple", func(n *xnode) bool { stmts = append(stmts, n); return false })
			// Statements other than StmtSimple (StmtCond, StmtCursor, ...) are never reads.
			root.each("StmtCond", func(n *xnode) bool { stmts = append(stmts, n); return false })
			root.each("StmtCursor", func(n *xnode) bool { stmts = append(stmts, n); return false })
			root.each("StmtUseDb", func(n *xnode) bool { stmts = append(stmts, n); return false })
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return stmts, rows.Err()
}

// xnode is any showplan element.
type xnode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Nodes   []*xnode   `xml:",any"`
}

func (n *xnode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) num(name string) float64 {
	f, _ := strconv.ParseFloat(n.attr(name), 64)
	return f
}

// each calls f on every descendant element named name; f returning false
// stops the descent below that element.
func (n *xnode) each(name string, f func(*xnode) bool) {
	for _, c := range n.Nodes {
		if c.XMLName.Local == name {
			if !f(c) {
				continue
			}
		}
		c.each(name, f)
	}
}

// child returns the first direct child named name.
func (n *xnode) child(name string) *xnode {
	for _, c := range n.Nodes {
		if c.XMLName.Local == name {
			return c
		}
	}
	return nil
}

// queryPlan turns a statement's estimated plan into dbscope's plan tree: one
// node per RelOp with its estimated rows and subtree cost.
func queryPlan(stmt *xnode) *plan.Node {
	root := &plan.Node{NodeType: stmt.attr("StatementType"), TotalCost: stmt.num("StatementSubTreeCost"),
		PlanRows: stmt.num("StatementEstRows"), Flags: []plan.Flag{}, Children: []*plan.Node{}}
	qp := stmt.child("QueryPlan")
	if qp == nil {
		return root
	}
	qp.each("MissingIndex", func(mi *xnode) bool {
		var eq, other []string
		for _, g := range mi.Nodes {
			for _, col := range g.Nodes {
				name := strings.Trim(col.attr("Name"), "[]")
				if g.attr("Usage") == "EQUALITY" {
					eq = append(eq, name)
				} else {
					other = append(other, name)
				}
			}
		}
		root.Flags = append(root.Flags, plan.Flag{Level: "warn", Code: "missing_index", Message: fmt.Sprintf(
			"The optimizer suggests an index on %s.%s (%s)", strings.Trim(mi.attr("Schema"), "[]"), strings.Trim(mi.attr("Table"), "[]"),
			strings.Join(append(eq, other...), ", "))})
		return false
	})
	if w := qp.child("Warnings"); w != nil {
		for _, c := range w.Nodes {
			if c.XMLName.Local == "PlanAffectingConvert" {
				root.Flags = append(root.Flags, plan.Flag{Level: "warn", Code: "implicit_conversion", Message: fmt.Sprintf(
					"An implicit conversion (%s) may stop an index seek: %s", c.attr("ConvertIssue"), c.attr("Expression"))})
			}
		}
	}
	for _, op := range relOps(qp) {
		root.Children = append(root.Children, relOp(op))
	}
	shares(root)
	return root
}

// relOps returns the RelOp elements below n that are not below another RelOp.
func relOps(n *xnode) []*xnode {
	var out []*xnode
	n.each("RelOp", func(r *xnode) bool { out = append(out, r); return false })
	return out
}

// scanOps read a whole table or index.
var scanOps = map[string]bool{"Table Scan": true, "Clustered Index Scan": true, "Index Scan": true, "Columnstore Index Scan": true}

func relOp(r *xnode) *plan.Node {
	n := &plan.Node{NodeType: r.attr("PhysicalOp"), PlanRows: r.num("EstimateRows"), TotalCost: r.num("EstimatedTotalSubtreeCost"),
		Flags: []plan.Flag{}, Children: []*plan.Node{}}
	if lo := r.attr("LogicalOp"); lo != "" && lo != n.NodeType && strings.Contains(lo, "Join") {
		n.JoinType = lo
	}
	for _, op := range r.Nodes {
		switch op.XMLName.Local {
		case "OutputList", "Warnings", "MemoryFractions", "RunTimeInformation", "RunTimePartitionSummary", "InternalInfo":
			if op.XMLName.Local == "Warnings" && op.attr("NoJoinPredicate") == "true" {
				n.Flags = append(n.Flags, plan.Flag{Level: "danger", Code: "no_join_predicate",
					Message: "This join has no join predicate: every row meets every row"})
			}
			continue
		}
		if obj := op.child("Object"); obj != nil {
			n.Relation = strings.Trim(obj.attr("Schema"), "[]") + "." + strings.Trim(obj.attr("Table"), "[]")
			n.Index = strings.Trim(obj.attr("Index"), "[]")
			n.Alias = strings.Trim(obj.attr("Alias"), "[]")
		}
		if p := op.child("Predicate"); p != nil {
			if s := p.child("ScalarOperator"); s != nil {
				n.Condition = s.attr("ScalarString")
			}
		}
		for _, c := range relOps(op) {
			n.Children = append(n.Children, relOp(c))
		}
	}
	switch {
	case scanOps[n.NodeType] && n.Condition != "":
		n.Flags = append(n.Flags, plan.Flag{Level: "warn", Code: "full_scan",
			Message: fmt.Sprintf("Reads all of %s to filter it; an index on the filtered columns would let the server seek", n.Relation)})
	case n.NodeType == "Key Lookup" || n.NodeType == "RID Lookup":
		n.Flags = append(n.Flags, plan.Flag{Level: "info", Code: "lookup",
			Message: "Fetches each row from the table after an index seek; including the columns in the index avoids it"})
	}
	return n
}

// shares sets each node's share of the plan's own (exclusive) cost.
func shares(root *plan.Node) {
	excl := map[*plan.Node]float64{}
	var sum float64
	var visit func(n *plan.Node)
	visit = func(n *plan.Node) {
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
		n.CostShare = math.Round(c/sum*1e4) / 1e4
	}
}

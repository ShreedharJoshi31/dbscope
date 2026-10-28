package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// readCommands are the commands a query may be.
var readCommands = map[string]bool{"find": true, "aggregate": true, "count": true, "distinct": true}

// forbidden are keys that write, run server-side JavaScript, or write with
// relaxed rules; checked at any depth, since $lookup, $facet and $unionWith
// nest pipelines.
var forbidden = map[string]string{
	"$out": "writes a collection", "$merge": "writes a collection",
	"$where": "runs JavaScript on the server", "$function": "runs JavaScript on the server", "$accumulator": "runs JavaScript on the server",
	"mapReduce": "runs JavaScript on the server", "bypassDocumentValidation": "only matters when writing", "writeConcern": "only matters when writing",
}

// Parse reads a query: a command document in MongoDB Extended JSON, e.g.
// {"find": "orders", "filter": {"status": "new"}, "limit": 10}.
func Parse(text string) (bson.D, error) {
	var cmd bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &cmd); err != nil {
		return nil, &guard.Error{Reason: guard.Syntax, Message: "Write the query as a MongoDB command in Extended JSON, e.g. {\"find\": \"orders\", \"filter\": {\"status\": \"new\"}}: " + err.Error()}
	}
	return cmd, nil
}

// Check accepts a find, aggregate, count or distinct command with nothing
// that writes or runs JavaScript. A user with only the read role is what
// keeps the server unchanged; Check explains refusals early.
func Check(cmd bson.D) error {
	if len(cmd) == 0 || !readCommands[cmd[0].Key] {
		name := ""
		if len(cmd) > 0 {
			name = cmd[0].Key
		}
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only find, aggregate, count and distinct run here; %q is not one of them", name)}
	}
	if _, ok := cmd[0].Value.(string); !ok {
		return &guard.Error{Reason: guard.Syntax, Message: fmt.Sprintf("%s needs a collection name as its value", cmd[0].Key)}
	}
	var bad string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case bson.D:
			for _, e := range x {
				if why, ok := forbidden[e.Key]; ok && bad == "" {
					bad = fmt.Sprintf("%s %s and is not allowed", e.Key, why)
				}
				walk(e.Value)
			}
		case bson.A:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(cmd)
	if bad != "" {
		reason := guard.WritesData
		if strings.Contains(bad, "JavaScript") {
			reason = guard.FunctionNotAllowed
		}
		return &guard.Error{Reason: reason, Message: bad}
	}
	return nil
}

func set(cmd bson.D, key string, v any) bson.D {
	for i, e := range cmd {
		if e.Key == key {
			cmd[i].Value = v
			return cmd
		}
	}
	return append(cmd, bson.E{Key: key, Value: v})
}

func get(cmd bson.D, key string) (any, bool) {
	for _, e := range cmd {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// Run executes a command that passed Check, with at most maxRows documents
// returned. The context deadline makes the driver send maxTimeMS (verified:
// adding it by hand is a duplicate field), so the server stops the command;
// if the client times out first it disconnects, and the server kills a find
// or aggregate whose client is gone. IsTimeout covers both.
func Run(ctx context.Context, d *mongo.Database, cmd bson.D, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd = set(cmd, "comment", "dbscope")
	text, _ := bson.MarshalExtJSON(cmd, false, false)
	res := &exec.Result{ExecutedSQL: string(text), Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	if p, err := explain(ctx, d, cmd); err == nil {
		res.Plan = p
	} else if explainOnly {
		return nil, err
	}
	if explainOnly {
		return res, nil
	}
	start := time.Now()
	var docs []bson.Raw
	switch cmd[0].Key {
	case "find":
		limit := int64(maxRows + 1)
		if v, ok := get(cmd, "limit"); ok {
			if n, ok := asInt(v); ok && n > 0 && n < limit {
				limit = n
			}
		}
		cmd = set(cmd, "limit", limit)
		cmd = set(cmd, "batchSize", limit)
		cur, err := d.RunCommandCursor(ctx, cmd)
		if err != nil {
			return nil, err
		}
		docs, err = drain(ctx, cur, maxRows+1)
		if err != nil {
			return nil, err
		}
	case "aggregate":
		pipe, _ := get(cmd, "pipeline")
		stages, _ := pipe.(bson.A)
		stages = append(stages, bson.D{{Key: "$limit", Value: int64(maxRows + 1)}})
		cmd = set(cmd, "pipeline", stages)
		cmd = set(cmd, "allowDiskUse", false)
		if _, ok := get(cmd, "cursor"); !ok {
			cmd = set(cmd, "cursor", bson.D{})
		}
		cur, err := d.RunCommandCursor(ctx, cmd)
		if err != nil {
			return nil, err
		}
		docs, err = drain(ctx, cur, maxRows+1)
		if err != nil {
			return nil, err
		}
	default: // count and distinct answer with one document
		r, err := d.RunCommand(ctx, cmd).Raw()
		if err != nil {
			return nil, err
		}
		docs = []bson.Raw{r}
	}
	if len(docs) > maxRows {
		docs, res.Truncated = docs[:maxRows], true
	}
	fill(res, docs)
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

// IsTimeout reports a command stopped by its timeout, by the server or the client.
func IsTimeout(err error) bool {
	var ce mongo.CommandError
	return mongo.IsTimeout(err) || (errors.As(err, &ce) && ce.Code == 50) || errors.Is(err, context.DeadlineExceeded)
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	}
	return 0, false
}

func drain(ctx context.Context, cur *mongo.Cursor, max int) ([]bson.Raw, error) {
	defer cur.Close(ctx)
	var out []bson.Raw
	for len(out) < max && cur.Next(ctx) {
		out = append(out, append(bson.Raw(nil), cur.Current...))
	}
	return out, cur.Err()
}

// fill turns documents into rows: one column per top-level field, in order
// of first appearance; scalars stay JSON scalars and the rest is relaxed
// Extended JSON text.
func fill(res *exec.Result, docs []bson.Raw) {
	index := map[string]int{}
	for _, d := range docs {
		elems, _ := d.Elements()
		for _, e := range elems {
			if _, ok := index[e.Key()]; !ok {
				index[e.Key()] = len(res.Columns)
				res.Columns = append(res.Columns, exec.Column{Name: e.Key(), Type: typeName(e.Value().Type)})
			}
		}
	}
	for _, d := range docs {
		row := make([]json.RawMessage, len(res.Columns))
		for i := range row {
			row[i] = json.RawMessage("null")
		}
		elems, _ := d.Elements()
		for _, e := range elems {
			row[index[e.Key()]] = cell(e.Value())
		}
		res.Rows = append(res.Rows, row)
	}
}

const maxSafeInt = 1<<53 - 1

func cell(v bson.RawValue) json.RawMessage {
	var out any
	switch v.Type {
	case bson.TypeNull, bson.TypeUndefined:
		return json.RawMessage("null")
	case bson.TypeString:
		out = v.StringValue()
	case bson.TypeBoolean:
		out = v.Boolean()
	case bson.TypeInt32:
		out = v.Int32()
	case bson.TypeInt64:
		if n := v.Int64(); n <= maxSafeInt && n >= -maxSafeInt {
			out = n
		} else {
			out = fmt.Sprint(n)
		}
	case bson.TypeDouble:
		out = v.Double()
	case bson.TypeObjectID:
		out = v.ObjectID().Hex()
	case bson.TypeDateTime:
		out = v.Time().UTC().Format(time.RFC3339Nano)
	default:
		doc := bson.D{{Key: "v", Value: v}}
		b, err := bson.MarshalExtJSON(doc, false, false)
		if err != nil {
			out = v.String()
		} else {
			out = strings.TrimSuffix(strings.TrimPrefix(string(b), `{"v":`), "}")
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// explain asks for the query planner's choice only: the explain command's
// default verbosity runs the query.
func explain(ctx context.Context, d *mongo.Database, cmd bson.D) (*plan.Node, error) {
	inner := bson.D{}
	for _, e := range cmd {
		if e.Key != "comment" {
			inner = append(inner, e)
		}
	}
	if inner[0].Key == "aggregate" {
		if _, ok := get(inner, "cursor"); !ok {
			inner = set(inner, "cursor", bson.D{})
		}
	}
	var out bson.Raw
	err := d.RunCommand(ctx, bson.D{{Key: "explain", Value: inner}, {Key: "verbosity", Value: "queryPlanner"},
		{Key: "comment", Value: "dbscope"}}).Decode(&out)
	if err != nil {
		return nil, err
	}
	qp, ok := out.Lookup("queryPlanner").DocumentOK()
	if !ok {
		// An aggregate's plan sits in its first stage's $cursor.
		if stages, ok := out.Lookup("stages").ArrayOK(); ok {
			if first, err := stages.IndexErr(0); err == nil {
				qp, ok = first.Document().Lookup("$cursor", "queryPlanner").DocumentOK()
			}
		}
		if !ok {
			return nil, errors.New("explain returned no query plan")
		}
	}
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}, StepsOnly: true}
	if wp, ok := qp.Lookup("winningPlan").DocumentOK(); ok {
		// 8.0's slot-based engine nests the classic tree under queryPlan.
		if inner, ok := wp.Lookup("queryPlan").DocumentOK(); ok {
			wp = inner
		}
		root.Children = append(root.Children, planNode(wp))
	}
	return root, nil
}

func planNode(d bson.Raw) *plan.Node {
	n := &plan.Node{NodeType: d.Lookup("stage").StringValue(), Flags: []plan.Flag{}, Children: []*plan.Node{}}
	if s, ok := d.Lookup("indexName").StringValueOK(); ok {
		n.Index = s
	}
	if f, ok := d.Lookup("filter").DocumentOK(); ok {
		if b, err := bson.MarshalExtJSON(f, false, false); err == nil {
			n.Condition = string(b)
		}
	}
	if n.NodeType == "COLLSCAN" {
		n.Flags = append(n.Flags, plan.Flag{Level: "warn", Code: "collection_scan",
			Message: "Collection scan: no index serves this query, so every document is read"})
	}
	if c, ok := d.Lookup("inputStage").DocumentOK(); ok {
		n.Children = append(n.Children, planNode(c))
	}
	if arr, ok := d.Lookup("inputStages").ArrayOK(); ok {
		vals, _ := arr.Values()
		for _, v := range vals {
			if c, ok := v.DocumentOK(); ok {
				n.Children = append(n.Children, planNode(c))
			}
		}
	}
	return n
}

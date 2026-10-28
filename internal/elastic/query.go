package elastic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// Request is a parsed query: a search request as JSON, {"index": "logs-*",
// "query": {...}}, sent to _search; or SQL.
type Request struct {
	Index string
	Body  map[string]any // search body without "index"
	SQL   string
}

// maxResultWindow is index.max_result_window's default on both products.
const maxResultWindow = 10000

var sqlStart = regexp.MustCompile(`(?is)^\s*(select|show|describe|desc)\b`)

// Parse reads the editor text: JSON starting with "{" is a search request,
// anything else SQL. Only SELECT, SHOW and DESCRIBE are accepted as SQL; the
// SQL engines cannot write in their default settings, and the user's
// privileges are the real guard.
func Parse(text, defaultIndex string) (*Request, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, &guard.Error{Reason: guard.Syntax, Message: "The query is empty"}
	}
	if !strings.HasPrefix(text, "{") {
		text = strings.TrimRight(text, "; \n")
		if strings.Contains(text, ";") {
			return nil, &guard.Error{Reason: guard.MultipleStatements, Message: "Run one statement at a time"}
		}
		if !sqlStart.MatchString(text) {
			return nil, &guard.Error{Reason: guard.NotSelect, Message: "Only SQL SELECT, SHOW and DESCRIBE, or a search request as JSON, run here"}
		}
		return &Request{SQL: text}, nil
	}
	var body map[string]any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return nil, &guard.Error{Reason: guard.Syntax, Message: "The search request is not valid JSON: " + err.Error()}
	}
	if dec.More() {
		return nil, &guard.Error{Reason: guard.MultipleStatements, Message: "Run one search request at a time"}
	}
	r := &Request{Index: defaultIndex, Body: body}
	if ix, ok := body["index"].(string); ok {
		r.Index = ix
		delete(body, "index")
	}
	for _, k := range []string{"scroll", "pit", "keep_alive"} {
		if _, ok := body[k]; ok {
			return nil, &guard.Error{Reason: guard.NotSelect, Message: k + " keeps a search context open on the cluster; it is not run through dbscope"}
		}
	}
	from, size := intOf(body["from"]), intOf(body["size"])
	if from+size > maxResultWindow {
		return nil, &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("from + size is %d, beyond index.max_result_window (10000 by default); page with search_after instead", from+size)}
	}
	return r, nil
}

func intOf(v any) int {
	if n, ok := v.(json.Number); ok {
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// expensive are query and aggregation keys Elasticsearch and OpenSearch
// both list as expensive (search.allow_expensive_queries).
var expensive = map[string]string{
	"script":         "a script runs for every candidate document",
	"script_score":   "a script scores every matching document",
	"wildcard":       "wildcard queries walk the terms dictionary",
	"regexp":         "regexp queries walk the terms dictionary",
	"fuzzy":          "fuzzy queries expand to many terms",
	"prefix":         "prefix queries expand to every matching term unless the field has index_prefixes",
	"percolate":      "percolate runs every stored query",
	"has_child":      "joining queries are slow on large indices",
	"has_parent":     "joining queries are slow on large indices",
	"query_string":   "query_string parses user syntax that can turn into wildcard or regexp queries",
	"more_like_this": "more_like_this runs many term queries",
}

// searchFlags checks a search body without running it.
func searchFlags(index string, body map[string]any) []plan.Flag {
	flags := []plan.Flag{}
	if index == "" || index == "*" || index == "_all" {
		flags = append(flags, plan.Flag{Level: "warn", Code: "all_indices", Message: "Searches every index; name the indices or an alias"})
	}
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if why, ok := expensive[k]; ok && !seen[k] {
					seen[k] = true
					flags = append(flags, plan.Flag{Level: "warn", Code: "expensive_" + k, Message: "Expensive: " + why})
				}
				if k == "terms" {
					if t, ok := x[k].(map[string]any); ok && intOf(t["size"]) > 10000 {
						flags = append(flags, plan.Flag{Level: "warn", Code: "large_terms", Message: fmt.Sprintf("A terms aggregation asks for %d buckets; search.max_buckets is 65,536 by default", intOf(t["size"]))})
					}
				}
				walk(x[k])
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(body["query"])
	walk(body["aggs"])
	walk(body["aggregations"])
	if _, ok := body["script_fields"]; ok {
		walk(map[string]any{"script": true})
	}
	return flags
}

// Run executes a request read-only and returns at most maxRows rows. With
// explainOnly it only checks it: a search is validated (_validate/query,
// which rewrites but does not run it), SQL translated or explained.
func Run(ctx context.Context, c *Client, req *Request, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	if req.SQL != "" {
		product, err := c.Product(ctx)
		if err != nil {
			return nil, err
		}
		return c.runSQL(ctx, product, req.SQL, maxRows, explainOnly, timeout)
	}
	text, _ := json.Marshal(req.Body)
	res := &exec.Result{ExecutedSQL: string(text), Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	node := &plan.Node{NodeType: "Search " + req.Index, Flags: searchFlags(req.Index, req.Body), Children: []*plan.Node{}}
	res.Plan = &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{node}, StepsOnly: true}
	index := url.PathEscape(req.Index)
	if explainOnly {
		var v struct {
			Valid        bool `json:"valid"`
			Explanations []struct {
				Index       string `json:"index"`
				Explanation string `json:"explanation"`
				Error       string `json:"error"`
			} `json:"explanations"`
		}
		q := req.Body["query"]
		if q == nil {
			q = map[string]any{"match_all": map[string]any{}}
		}
		if err := c.do(ctx, "POST", "/"+index+"/_validate/query?explain=true", map[string]any{"query": q}, &v); err != nil {
			var e *Error
			if errors.As(err, &e) && e.Status == 403 {
				node.Flags = append(node.Flags, plan.Flag{Level: "info", Code: "not_validated",
					Message: "Only dbscope's own checks ran: validating the query needs indices:admin/validate/query (docs/ENGINES.md)"})
				return res, nil
			}
			return nil, err
		}
		for _, e := range v.Explanations {
			if e.Error != "" {
				node.Flags = append(node.Flags, plan.Flag{Level: "danger", Code: "invalid", Message: "Invalid on " + e.Index + ": " + e.Error})
			} else if node.Condition == "" {
				node.Condition = e.Explanation
			}
		}
		return res, nil
	}

	body := map[string]any{}
	for k, v := range req.Body {
		body[k] = v
	}
	size := intOf(req.Body["size"])
	if _, ok := req.Body["size"]; !ok {
		size = 10
	}
	if size > maxRows {
		size = maxRows
		res.Truncated = true
	}
	body["size"] = size
	start := time.Now()
	var out struct {
		TimedOut bool `json:"timed_out"`
		Hits     struct {
			Hits []struct {
				Index  string         `json:"_index"`
				ID     string         `json:"_id"`
				Score  *float64       `json:"_score"`
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
		Aggregations map[string]any `json:"aggregations"`
	}
	path := fmt.Sprintf("/%s/_search?timeout=%dms&allow_partial_search_results=false", index, timeout.Milliseconds())
	if err := c.do(ctx, "POST", path, body, &out); err != nil {
		return nil, err
	}
	if out.TimedOut {
		return nil, fmt.Errorf("the search timed out after %s on some shards: %w", timeout, context.DeadlineExceeded)
	}
	if len(out.Hits.Hits) > 0 {
		keys := map[string]bool{}
		for _, h := range out.Hits.Hits {
			for k := range h.Source {
				keys[k] = true
			}
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, n := range append([]string{"_index", "_id", "_score"}, names...) {
			res.Columns = append(res.Columns, exec.Column{Name: n})
		}
		for _, h := range out.Hits.Hits {
			row := []json.RawMessage{cell(h.Index), cell(h.ID), cell(h.Score)}
			for _, n := range names {
				row = append(row, cell(h.Source[n]))
			}
			res.Rows = append(res.Rows, row)
		}
	} else if len(out.Aggregations) > 0 {
		res.Columns = []exec.Column{{Name: "aggregation"}, {Name: "result"}}
		names := make([]string, 0, len(out.Aggregations))
		for k := range out.Aggregations {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, n := range names {
			res.Rows = append(res.Rows, []json.RawMessage{cell(n), cell(out.Aggregations[n])})
		}
	}
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

// runSQL runs SQL through Elasticsearch's _sql or OpenSearch's SQL plugin.
// Asynchronous options, which store results on the cluster, are never set.
func (c *Client) runSQL(ctx context.Context, product, sql string, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	res := &exec.Result{ExecutedSQL: sql, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	node := &plan.Node{NodeType: "SQL", Flags: []plan.Flag{}, Children: []*plan.Node{}}
	res.Plan = &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{node}, StepsOnly: true}
	if explainOnly {
		if product == "opensearch" {
			var tree map[string]any
			if err := c.do(ctx, "POST", "/_plugins/_sql/_explain", map[string]any{"query": sql}, &tree); err != nil {
				return nil, err
			}
			root, _ := tree["root"].(map[string]any)
			*node = *explainNode(root)
			return res, nil
		}
		var dsl map[string]any
		if err := c.do(ctx, "POST", "/_sql/translate", map[string]any{"query": sql}, &dsl); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(dsl)
		node.NodeType, node.Condition = "Search request", string(b)
		node.Flags = searchFlags("index", dsl)
		return res, nil
	}
	start := time.Now()
	if product == "opensearch" {
		var out struct {
			Schema []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"schema"`
			Rows [][]any `json:"datarows"`
		}
		if err := c.do(ctx, "POST", "/_plugins/_sql", map[string]any{"query": sql}, &out); err != nil {
			return nil, err
		}
		for _, s := range out.Schema {
			res.Columns = append(res.Columns, exec.Column{Name: s.Name, Type: s.Type})
		}
		res.Rows, res.Truncated = rows(out.Rows, maxRows)
	} else {
		var out struct {
			Columns []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"columns"`
			Rows   [][]any `json:"rows"`
			Cursor string  `json:"cursor"`
		}
		body := map[string]any{"query": sql, "fetch_size": maxRows + 1, "request_timeout": fmt.Sprintf("%dms", timeout.Milliseconds())}
		if err := c.do(ctx, "POST", "/_sql?format=json", body, &out); err != nil {
			return nil, err
		}
		if out.Cursor != "" { // more pages: free the cursor's search context now
			c.do(ctx, "POST", "/_sql/close", map[string]any{"cursor": out.Cursor}, nil)
			res.Truncated = true
		}
		for _, col := range out.Columns {
			res.Columns = append(res.Columns, exec.Column{Name: col.Name, Type: col.Type})
		}
		var trunc bool
		res.Rows, trunc = rows(out.Rows, maxRows)
		res.Truncated = res.Truncated || trunc
	}
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

func rows(in [][]any, maxRows int) ([][]json.RawMessage, bool) {
	out := [][]json.RawMessage{}
	for i, r := range in {
		if i == maxRows {
			return out, true
		}
		row := make([]json.RawMessage, len(r))
		for j, v := range r {
			row[j] = cell(v)
		}
		out = append(out, row)
	}
	return out, false
}

// explainNode turns OpenSearch's SQL explain tree into plan nodes; an index
// scan's search request is checked like a search.
func explainNode(m map[string]any) *plan.Node {
	name, _ := m["name"].(string)
	n := &plan.Node{NodeType: name, Flags: []plan.Flag{}, Children: []*plan.Node{}}
	if d, ok := m["description"].(map[string]any); ok {
		if req, ok := d["request"].(string); ok {
			n.Condition = req
			if i := strings.Index(req, "sourceBuilder="); i >= 0 {
				src := req[i+len("sourceBuilder="):]
				var body map[string]any
				if dec := json.NewDecoder(strings.NewReader(src)); dec.Decode(&body) == nil {
					n.Flags = searchFlags("index", body)
				}
			}
		} else {
			b, _ := json.Marshal(d)
			n.Condition = string(b)
		}
	}
	if kids, ok := m["children"].([]any); ok {
		for _, k := range kids {
			if km, ok := k.(map[string]any); ok {
				n.Children = append(n.Children, explainNode(km))
			}
		}
	}
	return n
}

const maxSafeInt = 1<<53 - 1

// cell renders a value as JSON: scalars as themselves, objects and arrays as
// JSON text, integers beyond JavaScript's exact range as text.
func cell(v any) json.RawMessage {
	var out any
	switch x := v.(type) {
	case nil:
		return json.RawMessage("null")
	case *float64:
		if x == nil {
			return json.RawMessage("null")
		}
		out = *x
	case string, bool:
		out = x
	case float64:
		if math.Abs(x) > maxSafeInt && x == math.Trunc(x) {
			out = fmt.Sprintf("%.0f", x)
		} else {
			out = x
		}
	case json.Number:
		if i, err := x.Int64(); err == nil && (i > maxSafeInt || i < -maxSafeInt) {
			out = x.String()
		} else {
			out = x
		}
	default:
		b, _ := json.Marshal(x)
		out = string(b)
	}
	b, _ := json.Marshal(out)
	return b
}

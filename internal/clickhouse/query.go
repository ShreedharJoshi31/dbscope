package clickhouse

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

type token struct {
	text string
	word string // lowercased unquoted word; "" for literals, quoted names and punctuation
}

// tokens splits ClickHouse SQL, dropping comments (--, #, /* */); quoted
// names and strings are never words.
func tokens(s string) []token {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case strings.HasPrefix(s[i:], "--"), c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = len(s)
			} else {
				i += end + 4
			}
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			out = append(out, token{text: s[i:j]})
			i = j
		case c == '_' || unicode.IsLetter(rune(c)):
			j := i
			for j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			out = append(out, token{text: s[i:j], word: strings.ToLower(s[i:j])})
			i = j
		default:
			out = append(out, token{text: string(c)})
			i++
		}
	}
	return out
}

var reading = map[string]bool{"select": true, "with": true, "show": true, "describe": true, "desc": true, "exists": true}

// external are table functions that reach files, URLs or other servers;
// readonly = 2 allows table functions, and these would read outside the
// database or from the network on the server's behalf.
var external = map[string]bool{"url": true, "s3": true, "s3cluster": true, "file": true, "remote": true, "remotesecure": true,
	"mysql": true, "postgresql": true, "mongodb": true, "redis": true, "hdfs": true, "jdbc": true, "odbc": true, "azureblobstorage": true,
	"gcs": true, "executable": true, "input": true, "cluster": true, "clusterallreplicas": true, "sqlite": true, "iceberg": true,
	"deltalake": true, "hudi": true, "urlcluster": true, "filecluster": true}

// Check accepts one SELECT, WITH, SHOW, DESCRIBE or EXISTS. The server's
// readonly mode is the real guard; this also refuses what readonly = 2
// leaves open (SET, RESTORE, temporary tables, table functions that reach
// outside) and output files.
func Check(stmt string) error {
	toks := tokens(stmt)
	for len(toks) > 0 && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return &guard.Error{Reason: guard.Syntax, Message: "The statement is empty"}
	}
	if !reading[toks[0].word] {
		if toks[0].word == "explain" {
			return &guard.Error{Reason: guard.NotSelect, Message: "Use the Explain button; EXPLAIN ANALYZE runs the query"}
		}
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only SELECT, WITH, SHOW, DESCRIBE and EXISTS run here; %s is not one", strings.ToUpper(toks[0].text))}
	}
	for i, t := range toks {
		switch {
		case t.text == ";":
			return &guard.Error{Reason: guard.MultipleStatements, Message: "Run one statement at a time"}
		case t.word == "outfile" || (t.word == "temporary" && i > 0 && toks[i-1].word == "create"):
			return &guard.Error{Reason: guard.SelectInto, Message: strings.ToUpper(t.text) + " writes outside the query; it is not run through dbscope"}
		case external[t.word] && i+1 < len(toks) && toks[i+1].text == "(" && (i == 0 || toks[i-1].text != "."):
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf("The table function %s reads outside the database; it is not run through dbscope", t.text)}
		}
	}
	return nil
}

// Run executes a statement that passed Check, returning at most maxRows
// rows; with explainOnly it returns EXPLAIN PLAN with index pruning and
// EXPLAIN ESTIMATE, neither of which runs the query.
func Run(ctx context.Context, c *Client, stmt string, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	stmt = strings.TrimRight(strings.TrimSpace(stmt), "; \n")
	res := &exec.Result{ExecutedSQL: stmt, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	if explainOnly {
		p, err := c.explain(ctx, stmt, timeout)
		if err != nil {
			return nil, err
		}
		res.Plan = p
		return res, nil
	}
	qctx, err := c.readCtx(ctx, timeout, maxRows, nil)
	if err != nil {
		return nil, err
	}
	qctx, detached := detach(qctx)
	defer detached()
	start := time.Now()
	rows, err := c.Conn.Query(qctx, stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	types := rows.ColumnTypes()
	dest := make([]any, len(types))
	for i, t := range types {
		res.Columns = append(res.Columns, exec.Column{Name: t.Name(), Type: t.DatabaseTypeName()})
		dest[i] = reflect.New(t.ScanType()).Interface()
	}
	for rows.Next() {
		if len(res.Rows) == maxRows {
			res.Truncated = true
			break
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]json.RawMessage, len(dest))
		for i, d := range dest {
			row[i] = cell(reflect.ValueOf(d).Elem())
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil && !res.Truncated {
		return nil, err
	}
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

// explain reads EXPLAIN PLAN indexes = 1 as JSON, flagging reads the
// sorting key does not narrow, and adds EXPLAIN ESTIMATE's row counts.
// From 25.9 the index output is only meaningful with the query condition
// cache and skip-index-on-read off, which a readonly = 1 user may not set.
func (c *Client) explain(ctx context.Context, stmt string, timeout time.Duration) (*plan.Node, error) {
	level, version, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}, StepsOnly: true}
	extra := ch.Settings{}
	if level != 1 && versionNum(version) >= 250900 {
		extra["use_query_condition_cache"], extra["use_skip_indexes_on_data_read"] = 0, 0
	}
	ectx, err := c.readCtx(ctx, timeout, 0, extra)
	if err != nil {
		return nil, err
	}
	ectx, detached := detach(ectx)
	defer detached()
	toks := tokens(stmt)
	if !(toks[0].word == "select" || toks[0].word == "with") {
		root.Children = append(root.Children, &plan.Node{NodeType: strings.ToUpper(toks[0].text), Condition: "reads the catalog", Flags: []plan.Flag{}, Children: []*plan.Node{}})
		return root, nil
	}
	var text string
	if err := c.Conn.QueryRow(ectx, "EXPLAIN PLAN indexes = 1, json = 1 "+stmt).Scan(&text); err != nil {
		return nil, err
	}
	var doc []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("unexpected EXPLAIN output: %w", err)
	}
	estimates := map[string]string{}
	if rows, err := c.Conn.Query(ectx, "EXPLAIN ESTIMATE "+stmt); err == nil {
		for rows.Next() {
			var db, table string
			var parts, rws, marks uint64
			if rows.Scan(&db, &table, &parts, &rws, &marks) == nil {
				estimates[db+"."+table] = fmt.Sprintf("about %d rows in %d parts", rws, parts)
			}
		}
		rows.Close()
	}
	for _, d := range doc {
		root.Children = append(root.Children, convert(d.Plan, estimates))
	}
	for i, t := range toks {
		if t.word == "final" && (i == 0 || toks[i-1].text != ".") {
			root.Flags = append(root.Flags, plan.Flag{Level: "warn", Code: "final",
				Message: "FINAL merges rows at query time, reading the sorting key columns and using more CPU and memory"})
			break
		}
	}
	return root, nil
}

func convert(m map[string]any, estimates map[string]string) *plan.Node {
	typ, _ := m["Node Type"].(string)
	desc, _ := m["Description"].(string)
	n := &plan.Node{NodeType: typ, Condition: desc, Flags: []plan.Flag{}, Children: []*plan.Node{}}
	if typ == "ReadFromMergeTree" {
		n.Relation = desc
		if e := estimates[desc]; e != "" {
			n.Condition = desc + " (" + e + ")"
		}
		indexFlags(n, m)
	}
	if kids, ok := m["Plans"].([]any); ok {
		for _, k := range kids {
			if km, ok := k.(map[string]any); ok {
				n.Children = append(n.Children, convert(km, estimates))
			}
		}
	}
	return n
}

// indexFlags reads a ReadFromMergeTree node's Indexes entries: the primary
// key selecting nearly every granule means the WHERE clause does not use a
// prefix of the sorting key.
func indexFlags(n *plan.Node, m map[string]any) {
	idx, _ := m["Indexes"].([]any)
	var sawPK bool
	for _, e := range idx {
		ix, _ := e.(map[string]any)
		typ, _ := ix["Type"].(string)
		cond, _ := ix["Condition"].(string)
		sel, initial := granules(ix)
		switch typ {
		case "PrimaryKey":
			sawPK = true
			if initial >= 8 && float64(sel) >= 0.9*float64(initial) {
				msg := fmt.Sprintf("The sorting key narrows nothing: %d of %d granules are read", sel, initial)
				if cond == "true" {
					msg = fmt.Sprintf("No condition on the sorting key: all %d granules are read", initial)
				}
				n.Flags = append(n.Flags, plan.Flag{Level: "warn", Code: "full_scan", Message: msg + "; filter on the leading ORDER BY columns"})
			}
			n.Index = fmt.Sprintf("primary key: %d of %d granules", sel, initial)
		case "Skip":
			name, _ := ix["Name"].(string)
			if initial > 0 && sel == initial {
				n.Flags = append(n.Flags, plan.Flag{Level: "info", Code: "skip_index_unused", Message: fmt.Sprintf("The skip index %s skips no granules for this query", name)})
			}
		}
	}
	if !sawPK && len(idx) == 0 {
		n.Flags = append(n.Flags, plan.Flag{Level: "info", Code: "no_index_info", Message: "EXPLAIN reported no index use for this read"})
	}
}

// granules reads selected and initial granules: 26.x reports them as
// "Selected Granules" and "Initial Granules", older releases as "Granules": "a/b".
func granules(ix map[string]any) (sel, initial int) {
	if v, ok := ix["Initial Granules"]; ok {
		return num(ix["Selected Granules"]), num(v)
	}
	if s, ok := ix["Granules"].(string); ok {
		a, b, _ := strings.Cut(s, "/")
		sel, _ = strconv.Atoi(strings.TrimSpace(a))
		initial, _ = strconv.Atoi(strings.TrimSpace(b))
	}
	return sel, initial
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

const maxSafeInt = 1<<53 - 1

// cell renders a scanned value: integers JavaScript holds exactly and finite
// floats as numbers; UInt64 beyond that, big integers, decimals, UUIDs and
// times as text; arrays, maps and tuples as JSON text.
func cell(v reflect.Value) json.RawMessage {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return json.RawMessage("null")
		}
		v = v.Elem()
	}
	var out any
	switch x := v.Interface().(type) {
	case string, bool:
		out = x
	case int, int8, int16, int32, int64:
		if n := v.Int(); n > maxSafeInt || n < -maxSafeInt {
			out = strconv.FormatInt(n, 10)
		} else {
			out = n
		}
	case uint, uint8, uint16, uint32, uint64:
		if n := v.Uint(); n > maxSafeInt {
			out = strconv.FormatUint(n, 10)
		} else {
			out = n
		}
	case float32, float64:
		if f := v.Float(); math.IsNaN(f) || math.IsInf(f, 0) {
			out = strconv.FormatFloat(f, 'g', -1, 64)
		} else {
			out = f
		}
	case big.Int:
		out = x.String()
	case time.Time:
		out = x.UTC().Format(time.RFC3339Nano)
	case []byte:
		out = "0x" + hex.EncodeToString(x)
	case fmt.Stringer:
		out = x.String()
	default:
		b, err := json.Marshal(x)
		if err != nil {
			out = fmt.Sprint(x)
		} else {
			out = string(b)
		}
	}
	b, _ := json.Marshal(out)
	return b
}

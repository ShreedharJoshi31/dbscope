package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// Check accepts one SELECT, WITH, VALUES or EXPLAIN statement. It is a
// courtesy that explains refusals early: Open's read-only, query_only
// connection is what keeps the file unchanged. Leading with one of those
// keywords also rules out ATTACH, which would reach other files on this
// machine; load_extension is refused for the same reason.
func Check(sqlText string) error {
	words, statements := scan(sqlText)
	if statements > 1 {
		return &guard.Error{Reason: guard.MultipleStatements, Message: "Run one statement at a time"}
	}
	if len(words) == 0 {
		return &guard.Error{Reason: guard.Syntax, Message: "The statement is empty"}
	}
	switch words[0] {
	case "select", "with", "values", "explain":
	default:
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only read-only queries run here; %s is not one", strings.ToUpper(words[0]))}
	}
	for _, w := range words {
		if w == "load_extension" {
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: "load_extension reaches outside the database file and is not allowed"}
		}
	}
	return nil
}

// scan returns the lower-cased words outside strings, quoted names and
// comments, and the number of statements.
func scan(s string) (words []string, statements int) {
	for i := 0; i < len(s); {
		j, tok := nextToken(s, i)
		if (tok != tkWS && tok != tkSemi && tok != tkOpen) && isWord(rune(s[i])) {
			words = append(words, strings.ToLower(s[i:j]))
		}
		i = j
	}
	return words, len(Split(s))
}

// Run executes a statement that passed Check, returning at most maxRows rows.
// ExplainOnly returns EXPLAIN QUERY PLAN as a plan tree without running it.
func Run(ctx context.Context, db *sql.DB, sqlText string, maxRows int, explainOnly bool) (*exec.Result, error) {
	sqlText = strings.TrimRight(strings.TrimSpace(sqlText), ";")
	res := &exec.Result{ExecutedSQL: sqlText, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer tx.Rollback()

	if p, err := queryPlan(ctx, tx, sqlText); err == nil {
		res.Plan = p
	} else if explainOnly {
		return nil, err
	}
	if explainOnly {
		return res, nil
	}

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
	for _, t := range types {
		res.Columns = append(res.Columns, exec.Column{Name: t.Name(), Type: strings.ToLower(t.DatabaseTypeName())})
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
			row[i] = value(v)
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

// maxSafeInt is the largest integer a JavaScript number holds exactly.
const maxSafeInt = 1<<53 - 1

// value maps a driver value to JSON the way exec.Value does for PostgreSQL:
// integers beyond JavaScript's exact range and non-finite floats stay strings,
// and blobs are hex like PostgreSQL's bytea_output.
func value(v any) json.RawMessage {
	var out any
	switch x := v.(type) {
	case nil:
		return json.RawMessage("null")
	case int64:
		if x > maxSafeInt || x < -maxSafeInt {
			out = fmt.Sprint(x)
		} else {
			out = x
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			out = fmt.Sprint(x)
		} else {
			out = x
		}
	case []byte:
		out = `\x` + hex.EncodeToString(x)
	case time.Time:
		out = x.Format(time.RFC3339Nano)
	default:
		out = fmt.Sprint(x)
	}
	b, _ := json.Marshal(out)
	return b
}

// queryPlan turns EXPLAIN QUERY PLAN's (id, parent, detail) rows into a plan
// tree. SQLite gives no costs, so only the node text is filled in.
func queryPlan(ctx context.Context, tx *sql.Tx, sqlText string) (*plan.Node, error) {
	rows, err := tx.QueryContext(ctx, "EXPLAIN QUERY PLAN "+sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}}
	byID := map[int64]*plan.Node{0: root}
	for rows.Next() {
		var id, parent, unused int64
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return nil, err
		}
		n := &plan.Node{NodeType: detail, Flags: []plan.Flag{}, Children: []*plan.Node{}}
		byID[id] = n
		up := byID[parent]
		if up == nil {
			up = root
		}
		up.Children = append(up.Children, n)
	}
	return root, rows.Err()
}

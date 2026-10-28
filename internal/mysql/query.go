package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// Run executes a statement that passed Check inside a READ ONLY transaction
// on one connection, bounded by timeout on the server as well as by ctx.
func Run(ctx context.Context, db *sql.DB, sqlText string, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	sqlText = strings.TrimRight(strings.TrimSpace(sqlText), "; \t\n")
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer c.Close()
	srv, err := Describe(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	// MySQL's limit covers SELECT only and is in milliseconds; MariaDB's covers any statement, in seconds.
	limit := fmt.Sprintf("SET SESSION max_execution_time = %d", timeout.Milliseconds())
	if srv.Flavor == "mariadb" {
		limit = fmt.Sprintf("SET SESSION max_statement_time = %.3f", timeout.Seconds())
	}
	if _, err := c.ExecContext(ctx, limit); err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	tx, err := c.BeginTx(ctx, readOnly)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer tx.Rollback()

	res := &exec.Result{ExecutedSQL: sqlText, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
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
	oids := make([]uint32, len(types))
	for i, t := range types {
		name := strings.ToLower(t.DatabaseTypeName())
		res.Columns = append(res.Columns, exec.Column{Name: t.Name(), Type: name})
		oids[i] = pseudoOID(name)
	}
	vals := make([]sql.RawBytes, len(types))
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
			switch {
			case v == nil:
				row[i] = json.RawMessage("null")
			case oids[i] == oidBinary:
				b, _ := json.Marshal(`\x` + hex.EncodeToString(v))
				row[i] = b
			default:
				row[i] = exec.Value(oids[i], v)
			}
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

// oidBinary marks byte columns, shown as hex like PostgreSQL's bytea.
const oidBinary = 17

// pseudoOID maps a MySQL type name to the PostgreSQL type exec.Value
// renders the same way, so both engines give the browser the same JSON.
func pseudoOID(t string) uint32 {
	switch t {
	case "tinyint", "smallint", "mediumint", "int", "year", "unsigned tinyint", "unsigned smallint", "unsigned mediumint":
		return 23 // int4
	case "bigint", "unsigned int", "unsigned bigint":
		return 20 // int8: values beyond 2^53 stay strings
	case "float", "double":
		return 701 // float8
	case "json":
		return 114
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit", "geometry":
		return oidBinary
	}
	return 0 // decimal, dates and text stay strings
}

// queryPlan turns tabular EXPLAIN, which MySQL and MariaDB share, into a
// plan with one node per row; neither server gives costs in this form.
func queryPlan(ctx context.Context, tx *sql.Tx, sqlText string) (*plan.Node, error) {
	if w := Tokens(sqlText); len(w) > 0 && (w[0].Word == "explain" || w[0].Word == "show" || w[0].Word == "describe" || w[0].Word == "desc") {
		return nil, fmt.Errorf("no plan for %s", strings.ToUpper(w[0].Word))
	}
	rows, err := tx.QueryContext(ctx, "EXPLAIN "+sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}, StepsOnly: true}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		get := func(name string) string {
			for i, c := range cols {
				if strings.EqualFold(c, name) {
					return vals[i].String
				}
			}
			return ""
		}
		access := get("type")
		n := &plan.Node{NodeType: strings.TrimSpace(get("select_type") + " " + access), Relation: get("table"),
			Index: get("key"), Condition: get("Extra"), Flags: []plan.Flag{}, Children: []*plan.Node{}}
		fmt.Sscanf(get("rows"), "%g", &n.PlanRows)
		root.Children = append(root.Children, n)
	}
	return root, rows.Err()
}

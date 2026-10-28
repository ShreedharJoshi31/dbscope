package oracle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// maxNames bounds the identifiers the PL/SQL gate resolves.
const maxNames = 200

// Run gates the statement, explains it, then runs it in a READ ONLY
// transaction that is always rolled back.
//
// A READ ONLY transaction does not stop a function declared PRAGMA
// AUTONOMOUS_TRANSACTION: it writes and commits in a transaction of its own
// (verified on 23.26). A reader cannot see the source of a function it
// reaches only through a view, so the gate refuses any query that reaches a
// PL/SQL function, procedure or package of a user schema, directly, through
// a synonym or through views. Oracle's own packages are trusted, apart from
// those Check refuses.
func Run(ctx context.Context, db *sql.DB, sqlText string, maxRows int, explainOnly bool) (*exec.Result, error) {
	sqlText = strings.TrimRight(strings.TrimSpace(sqlText), "; \t\n")
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer c.Close()

	if code, err := reachedCode(ctx, c, sqlText); err != nil {
		return nil, fmt.Errorf("%w: resolving names: %w", exec.ErrConnect, err)
	} else if len(code) > 0 {
		return nil, &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf(
			"The query reaches the PL/SQL %s; dbscope does not run user PL/SQL, which can write in an autonomous transaction even in a read-only one",
			strings.Join(code, ", "))}
	}
	root, err := explain(ctx, c, sqlText)
	if err != nil {
		return nil, err
	}
	if root.NodeType != "SELECT STATEMENT" {
		return nil, &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only read-only queries run here; the server planned this as %s", root.NodeType)}
	}
	res := &exec.Result{ExecutedSQL: sqlText, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}, Plan: root}
	if explainOnly {
		return res, nil
	}

	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
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
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = columnType(t)
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

// columnType names a result column's type as Oracle spells it.
func columnType(t *sql.ColumnType) string {
	switch n := t.DatabaseTypeName(); n {
	case "NUMBER":
		// A declared scale (NUMBER(10,2)) keeps every value as text, so a column never mixes numbers and strings;
		// go-ora reports scale 255 when the scale is unknown, as for expressions.
		if p, s, ok := t.DecimalSize(); ok && s > 0 && s != 255 {
			return fmt.Sprintf("number(%d,%d)", p, s)
		}
		return "number"
	case "TimeStampDTY":
		return "timestamp"
	case "TimeStampTZ_DTY", "TimeStampLTZ_DTY":
		return "timestamp with time zone"
	case "IBDouble", "IBFloat":
		return "binary_double"
	case "LongVarChar", "OCIClobLocator":
		return "clob"
	case "OCIBlobLocator":
		return "blob"
	case "NCHAR":
		return "text" // go-ora reports every character type as NCHAR
	default:
		return strings.ToLower(n)
	}
}

// value renders a go-ora value: NUMBER arrives as its exact decimal text.
func value(typ string, v any) json.RawMessage {
	switch x := v.(type) {
	case nil:
		return json.RawMessage("null")
	case string:
		if typ == "number" {
			return exec.Value(20, []byte(x)) // integers within 2^53 become numbers; the rest stay text
		}
		b, _ := json.Marshal(x)
		return b
	case float64:
		return exec.Value(701, []byte(strconv.FormatFloat(x, 'g', -1, 64)))
	case float32:
		return exec.Value(701, []byte(strconv.FormatFloat(float64(x), 'g', -1, 32)))
	case int64:
		return exec.Value(20, []byte(strconv.FormatInt(x, 10)))
	case time.Time:
		layout := "2006-01-02T15:04:05.999999999"
		if typ == "timestamp with time zone" {
			layout += "Z07:00"
		}
		b, _ := json.Marshal(x.Format(layout))
		return b
	case []byte:
		// JSON columns arrive as their text; other LOBs and RAW are shown as hex.
		if typ == "blob" && utf8.Valid(x) && json.Valid(x) {
			return json.RawMessage(x)
		}
		b, _ := json.Marshal(`\x` + hex.EncodeToString(x))
		return b
	}
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(fmt.Sprint(v))
	}
	return b
}

// candidateNames are the identifiers a statement may refer to, each part of
// a dotted name on its own, upper-cased unless quoted.
func candidateNames(sqlText string) []string {
	seen := map[string]bool{}
	var out []string
	addName := func(n string) {
		if n != "" && !seen[n] && len(out) < maxNames {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, t := range Tokens(sqlText) {
		switch {
		case t.Word != "":
			for _, part := range strings.Split(t.Text, ".") {
				addName(strings.ToUpper(part))
			}
		case strings.HasPrefix(t.Text, `"`):
			addName(strings.ReplaceAll(strings.Trim(t.Text, `"`), `""`, `"`))
		}
	}
	return out
}

// reachedCode returns the user-schema PL/SQL units the statement can reach:
// named directly, through a synonym, or through the views and other objects
// it depends on, as far as ALL_DEPENDENCIES lets this user see.
func reachedCode(ctx context.Context, c *sql.Conn, sqlText string) ([]string, error) {
	names := candidateNames(sqlText)
	if len(names) == 0 {
		return nil, nil
	}
	marks := make([]string, len(names))
	args := make([]any, len(names))
	for i, n := range names {
		marks[i], args[i] = "SELECT :"+strconv.Itoa(i+1)+" FROM dual", n
	}
	q := `WITH n (v) AS (` + strings.Join(marks, " UNION ALL ") + `),
		starts AS (
			SELECT o.owner, o.object_name AS name, o.object_type AS type FROM all_objects o WHERE o.object_name IN (SELECT v FROM n)
			UNION
			SELECT o.owner, o.object_name, o.object_type FROM all_synonyms s
				JOIN all_objects o ON o.owner = s.table_owner AND o.object_name = s.table_name
				WHERE s.synonym_name IN (SELECT v FROM n)),
		reached AS (
			SELECT owner, name, type FROM starts
			UNION
			SELECT d.referenced_owner, d.referenced_name, d.referenced_type FROM all_dependencies d
				START WITH (d.owner, d.name) IN (SELECT owner, name FROM starts)
				CONNECT BY NOCYCLE PRIOR d.referenced_owner = d.owner AND PRIOR d.referenced_name = d.name)
		SELECT DISTINCT r.owner || '.' || r.name FROM reached r
			JOIN all_users u ON u.username = r.owner AND u.oracle_maintained = 'N'
		WHERE r.type IN ('FUNCTION', 'PROCEDURE', 'PACKAGE', 'PACKAGE BODY')
		ORDER BY 1`
	rows, err := c.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// explain plans the statement with EXPLAIN PLAN, which never executes it but
// writes PLAN_TABLE and so cannot run in a READ ONLY transaction; its rows
// are read back and rolled away.
func explain(ctx context.Context, c *sql.Conn, sqlText string) (*plan.Node, error) {
	idBytes := make([]byte, 6)
	rand.Read(idBytes)
	id := "dbscope-" + hex.EncodeToString(idBytes)
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exec.ErrConnect, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "EXPLAIN PLAN SET STATEMENT_ID = '"+id+"' FOR "+sqlText); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, parent_id, operation, options, object_owner, object_name, object_alias,
			cardinality, cost, access_predicates, filter_predicates
		FROM plan_table WHERE statement_id = :1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int]*plan.Node{}
	var root *plan.Node
	for rows.Next() {
		var nid int
		var parent sql.NullInt64
		var op string
		var options, owner, object, alias, access, filter sql.NullString
		var card, cost sql.NullFloat64
		if err := rows.Scan(&nid, &parent, &op, &options, &owner, &object, &alias, &card, &cost, &access, &filter); err != nil {
			return nil, err
		}
		n := &plan.Node{NodeType: strings.TrimSpace(op + " " + options.String), PlanRows: card.Float64, TotalCost: cost.Float64,
			Flags: []plan.Flag{}, Children: []*plan.Node{}}
		if object.Valid && owner.Valid {
			n.Relation = owner.String + "." + object.String
		}
		n.Alias, _, _ = strings.Cut(alias.String, "@")
		n.Condition = strings.TrimSpace(strings.Join(nonEmpty(access.String, filter.String), " AND "))
		switch {
		case n.NodeType == "TABLE ACCESS FULL" && filter.String != "":
			n.Flags = append(n.Flags, plan.Flag{Level: "warn", Code: "full_scan",
				Message: fmt.Sprintf("Reads all of %s to filter it; an index on the filtered columns would let the server go to the rows", n.Relation)})
		case op == "MERGE JOIN" && options.String == "CARTESIAN":
			n.Flags = append(n.Flags, plan.Flag{Level: "danger", Code: "cartesian",
				Message: "A Cartesian join: every row of one side meets every row of the other"})
		}
		byID[nid] = n
		if !parent.Valid {
			root = n
		} else if p := byID[int(parent.Int64)]; p != nil {
			p.Children = append(p.Children, n)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("EXPLAIN PLAN wrote no plan")
	}
	plan.SetCostShares(root)
	return root, nil
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

package neo4j

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

type token struct {
	text string
	word string // lowercased unquoted word; "" for literals, quoted names and punctuation
}

// tokens splits Cypher into words, literals and punctuation, dropping
// comments; backquoted names and strings are never words.
func tokens(s string) []token {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case strings.HasPrefix(s[i:], "//"):
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
				if s[j] == '\\' && c != '`' {
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

// writing are clauses and commands that change data, schema, users or
// databases, or run outside a single read transaction.
var writing = map[string]string{"create": "CREATE", "merge": "MERGE", "delete": "DELETE", "detach": "DETACH DELETE", "set": "SET",
	"remove": "REMOVE", "foreach": "FOREACH", "drop": "DROP", "alter": "ALTER", "grant": "GRANT", "revoke": "REVOKE", "deny": "DENY",
	"rename": "RENAME", "terminate": "TERMINATE", "enable": "ENABLE", "deallocate": "DEALLOCATE", "reallocate": "REALLOCATE"}

// Check refuses Cypher that writes or leaves the read transaction before it
// is sent: write clauses, LOAD CSV, CALL { } IN TRANSACTIONS, USE, EXPLAIN
// and PROFILE (which runs the query). The plan's query type checks again.
func Check(stmt string) error {
	toks := tokens(stmt)
	for len(toks) > 0 && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return &guard.Error{Reason: guard.Syntax, Message: "The statement is empty"}
	}
	for i, t := range toks {
		switch {
		case t.text == ";":
			return &guard.Error{Reason: guard.MultipleStatements, Message: "Run one statement at a time"}
		case writing[t.word] != "" && !(i > 0 && toks[i-1].text == "."):
			return &guard.Error{Reason: guard.WritesData, Message: fmt.Sprintf("Only reading Cypher runs here; %s changes the database", writing[t.word])}
		case t.word == "load" && i+1 < len(toks) && toks[i+1].word == "csv":
			return &guard.Error{Reason: guard.NotSelect, Message: "LOAD CSV is not run through dbscope"}
		case t.word == "in" && i+1 < len(toks) && toks[i+1].word == "transactions":
			return &guard.Error{Reason: guard.WritesData, Message: "CALL { } IN TRANSACTIONS commits batches of its own; it is not run through dbscope"}
		case t.word == "use" && (i == 0 || toks[i-1].text != "."):
			return &guard.Error{Reason: guard.NotSelect, Message: "USE switches databases; the profile names the database"}
		case (t.word == "explain" || t.word == "profile") && i == 0:
			return &guard.Error{Reason: guard.NotSelect, Message: "Use the Explain button; PROFILE runs the query"}
		}
	}
	return nil
}

// procedures returns the names of procedures a statement CALLs.
func procedures(toks []token) []string {
	var out []string
	for i, t := range toks {
		if t.word != "call" || i+1 >= len(toks) || toks[i+1].word == "" {
			continue
		}
		name := toks[i+1].text
		for j := i + 2; j+1 < len(toks) && toks[j].text == "." && toks[j+1].word != ""; j += 2 {
			name += "." + toks[j+1].text
		}
		out = append(out, name)
	}
	return out
}

// checkProcedures allows only procedures whose mode is READ; DBMS-mode
// procedures include admin ones such as db.clearQueryCaches.
func (c *Client) checkProcedures(ctx context.Context, names []string) error {
	for _, n := range names {
		recs, _, err := c.read(ctx, 10*time.Second, "SHOW PROCEDURES YIELD name, mode WHERE toLower(name) = toLower($n) RETURN mode", map[string]any{"n": n})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			continue // unknown: EXPLAIN reports it
		}
		if mode, _ := recs[0].Values[0].(string); mode != "READ" {
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf("Procedure %s runs in %s mode; only READ procedures run here", n, mode)}
		}
	}
	return nil
}

// Run checks a statement's plan, then executes it in a read transaction that
// is rolled back, returning at most maxRows records.
func Run(ctx context.Context, c *Client, stmt string, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	stmt = strings.TrimRight(strings.TrimSpace(stmt), "; \n")
	res := &exec.Result{ExecutedSQL: stmt, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	if err := c.checkProcedures(ctx, procedures(tokens(stmt))); err != nil {
		return nil, err
	}
	_, sum, err := c.read(ctx, timeout, "EXPLAIN "+stmt, nil)
	if err != nil {
		return nil, err
	}
	if qt := sum.QueryType(); qt != drv.StatementTypeReadOnly {
		return nil, &guard.Error{Reason: guard.WritesData, Message: fmt.Sprintf("Neo4j plans this as a %s statement; only read-only Cypher runs here", queryTypeName(qt))}
	}
	res.Plan = planFor(sum)
	if explainOnly {
		return res, nil
	}

	start := time.Now()
	s := c.Driver.NewSession(ctx, drv.SessionConfig{AccessMode: drv.AccessModeRead, DatabaseName: c.Database, FetchSize: min(maxRows+1, 1000)})
	defer s.Close(ctx)
	tx, err := s.BeginTransaction(ctx, drv.WithTxTimeout(timeout))
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := tx.Run(ctx, stmt, nil)
	if err != nil {
		return nil, err
	}
	keys, err := r.Keys()
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		res.Columns = append(res.Columns, exec.Column{Name: k})
	}
	for len(res.Rows) <= maxRows && r.Next(ctx) {
		rec := r.Record()
		row := make([]json.RawMessage, len(rec.Values))
		for i, v := range rec.Values {
			row[i] = cell(v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	if len(res.Rows) > maxRows {
		res.Rows, res.Truncated = res.Rows[:maxRows], true
	}
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

func queryTypeName(t drv.StatementType) string {
	switch t {
	case drv.StatementTypeReadWrite:
		return "read-write"
	case drv.StatementTypeWriteOnly:
		return "write"
	case drv.StatementTypeSchemaWrite:
		return "schema"
	}
	return "non-read"
}

// unbounded matches a variable-length pattern's bounds in an operator's
// details: [:T*], [:T*2..] have no upper bound; [:T*..3], [:T*2] do.
var varLength = regexp.MustCompile(`\*(\d*)(\.\.(\d*))?\]`)

func unbounded(details string) bool {
	for _, m := range varLength.FindAllStringSubmatch(details, -1) {
		if (m[1] == "" && m[2] == "") || (m[2] != "" && m[3] == "") {
			return true
		}
	}
	return false
}

// statusFlags are the performance and warning GQLSTATUS codes worth a flag;
// codes are stable, descriptions are not, so only the code decides.
var statusFlags = map[string]string{"03N90": "warn", "03N91": "warn", "03N92": "warn", "03N93": "warn", "03N94": "warn",
	"03N95": "info", "01N50": "warn", "01N51": "warn", "01N52": "warn"}

// planFor turns an EXPLAIN plan into dbscope's tree, flagging operators that
// read everything, multiply rows or hold rows in memory.
func planFor(sum drv.ResultSummary) *plan.Node {
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}, StepsOnly: true}
	if p := sum.Plan(); p != nil {
		root.Children = append(root.Children, convert(p))
	}
	for _, g := range sum.GqlStatusObjects() {
		if level, ok := statusFlags[g.GqlStatus()]; ok && len(root.Children) > 0 {
			root.Children[0].Flags = append(root.Children[0].Flags, plan.Flag{Level: level, Code: "gql_" + g.GqlStatus(), Message: g.StatusDescription()})
		}
	}
	return root
}

func convert(p drv.Plan) *plan.Node {
	op := p.Operator()
	if i := strings.IndexByte(op, '@'); i >= 0 {
		op = op[:i]
	}
	details, _ := p.Arguments()["Details"].(string)
	est, _ := p.Arguments()["EstimatedRows"].(float64)
	n := &plan.Node{NodeType: op, Condition: strings.TrimSpace(fmt.Sprintf("%s  (est. %s rows)", details, rows(est))), Flags: []plan.Flag{}, Children: []*plan.Node{}}
	flag := func(level, code, msg string) {
		n.Flags = append(n.Flags, plan.Flag{Level: level, Code: code, Message: msg})
	}
	switch {
	case op == "AllNodesScan" || op == "PartitionedAllNodesScan":
		flag("danger", "all_nodes_scan", "Reads every node in the database; start from a label and an indexed property")
	case strings.HasSuffix(op, "AllRelationshipsScan"):
		flag("danger", "all_relationships_scan", "Reads every relationship in the database")
	case op == "NodeByLabelScan" || op == "PartitionedNodeByLabelScan":
		level := "info"
		if est >= 100000 {
			level = "warn"
		}
		flag(level, "label_scan", fmt.Sprintf("Reads every %s node (about %s); an index on the filtered property would seek instead", details, rows(est)))
	case strings.HasPrefix(op, "NodeIndexScan") || strings.HasPrefix(op, "NodeIndexContainsScan") || strings.HasPrefix(op, "NodeIndexEndsWithScan"):
		flag("info", "index_scan", "Reads the whole index rather than seeking into it")
	case op == "CartesianProduct":
		level := "warn"
		if est >= 1e6 {
			level = "danger"
		}
		flag(level, "cartesian_product", fmt.Sprintf("Pairs every row of one side with every row of the other (about %s rows); connect the patterns or match them separately", rows(est)))
	case op == "Eager":
		flag("warn", "eager", "Eager holds all earlier rows in memory before continuing")
	case (strings.HasPrefix(op, "VarLengthExpand") || strings.HasPrefix(op, "Repeat") || strings.Contains(op, "ShortestPath")) && unbounded(details):
		flag("danger", "unbounded_path", "A variable-length pattern with no upper bound can walk the whole graph; add one, such as *1..5")
	}
	for _, c := range p.Children() {
		n.Children = append(n.Children, convert(c))
	}
	return n
}

func rows(f float64) string {
	switch {
	case f >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case f >= 1e4:
		return fmt.Sprintf("%.0fk", f/1e3)
	}
	return fmt.Sprintf("%.0f", math.Round(f))
}

const maxSafeInt = 1<<53 - 1

// cell renders a value as JSON: scalars as themselves (integers beyond
// JavaScript's exact range as text), and nodes, relationships, paths,
// lists and maps as JSON text.
func cell(v any) json.RawMessage {
	var out any
	switch x := v.(type) {
	case nil:
		return json.RawMessage("null")
	case string, bool:
		out = x
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
	default:
		pv := plain(v)
		if s, ok := pv.(string); ok { // temporal and point values
			out = s
			break
		}
		b, err := json.Marshal(pv)
		if err != nil {
			out = fmt.Sprint(v)
		} else {
			out = string(b)
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// plain converts graph and temporal values for JSON.
func plain(v any) any {
	switch x := v.(type) {
	case dbtype.Node:
		m := map[string]any{"labels": x.Labels}
		for k, p := range x.Props {
			m[k] = plain(p)
		}
		return m
	case dbtype.Relationship:
		m := map[string]any{"type": x.Type}
		for k, p := range x.Props {
			m[k] = plain(p)
		}
		return m
	case dbtype.Path:
		nodes := make([]any, len(x.Nodes))
		for i, n := range x.Nodes {
			nodes[i] = plain(n)
		}
		rels := make([]any, len(x.Relationships))
		for i, r := range x.Relationships {
			rels[i] = plain(r)
		}
		return map[string]any{"nodes": nodes, "relationships": rels}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plain(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = plain(x[k])
		}
		return out
	case fmt.Stringer:
		return x.String()
	case []byte:
		return fmt.Sprintf("0x%x", x)
	}
	return v
}

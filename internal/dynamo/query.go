package dynamo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

// token is a PartiQL token: a word (lower-cased), a quoted name or string, or punctuation.
type token struct {
	text, word string
	quoted     bool // "name" or 'string'
}

func tokens(s string) []token {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return out
			}
			i += j + 1
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && !(s[j] == c && (j+1 >= len(s) || s[j+1] != c)) {
				if s[j] == c {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			out = append(out, token{text: s[i:j], quoted: true})
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			j := i
			for j < len(s) && (s[j] == '_' || s[j] == '.' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			out = append(out, token{text: s[i:j], word: strings.ToLower(s[i:j])})
			i = j
		default:
			out = append(out, token{text: s[i : i+1]})
			i++
		}
	}
	return out
}

// Check accepts one PartiQL SELECT. PartiQL for DynamoDB has only SELECT,
// INSERT, UPDATE and DELETE, so anything else is refused; an IAM policy that
// allows only PartiQLSelect is what keeps the tables unchanged.
func Check(stmt string) error {
	toks := tokens(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(stmt), ";")))
	if len(toks) == 0 {
		return &guard.Error{Reason: guard.Syntax, Message: "The statement is empty"}
	}
	for _, t := range toks {
		if t.text == ";" {
			return &guard.Error{Reason: guard.MultipleStatements, Message: "Run one statement at a time"}
		}
	}
	if toks[0].word != "select" {
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only PartiQL SELECT runs here; %s is not one", strings.ToUpper(toks[0].text))}
	}
	return nil
}

// target is the table (and index) a SELECT reads: FROM "table" or "table"."index".
func target(toks []token) (table, index string) {
	for i, t := range toks {
		if t.word != "from" || i+1 >= len(toks) {
			continue
		}
		name := toks[i+1]
		table = strings.Trim(name.text, `"`)
		if name.quoted && i+3 < len(toks) && toks[i+2].text == "." && toks[i+3].quoted {
			index = strings.Trim(toks[i+3].text, `"`)
		} else if !name.quoted {
			table, index, _ = strings.Cut(name.text, ".")
		}
		return table, index
	}
	return "", ""
}

// pinsKey reports whether the WHERE clause gives the partition key with = or
// IN, which DynamoDB needs to read one partition instead of the whole table.
func pinsKey(toks []token, key string) bool {
	where := -1
	for i, t := range toks {
		if t.word == "where" {
			where = i
		}
	}
	if where < 0 {
		return false
	}
	for i := where + 1; i+1 < len(toks); i++ {
		name := strings.Trim(toks[i].text, `"`)
		if strings.EqualFold(name, key) && (toks[i+1].text == "=" || toks[i+1].word == "in") {
			return true
		}
	}
	return false
}

// Run executes a SELECT that passed Check, returning at most maxRows items.
// Its plan says which table or index it reads and whether that is a scan.
func Run(ctx context.Context, c *dynamodb.Client, stmt string, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stmt = strings.TrimRight(strings.TrimSpace(stmt), "; \n")
	res := &exec.Result{ExecutedSQL: stmt, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	p, err := planFor(ctx, c, tokens(stmt))
	if err != nil {
		return nil, err
	}
	res.Plan = p
	if explainOnly {
		return res, nil
	}
	start := time.Now()
	var items []map[string]types.AttributeValue
	var next *string
	consumed := 0.0
	for len(items) <= maxRows {
		out, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{Statement: aws.String(stmt), NextToken: next,
			Limit: aws.Int32(int32(maxRows + 1 - len(items))), ReturnConsumedCapacity: types.ReturnConsumedCapacityTotal})
		if err != nil {
			return nil, err
		}
		items = append(items, out.Items...)
		if out.ConsumedCapacity != nil && out.ConsumedCapacity.CapacityUnits != nil {
			consumed += *out.ConsumedCapacity.CapacityUnits
		}
		if out.NextToken == nil {
			break
		}
		next = out.NextToken
	}
	if len(items) > maxRows {
		items, res.Truncated = items[:maxRows], true
	}
	fill(res, items)
	res.Warnings = append(res.Warnings, fmt.Sprintf("consumed %.1f read capacity units", consumed))
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

func planFor(ctx context.Context, c *dynamodb.Client, toks []token) (*plan.Node, error) {
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}, StepsOnly: true}
	table, index := target(toks)
	if table == "" {
		return root, nil
	}
	d, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	if err != nil {
		return nil, err
	}
	keys := d.Table.KeySchema
	if index != "" {
		for _, g := range d.Table.GlobalSecondaryIndexes {
			if aws.ToString(g.IndexName) == index {
				keys = g.KeySchema
			}
		}
		for _, l := range d.Table.LocalSecondaryIndexes {
			if aws.ToString(l.IndexName) == index {
				keys = l.KeySchema
			}
		}
	}
	// A GSI's partition key can have up to four attributes; a Query needs every one.
	var hash []string
	pinned := true
	for _, k := range keys {
		if k.KeyType == types.KeyTypeHash {
			name := aws.ToString(k.AttributeName)
			hash = append(hash, name)
			pinned = pinned && pinsKey(toks, name)
		}
	}
	n := &plan.Node{Relation: table, Index: index, Flags: []plan.Flag{}, Children: []*plan.Node{}}
	if pinned && len(hash) > 0 {
		n.NodeType = "Query (partition key " + strings.Join(hash, ", ") + ")"
	} else {
		n.NodeType = "Scan"
		n.Flags = append(n.Flags, plan.Flag{Level: "warn", Code: "full_scan",
			Message: fmt.Sprintf("Full scan: the WHERE clause does not give the partition key %s with = or IN, so every item is read and billed", strings.Join(hash, ", "))})
	}
	root.Children = append(root.Children, n)
	return root, nil
}

// fill turns items into rows: one column per attribute in name order,
// numbers as JSON numbers when they fit exactly, the rest as text.
func fill(res *exec.Result, items []map[string]types.AttributeValue) {
	seen := map[string]bool{}
	var names []string
	for _, it := range items {
		for k := range it {
			if !seen[k] {
				seen[k] = true
				names = append(names, k)
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		res.Columns = append(res.Columns, exec.Column{Name: n})
	}
	for _, it := range items {
		row := make([]json.RawMessage, len(names))
		for i, n := range names {
			row[i] = cell(it[n])
		}
		res.Rows = append(res.Rows, row)
	}
}

const maxSafeInt = 1<<53 - 1

func cell(v types.AttributeValue) json.RawMessage {
	var out any
	switch x := v.(type) {
	case nil, *types.AttributeValueMemberNULL:
		return json.RawMessage("null")
	case *types.AttributeValueMemberS:
		out = x.Value
	case *types.AttributeValueMemberN:
		// DynamoDB numbers have 38 digits; only exact JavaScript numbers leave the string.
		if n, err := strconv.ParseInt(x.Value, 10, 64); err == nil && n <= maxSafeInt && n >= -maxSafeInt {
			out = n
		} else if f, err := strconv.ParseFloat(x.Value, 64); err == nil && strconv.FormatFloat(f, 'f', -1, 64) == x.Value {
			out = f
		} else {
			out = x.Value
		}
	case *types.AttributeValueMemberBOOL:
		out = x.Value
	case *types.AttributeValueMemberB:
		out = base64.StdEncoding.EncodeToString(x.Value)
	default:
		b, _ := json.Marshal(plain(v))
		out = string(b)
	}
	b, _ := json.Marshal(out)
	return b
}

// plain converts maps, lists and sets for display as JSON text.
func plain(v types.AttributeValue) any {
	switch x := v.(type) {
	case *types.AttributeValueMemberM:
		m := map[string]any{}
		for k, e := range x.Value {
			m[k] = plain(e)
		}
		return m
	case *types.AttributeValueMemberL:
		l := make([]any, len(x.Value))
		for i, e := range x.Value {
			l[i] = plain(e)
		}
		return l
	case *types.AttributeValueMemberSS:
		return x.Value
	case *types.AttributeValueMemberNS:
		return x.Value
	case *types.AttributeValueMemberS:
		return x.Value
	case *types.AttributeValueMemberN:
		return json.Number(x.Value)
	case *types.AttributeValueMemberBOOL:
		return x.Value
	case *types.AttributeValueMemberNULL:
		return nil
	}
	return fmt.Sprintf("%T", v)
}

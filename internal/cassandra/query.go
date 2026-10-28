package cassandra

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strings"
	"time"
	"unicode"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"gopkg.in/inf.v0"

	"github.com/dbscope/dbscope/internal/query/exec"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
)

type token struct {
	text  string // as written
	word  string // lowercased unquoted identifier or keyword; "" otherwise
	ident string // identifier as Cassandra resolves it: folded unless quoted
}

// tokens splits CQL into words, quoted identifiers, literals and punctuation,
// dropping comments (--, // and /* */).
func tokens(s string) []token {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case strings.HasPrefix(s[i:], "--"), strings.HasPrefix(s[i:], "//"):
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
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			j = min(j+1, len(s))
			t := token{text: s[i:j]}
			if c == '"' {
				t.ident = strings.ReplaceAll(strings.Trim(t.text, `"`), `""`, `"`)
			}
			out = append(out, t)
			i = j
		case strings.HasPrefix(s[i:], "$$"):
			end := strings.Index(s[i+2:], "$$")
			j := len(s)
			if end >= 0 {
				j = i + end + 4
			}
			out = append(out, token{text: s[i:j]})
			i = j
		case c == '_' || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)):
			j := i
			for j < len(s) && (s[j] == '_' || s[j] == '-' && j > i && isHexish(s[i:j]) || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			w := strings.ToLower(s[i:j])
			out = append(out, token{text: s[i:j], word: w, ident: w})
			i = j
		case strings.ContainsRune("<>!", rune(c)) && i+1 < len(s) && s[i+1] == '=':
			out = append(out, token{text: s[i : i+2]})
			i += 2
		default:
			out = append(out, token{text: string(c)})
			i++
		}
	}
	return out
}

// isHexish lets a UUID literal (8-4-4-4-12 hex digits) stay one token.
func isHexish(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF-", r) {
			return false
		}
	}
	return true
}

// blocked are tables a SELECT must not read: role secrets (ScyllaDB's
// salted_hash is also the Alternator key) and ScyllaDB's writable config.
var blocked = map[string]bool{"system.roles": true, "system.role_members": true, "system.role_permissions": true,
	"system.role_attributes": true, "system.config": true}

// Check accepts one SELECT and nothing else; the server's grants are the
// real guard (docs/ENGINES.md), this refuses early and keeps secrets out.
func Check(stmt string) error {
	toks := tokens(stmt)
	for len(toks) > 0 && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return &guard.Error{Reason: guard.Syntax, Message: "The statement is empty"}
	}
	if toks[0].word != "select" {
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only CQL SELECT runs here; %s is not one", strings.ToUpper(toks[0].text))}
	}
	for _, t := range toks {
		if t.text == ";" {
			return &guard.Error{Reason: guard.MultipleStatements, Message: "Run one statement at a time"}
		}
		if t.ident == "salted_hash" {
			return &guard.Error{Reason: guard.NotSelect, Message: "Password hashes are not readable through dbscope"}
		}
	}
	ks, table := target(toks)
	if ks == "system_auth" || blocked[ks+"."+table] {
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("%s.%s holds roles, secrets or settings and is not readable through dbscope", ks, table)}
	}
	return nil
}

// target returns the keyspace and table after FROM; keyspace is "" when unqualified.
func target(toks []token) (keyspace, table string) {
	for i, t := range toks {
		if t.word != "from" || i+1 >= len(toks) {
			continue
		}
		if i+3 < len(toks) && toks[i+2].text == "." {
			return toks[i+1].ident, toks[i+3].ident
		}
		return "", toks[i+1].ident
	}
	return "", ""
}

// restriction is one AND-ed condition of a WHERE clause.
type restriction struct {
	cols  []string // one column, or several in a tuple
	op    string   // =, in, <, >, <=, >=, !=, contains, like, is
	token bool     // token(...) bound
}

var clauseEnd = map[string]bool{"group": true, "order": true, "limit": true, "per": true, "allow": true, "using": true}

func restrictions(toks []token) (out []restriction, allowFiltering bool) {
	where := -1
	for i, t := range toks {
		if t.word == "where" && where < 0 {
			where = i
		}
		if t.word == "allow" && i+1 < len(toks) && toks[i+1].word == "filtering" {
			allowFiltering = true
		}
	}
	if where < 0 {
		return nil, allowFiltering
	}
	var cur []token
	depth := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, parseRestriction(cur))
		}
		cur = nil
	}
	for _, t := range toks[where+1:] {
		switch {
		case t.text == "(" || t.text == "[" || t.text == "{":
			depth++
		case t.text == ")" || t.text == "]" || t.text == "}":
			depth--
		case depth == 0 && t.word == "and":
			flush()
			continue
		case depth == 0 && clauseEnd[t.word]:
			flush()
			return out, allowFiltering
		}
		cur = append(cur, t)
	}
	flush()
	return out, allowFiltering
}

func parseRestriction(ts []token) restriction {
	var r restriction
	i := 0
	switch {
	case ts[0].word == "token":
		r.token = true
		for i < len(ts) && ts[i].text != ")" {
			if ts[i].ident != "" && ts[i].word != "token" {
				r.cols = append(r.cols, ts[i].ident)
			}
			i++
		}
		i++
	case ts[0].text == "(":
		for i = 1; i < len(ts) && ts[i].text != ")"; i++ {
			if ts[i].ident != "" {
				r.cols = append(r.cols, ts[i].ident)
			}
		}
		i++
	default:
		r.cols = []string{ts[0].ident}
		i = 1
	}
	if i < len(ts) {
		r.op = ts[i].text
		if ts[i].word != "" {
			r.op = ts[i].word
		}
	}
	return r
}

// tableInfo is what the classifier needs to know about the table read.
type tableInfo struct {
	partition  []string
	clustering []string
	indexes    map[string]indexInfo // by column
}

type indexInfo struct{ name, kind string }

func loadTable(ctx context.Context, s *gocql.Session, ks, table string) (*tableInfo, error) {
	ti := &tableInfo{indexes: map[string]indexInfo{}}
	type kc struct {
		name string
		pos  int
	}
	var parts, clus []kc
	var col, kind string
	var pos int
	iter := s.Query("SELECT column_name, kind, position FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?", ks, table).IterContext(ctx)
	for iter.Scan(&col, &kind, &pos) {
		switch kind {
		case "partition_key":
			parts = append(parts, kc{col, pos})
		case "clustering":
			clus = append(clus, kc{col, pos})
		}
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, nil // unknown table: the server will say so
	}
	for _, list := range []struct {
		in  []kc
		out *[]string
	}{{parts, &ti.partition}, {clus, &ti.clustering}} {
		*list.out = make([]string, len(list.in))
		for _, k := range list.in {
			if k.pos >= 0 && k.pos < len(list.in) {
				(*list.out)[k.pos] = k.name
			}
		}
	}
	flavor := "cassandra"
	if srv, err := describe(ctx, s); err == nil {
		flavor = srv.Flavor
	}
	var name string
	var opts map[string]string
	iter = s.Query("SELECT index_name, kind, options FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?", ks, table).IterContext(ctx)
	for iter.Scan(&name, &kind, &opts) {
		target := opts["target"]
		// Collection targets read keys(c), values(c), entries(c) or full(c).
		if i := strings.IndexByte(target, '('); i >= 0 {
			target = strings.TrimSuffix(target[i+1:], ")")
		}
		ti.indexes[strings.Trim(target, `"`)] = indexInfo{name, indexKind(kind, opts["class_name"], flavor)}
		opts = nil
	}
	return ti, iter.Close()
}

// classifyRead names the read a SELECT makes from the table's keys and indexes
// (docs/research-cassandra.md, 3): one partition, several, an index read, a
// token range or the whole table.
func classifyRead(ti *tableInfo, rs []restriction, allowFiltering bool) (string, []plan.Flag) {
	flags := []plan.Flag{}
	key := map[string]bool{}
	for _, c := range append(append([]string{}, ti.partition...), ti.clustering...) {
		key[c] = true
	}
	pinned := map[string]string{}
	var filtered, indexed []string
	for _, r := range rs {
		if r.token {
			return "Token range scan", append(flags, plan.Flag{Level: "warn", Code: "token_range",
				Message: "Reads partitions by token range: a scan over part of the ring, not a lookup"})
		}
		for _, c := range r.cols {
			switch {
			case slicesContain(ti.partition, c):
				if r.op == "=" || r.op == "in" {
					pinned[c] = r.op
				}
			case key[c]:
			case ti.indexes[c].name != "":
				indexed = append(indexed, c)
			default:
				filtered = append(filtered, c)
			}
		}
	}
	all, in := len(ti.partition) > 0, false
	for _, c := range ti.partition {
		all = all && pinned[c] != ""
		in = in || pinned[c] == "in"
	}
	partKey := strings.Join(ti.partition, ", ")
	if all {
		node := "Single-partition read (" + partKey + ")"
		if in {
			node = "Multi-partition read (IN on " + partKey + ")"
			flags = append(flags, plan.Flag{Level: "info", Code: "partition_in",
				Message: "IN on the partition key makes the coordinator read one partition per value; keep the list short"})
		}
		if len(filtered)+len(indexed) > 0 {
			flags = append(flags, plan.Flag{Level: "info", Code: "filter_in_partition",
				Message: "Filters rows of the partition on " + strings.Join(append(filtered, indexed...), ", ") + ": cheap only while partitions stay small"})
		}
		return node, flags
	}
	if len(indexed) > 0 {
		ix := ti.indexes[indexed[0]]
		node := fmt.Sprintf("Index read (%s %s)", ix.kind, ix.name)
		switch ix.kind {
		case "secondary index":
			flags = append(flags, plan.Flag{Level: "warn", Code: "legacy_index",
				Message: "A legacy secondary index is local to each node, so this read asks every node; SAI or a query table keyed for this read scale better"})
		case "global index":
			flags = append(flags, plan.Flag{Level: "info", Code: "global_index",
				Message: "Reads the index's hidden view, then the base table for each match"})
		}
		if len(filtered) > 0 || len(indexed) > 1 && ix.kind != "sai" {
			flags = append(flags, plan.Flag{Level: "warn", Code: "allow_filtering",
				Message: "Filters the index's matches on " + strings.Join(append(filtered, indexed[1:]...), ", ") + ", which needs ALLOW FILTERING"})
		}
		return node, flags
	}
	if len(filtered) == 0 && !allowFiltering {
		return "Full scan", append(flags, plan.Flag{Level: "warn", Code: "full_scan",
			Message: "No partition key (" + partKey + ") is given, so this reads every partition in the cluster"})
	}
	return "Full scan with filtering", append(flags, plan.Flag{Level: "danger", Code: "allow_filtering",
		Message: "Filters on " + strings.Join(filtered, ", ") + " without the partition key (" + partKey + "), which needs ALLOW FILTERING: it reads the whole table, and latency grows with its size"})
}

func slicesContain(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// planFor classifies the read, then asks the server to prepare the
// statement without running it: Cassandra and ScyllaDB decide whether ALLOW
// FILTERING is needed at prepare time.
func planFor(ctx context.Context, s *gocql.Session, keyspace string, toks []token) (*plan.Node, error) {
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}, StepsOnly: true}
	ks, table := target(toks)
	if table == "" {
		return root, nil
	}
	if ks == "" {
		ks = keyspace
	}
	n := &plan.Node{NodeType: "Read", Relation: ks + "." + table, Flags: []plan.Flag{}, Children: []*plan.Node{}}
	root.Children = append(root.Children, n)
	ti, err := loadTable(ctx, s, ks, table)
	if err != nil {
		return nil, err
	}
	if ti != nil {
		rs, af := restrictions(toks)
		n.NodeType, n.Flags = classifyRead(ti, rs, af)
		for _, r := range rs {
			if len(r.cols) == 1 && ti.indexes[r.cols[0]].name != "" && n.Index == "" && strings.HasPrefix(n.NodeType, "Index") {
				n.Index = ti.indexes[r.cols[0]].name
			}
		}
	}
	return root, nil
}

// serverVerdict prepares the statement; a refusal (ALLOW FILTERING needed, an
// unknown column) becomes a flag rather than an error, since nothing ran.
func serverVerdict(ctx context.Context, s *gocql.Session, keyspace, stmt string) *plan.Flag {
	_, err := s.StatementMetadata(ctx, stmt, keyspace)
	if err == nil {
		return nil
	}
	var re gocql.RequestError
	if errors.As(err, &re) {
		if strings.Contains(re.Message(), "ALLOW FILTERING") {
			return &plan.Flag{Level: "danger", Code: "server_refuses", Message: "The server refuses this query without ALLOW FILTERING: " + re.Message()}
		}
		return &plan.Flag{Level: "warn", Code: "server_refuses", Message: "The server would refuse this query: " + re.Message()}
	}
	return nil
}

// Run executes a SELECT that passed Check at LOCAL_ONE, returning at most
// maxRows rows; with explainOnly it only classifies and prepares it.
func Run(ctx context.Context, s *gocql.Session, keyspace, stmt string, maxRows int, explainOnly bool, timeout time.Duration) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stmt = strings.TrimRight(strings.TrimSpace(stmt), "; \n")
	res := &exec.Result{ExecutedSQL: stmt, Columns: []exec.Column{}, Rows: [][]json.RawMessage{}, Warnings: []string{}}
	p, err := planFor(ctx, s, keyspace, tokens(stmt))
	if err != nil {
		return nil, err
	}
	res.Plan = p
	if explainOnly {
		if f := serverVerdict(ctx, s, keyspace, stmt); f != nil && len(p.Children) > 0 {
			p.Children[0].Flags = append(p.Children[0].Flags, *f)
		}
		return res, nil
	}
	start := time.Now()
	iter := s.Query(stmt).Consistency(gocql.LocalOne).PageSize(min(maxRows+1, 1000)).IterContext(ctx)
	rd, err := iter.RowData()
	if err != nil {
		iter.Close()
		return nil, err
	}
	for _, c := range iter.Columns() {
		if tt, ok := c.TypeInfo.(gocql.TupleTypeInfo); ok {
			for i := range tt.Elems {
				res.Columns = append(res.Columns, exec.Column{Name: gocql.TupleColumnName(c.Name, i), Type: "tuple element"})
			}
			continue
		}
		res.Columns = append(res.Columns, exec.Column{Name: c.Name, Type: typeName(c.TypeInfo)})
	}
	// Scan into pointers to pointers so a null stays distinct from a zero value.
	dest := make([]any, len(rd.Values))
	for i, v := range rd.Values {
		dest[i] = reflect.New(reflect.TypeOf(v)).Interface()
	}
	sc := iter.Scanner()
	for len(res.Rows) <= maxRows && sc.Next() {
		if err := sc.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]json.RawMessage, len(dest))
		for i, d := range dest {
			row[i] = cell(reflect.ValueOf(d).Elem())
		}
		res.Rows = append(res.Rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(res.Rows) > maxRows {
		res.Rows, res.Truncated = res.Rows[:maxRows], true
	}
	res.RowCount = len(res.Rows)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	for _, w := range iter.Warnings() {
		res.Warnings = append(res.Warnings, w)
	}
	return res, nil
}

var typeNames = map[gocql.Type]string{gocql.TypeCustom: "custom", gocql.TypeAscii: "ascii", gocql.TypeBigInt: "bigint", gocql.TypeBlob: "blob",
	gocql.TypeBoolean: "boolean", gocql.TypeCounter: "counter", gocql.TypeDecimal: "decimal", gocql.TypeDouble: "double", gocql.TypeFloat: "float",
	gocql.TypeInt: "int", gocql.TypeText: "text", gocql.TypeTimestamp: "timestamp", gocql.TypeUUID: "uuid", gocql.TypeVarchar: "text",
	gocql.TypeVarint: "varint", gocql.TypeTimeUUID: "timeuuid", gocql.TypeInet: "inet", gocql.TypeDate: "date", gocql.TypeTime: "time",
	gocql.TypeSmallInt: "smallint", gocql.TypeTinyInt: "tinyint", gocql.TypeDuration: "duration", gocql.TypeUDT: "udt"}

// typeName is the CQL name of a result column's type; gocql v2 exports none
// (CollectionType.String prints type ids).
func typeName(ti gocql.TypeInfo) string {
	switch t := ti.(type) {
	case gocql.CollectionType:
		switch t.Type() {
		case gocql.TypeMap:
			return "map<" + typeName(t.Key) + ", " + typeName(t.Elem) + ">"
		case gocql.TypeList:
			return "list<" + typeName(t.Elem) + ">"
		case gocql.TypeSet:
			return "set<" + typeName(t.Elem) + ">"
		}
	case gocql.UDTTypeInfo:
		return t.Name
	case gocql.VectorType:
		return fmt.Sprintf("vector<%s, %d>", typeName(t.SubType), t.Dimensions)
	}
	if n, ok := typeNames[ti.Type()]; ok {
		return n
	}
	return fmt.Sprintf("type 0x%04x", int(ti.Type()))
}

const maxSafeInt = 1<<53 - 1

// cell renders a scanned value (a pointer that is nil for null) as JSON:
// integers JavaScript can hold exactly and finite floats as numbers;
// varint, decimal, UUIDs, blobs and times as text; collections as JSON text.
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
		n := v.Int()
		if n > maxSafeInt || n < -maxSafeInt {
			out = fmt.Sprint(n)
		} else {
			out = n
		}
	case float32, float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			out = fmt.Sprint(f)
		} else {
			out = f
		}
	case big.Int:
		out = x.String()
	case inf.Dec:
		out = x.String()
	case gocql.UUID:
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

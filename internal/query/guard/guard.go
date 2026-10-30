// Package guard decides whether a SQL string may run. Check parses it with
// libpg_query (compiled to WebAssembly, no cgo) and accepts exactly one
// SELECT that cannot write, lock rows or create tables. AllowFunctions then
// checks every called function against the live catalog.
//
// These checks give clear errors. They are not the only protection: the
// read-only transaction and the role's privileges are what enforce safety
// (docs/QUERY-SAFETY.md).
package guard

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"github.com/wasilibs/go-pgquery/parser"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/dbscope/dbscope/internal/cypherscan"
)

// Reason is the machine-readable cause of a rejection, as sent in the API
// error's detail field.
type Reason string

const (
	MultipleStatements Reason = "multiple_statements"
	NotSelect          Reason = "not_select"
	WritesData         Reason = "writes_data"
	LockingClause      Reason = "locking_clause"
	SelectInto         Reason = "select_into"
	FunctionNotAllowed Reason = "function_not_allowed"
	Syntax             Reason = "syntax"
)

// Error is a rejected statement. Position is the 1-based character offset of
// a syntax error in the checked SQL, or 0 when unknown.
type Error struct {
	Reason   Reason
	Message  string
	Position int
}

func (e *Error) Error() string { return e.Message }

func reject(r Reason, format string, args ...any) *Error {
	return &Error{Reason: r, Message: fmt.Sprintf(format, args...)}
}

// FuncName is a called function's name as written, with the schema only
// when the call was schema-qualified.
type FuncName struct {
	Schema string
	Name   string
}

func (f FuncName) String() string {
	if f.Schema == "" {
		return f.Name
	}
	return f.Schema + "." + f.Name
}

// Parsed is an accepted statement.
type Parsed struct {
	// SQL is the input as given.
	SQL string
	// Deparsed is the statement re-emitted from its parse tree: no comments
	// and no trailing semicolon. It is the text that runs.
	Deparsed string
	// Functions are the distinct functions the statement calls by name,
	// sorted. Calls through operators, casts and views are not included.
	Functions []FuncName
}

// Check accepts exactly one SELECT (including VALUES, TABLE and WITH ...
// SELECT) that contains no data-modifying statement, no SELECT INTO and no
// locking clause. The deparsed text is parsed and checked again, so what
// runs is what was checked.
func Check(sql string) (*Parsed, error) {
	tree, err := parse(sql)
	if err != nil {
		return nil, err
	}
	fns, cyphers, err := checkTree(tree)
	if err != nil {
		return nil, err
	}
	deparsed, err := deparse(tree)
	if err != nil {
		return nil, err
	}
	// Apache AGE takes cypher()'s query only as a dollar-quoted constant, which deparsing
	// turns into an ordinary one; quote it back, then check the result says the same.
	deparsed, ok := dollarQuote(deparsed, cyphers)
	again, againCyphers, err := checkText(deparsed)
	if !ok || err != nil || !slices.Equal(fns, again) || !slices.Equal(cyphers, againCyphers) {
		return nil, reject(Syntax, "dbscope could not re-create this statement exactly; please simplify it")
	}
	return &Parsed{SQL: sql, Deparsed: deparsed, Functions: fns}, nil
}

// dollarQuote rewrites each cypher() query, in order, from a quoted
// string to a dollar-quoted one with a tag the query does not contain.
func dollarQuote(sql string, cyphers []string) (string, bool) {
	from := 0
	for _, q := range cyphers {
		lit := "'" + strings.ReplaceAll(q, "'", "''") + "'"
		i := strings.Index(sql[from:], lit)
		if i < 0 {
			return sql, false
		}
		tag := "$cypher$"
		for n := 1; strings.Contains(q, tag); n++ {
			tag = fmt.Sprintf("$cypher%d$", n)
		}
		quoted := tag + q + tag
		sql = sql[:from+i] + quoted + sql[from+i+len(lit):]
		from += i + len(quoted)
	}
	return sql, true
}

var warmOnce sync.Once

// Warm loads the WebAssembly parser in the background, once per process;
// the first parse otherwise takes about 600 ms.
func Warm() {
	warmOnce.Do(func() { go parse("SELECT 1") })
}

// checkText parses sql and runs the structural checks, returning the
// functions it calls.
func checkText(sql string) ([]FuncName, []string, error) {
	tree, err := parse(sql)
	if err != nil {
		return nil, nil, err
	}
	return checkTree(tree)
}

// checkTree also returns the Cypher of each cypher() call, in order.
func checkTree(tree *pg_query.ParseResult) ([]FuncName, []string, error) {
	switch n := len(tree.Stmts); {
	case n == 0:
		return nil, nil, reject(NotSelect, "the query is empty")
	case n > 1:
		return nil, nil, reject(MultipleStatements, "only one statement can run at a time; this has %d", n)
	}
	stmt := tree.Stmts[0].GetStmt()
	if stmt.GetSelectStmt() == nil {
		return nil, nil, notSelect(nodeName(stmt))
	}
	w := walker{fns: map[FuncName]bool{}}
	if err := w.walk(stmt.ProtoReflect()); err != nil {
		return nil, nil, err
	}
	out := make([]FuncName, 0, len(w.fns))
	for f := range w.fns {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b FuncName) int { return strings.Compare(a.String(), b.String()) })
	return out, w.cyphers, nil
}

// parse wraps the WebAssembly parser, turning its errors (and any panic
// from the runtime) into syntax rejections.
func parse(sql string) (tree *pg_query.ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			tree, err = nil, reject(Syntax, "the SQL parser failed on this input")
		}
	}()
	tree, err = pgquery.Parse(sql)
	if err == nil {
		return tree, nil
	}
	var pe *parser.Error
	if errors.As(err, &pe) {
		return nil, &Error{Reason: Syntax, Message: pe.Message, Position: pe.Cursorpos}
	}
	return nil, reject(Syntax, "the SQL parser failed: %v", err)
}

func deparse(tree *pg_query.ParseResult) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = "", reject(Syntax, "dbscope could not re-create this statement; please simplify it")
		}
	}()
	out, err = pgquery.Deparse(tree)
	if err != nil {
		return "", reject(Syntax, "dbscope could not re-create this statement: %v", err)
	}
	return out, nil
}

// nodeName is the message type held by a Node, e.g. "CopyStmt".
func nodeName(n *pg_query.Node) string {
	m := n.ProtoReflect()
	od := m.Descriptor().Oneofs().ByName("node")
	if od == nil {
		return "unknown"
	}
	f := m.WhichOneof(od)
	if f == nil {
		return "unknown"
	}
	return string(f.Message().Name())
}

var writeStmts = map[string]string{
	"InsertStmt": "INSERT", "UpdateStmt": "UPDATE", "DeleteStmt": "DELETE", "MergeStmt": "MERGE",
}

var stmtLabels = map[string]string{
	"CopyStmt": "COPY", "DoStmt": "DO", "CallStmt": "CALL", "VariableSetStmt": "SET/RESET",
	"VariableShowStmt": "SHOW", "TransactionStmt": "transaction control", "ExplainStmt": "EXPLAIN",
	"CreateTableAsStmt": "CREATE TABLE AS", "DeclareCursorStmt": "DECLARE CURSOR", "FetchStmt": "FETCH",
	"ClosePortalStmt": "CLOSE", "PrepareStmt": "PREPARE", "ExecuteStmt": "EXECUTE",
	"DeallocateStmt": "DEALLOCATE", "ListenStmt": "LISTEN", "UnlistenStmt": "UNLISTEN",
	"NotifyStmt": "NOTIFY", "LockStmt": "LOCK", "LoadStmt": "LOAD", "VacuumStmt": "VACUUM/ANALYZE",
	"DiscardStmt": "DISCARD", "CheckPointStmt": "CHECKPOINT", "ReindexStmt": "REINDEX",
	"CreateStmt": "CREATE TABLE", "DropStmt": "DROP", "TruncateStmt": "TRUNCATE",
	"GrantStmt": "GRANT/REVOKE", "GrantRoleStmt": "GRANT/REVOKE role", "RefreshMatViewStmt": "REFRESH MATERIALIZED VIEW",
}

func notSelect(name string) *Error {
	if verb, ok := writeStmts[name]; ok {
		return reject(WritesData, "%s modifies data; only SELECT statements can run", verb)
	}
	label, ok := stmtLabels[name]
	if !ok {
		label = camelWords(strings.TrimSuffix(name, "Stmt"))
	}
	return reject(NotSelect, "only SELECT statements can run, not %s", label)
}

// camelWords turns "AlterTable" into "ALTER TABLE".
func camelWords(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// walker visits every message in the tree through protobuf reflection, so
// a node type added to a future grammar is still visited.
type walker struct {
	fns     map[FuncName]bool
	cyphers []string
}

func (w *walker) walk(m protoreflect.Message) error {
	name := string(m.Descriptor().Name())
	switch {
	case name == "SelectStmt":
		s := m.Interface().(*pg_query.SelectStmt)
		if s.IntoClause != nil {
			return reject(SelectInto, "SELECT INTO creates a table; remove the INTO clause")
		}
		if len(s.LockingClause) > 0 {
			return reject(LockingClause, "%s locks rows; remove the locking clause", lockingLabel(s.LockingClause[0]))
		}
	case name == "FuncCall":
		fc := m.Interface().(*pg_query.FuncCall)
		w.addFunc(fc.Funcname)
		q, err := checkCypherCall(fc)
		if err != nil {
			return err
		}
		if q != nil {
			w.cyphers = append(w.cyphers, *q)
		}
	case strings.HasSuffix(name, "Stmt"):
		// Any statement nested in a SELECT (e.g. a data-modifying CTE).
		return notSelect(name)
	}
	var err error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true
		}
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len() && err == nil; i++ {
				err = w.walk(l.Get(i).Message())
			}
		case fd.IsMap():
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				if fd.MapValue().Kind() == protoreflect.MessageKind {
					err = w.walk(mv.Message())
				}
				return err == nil
			})
		default:
			err = w.walk(v.Message())
		}
		return err == nil
	})
	return err
}

func lockingLabel(n *pg_query.Node) string {
	switch n.GetLockingClause().GetStrength() {
	case pg_query.LockClauseStrength_LCS_FORKEYSHARE:
		return "FOR KEY SHARE"
	case pg_query.LockClauseStrength_LCS_FORSHARE:
		return "FOR SHARE"
	case pg_query.LockClauseStrength_LCS_FORNOKEYUPDATE:
		return "FOR NO KEY UPDATE"
	default:
		return "FOR UPDATE"
	}
}

// checkCypherCall vets Apache AGE's cypher(graph, query): it runs Cypher
// that the SQL parser sees only as a string, so the string must be a
// constant and must not write. The read-only transaction refuses AGE's
// writes too (verified on AGE 1.8); this keeps the check before the run,
// as for every other function. AllowFunctions then admits only the
// extension's own ag_catalog.cypher, which is VOLATILE.
func checkCypherCall(fc *pg_query.FuncCall) (*string, error) {
	parts := fc.Funcname
	if len(parts) == 0 || parts[len(parts)-1].GetString_().GetSval() != "cypher" {
		return nil, nil
	}
	if len(fc.Args) < 2 {
		return nil, reject(FunctionNotAllowed, "cypher() needs a graph name and its Cypher query as string constants")
	}
	query := fc.Args[1].GetAConst().GetSval()
	if query == nil || fc.Args[0].GetAConst().GetSval() == nil {
		return nil, reject(FunctionNotAllowed, "cypher() runs only with the graph name and query written as string constants, such as $$ MATCH (n) RETURN n $$")
	}
	if clause := cypherscan.WriteClause(cypherscan.Tokens(query.GetSval())); clause != "" {
		return nil, reject(WritesData, "the Cypher in cypher() uses %s, which changes the graph; only reading Cypher runs here", clause)
	}
	q := query.GetSval()
	return &q, nil
}

// addFunc records a FuncCall's name. Names arrive as String nodes, already
// case-folded by the parser; a three-part name keeps its last two parts
// (PostgreSQL rejects cross-database references itself).
func (w *walker) addFunc(parts []*pg_query.Node) {
	var names []string
	for _, p := range parts {
		names = append(names, p.GetString_().GetSval())
	}
	var f FuncName
	switch len(names) {
	case 0:
		return
	case 1:
		f.Name = names[0]
	default:
		f.Schema, f.Name = names[len(names)-2], names[len(names)-1]
	}
	w.fns[f] = true
}

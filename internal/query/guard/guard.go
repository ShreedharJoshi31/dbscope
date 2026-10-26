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
	fns, err := checkTree(tree)
	if err != nil {
		return nil, err
	}
	deparsed, err := deparse(tree)
	if err != nil {
		return nil, err
	}
	again, err := checkText(deparsed)
	if err != nil || !slices.Equal(fns, again) {
		return nil, reject(Syntax, "dbscope could not re-create this statement exactly; please simplify it")
	}
	return &Parsed{SQL: sql, Deparsed: deparsed, Functions: fns}, nil
}

var warmOnce sync.Once

// Warm loads the WebAssembly parser in the background, once per process;
// the first parse otherwise takes about 600 ms.
func Warm() {
	warmOnce.Do(func() { go parse("SELECT 1") })
}

// checkText parses sql and runs the structural checks, returning the
// functions it calls.
func checkText(sql string) ([]FuncName, error) {
	tree, err := parse(sql)
	if err != nil {
		return nil, err
	}
	return checkTree(tree)
}

func checkTree(tree *pg_query.ParseResult) ([]FuncName, error) {
	switch n := len(tree.Stmts); {
	case n == 0:
		return nil, reject(NotSelect, "the query is empty")
	case n > 1:
		return nil, reject(MultipleStatements, "only one statement can run at a time; this has %d", n)
	}
	stmt := tree.Stmts[0].GetStmt()
	if stmt.GetSelectStmt() == nil {
		return nil, notSelect(nodeName(stmt))
	}
	w := walker{fns: map[FuncName]bool{}}
	if err := w.walk(stmt.ProtoReflect()); err != nil {
		return nil, err
	}
	out := make([]FuncName, 0, len(w.fns))
	for f := range w.fns {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b FuncName) int { return strings.Compare(a.String(), b.String()) })
	return out, nil
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
	fns map[FuncName]bool
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
		w.addFunc(m.Interface().(*pg_query.FuncCall).Funcname)
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

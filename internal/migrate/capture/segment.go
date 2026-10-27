package capture

import (
	"fmt"
	"regexp"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/dbscope/dbscope/internal/migrate/parse"
)

// Framework names the migration tool whose bookkeeping writes were seen.
type Framework string

const (
	Rails   Framework = "rails"
	Django  Framework = "django"
	Alembic Framework = "alembic"
	Drizzle Framework = "drizzle"
	Prisma  Framework = "prisma"
)

// Migration is the SQL one or more migrations ran. Several migrations share
// one Migration when a single transaction spans them (Drizzle, Alembic's
// default), because their locks are held together.
type Migration struct {
	Framework Framework
	// Keys identify each migration the way its bookkeeping table does
	// (Rails version, Django "app.name", Alembic revision, Drizzle created_at
	// or name, Prisma migration_name).
	Keys []string
	SQL  []string
	// Partial is set when parameters could not be rendered as literals.
	Partial bool
}

// Result is a segmented capture.
type Result struct {
	Migrations []Migration
	// Unrecorded holds statements after the last bookkeeping write, or all of
	// them when no framework was recognised.
	Unrecorded *Migration
	Failed     []Event
	Dropped    int
	RolledBack int
}

var bookkeeping = regexp.MustCompile(`(?i)\b(schema_migrations|ar_internal_metadata|django_migrations|alembic_version|__drizzle_migrations|_prisma_migrations)\b|create\s+schema\s+if\s+not\s+exists\s+"?drizzle"?`)

// Settings that change what a later statement locks, how long it waits, or
// which table a name means; other SETs are connection setup.
var keptSettings = map[string]bool{"lock_timeout": true, "statement_timeout": true, "search_path": true}

// Functions a FROM-less SELECT calls to read state or take session locks. A
// FROM-less SELECT of anything else (setval, create_hypertable, ...) changes
// the database and is kept.
var readOnlyCalls = map[string]bool{
	"pg_advisory_lock": true, "pg_try_advisory_lock": true, "pg_advisory_unlock": true,
	"pg_advisory_unlock_all": true, "pg_advisory_xact_lock": true, "pg_try_advisory_xact_lock": true,
	"version": true, "current_schema": true, "current_schemas": true, "current_setting": true,
	"current_database": true, "current_user": true, "pg_backend_pid": true, "pg_is_in_recovery": true,
	"now": true, "pg_postmaster_start_time": true, "inet_server_addr": true, "inet_server_port": true,
}

type kind int

const (
	keep    kind = iota
	setting      // a kept SET: it matters only to statements after it
	drop
	begin
	commit
	rollback
	savepoint
	release
	rollbackTo
	marker
)

type line struct {
	sql  string
	kind kind
}

type save struct {
	name      string
	buf, keys int
}

// connState is one connection's statements since its last migration ended.
// Connections are segmented separately so their transactions cannot mix.
type connState struct {
	buf       []line
	keys      []string
	fw        Framework
	partial   bool
	open      bool // a transaction is open
	begin     int  // index of its BEGIN in buf
	beginKeys int
	implicit  bool // the open transaction is a multi-statement query's
	saves     []save
}

// Segment turns recorded events into migrations.
func Segment(events []Event) Result {
	var res Result
	wrap := implicitUnits(events)
	conns := map[int]*connState{}
	var order []int
	prisma := map[string]string{} // _prisma_migrations id -> migration_name

	flush := func(c *connState) {
		res.Migrations = append(res.Migrations, Migration{Framework: c.fw, Keys: c.keys, SQL: clean(c.buf), Partial: c.partial})
		c.buf, c.keys, c.partial = nil, nil, false
	}
	unitLast := map[[2]int]int{}
	for i, e := range events {
		if e.Error == "" {
			unitLast[[2]int{e.Conn, e.Unit}] = i
		}
	}

	for i, e := range events {
		c := conns[e.Conn]
		if c == nil {
			c = &connState{}
			conns[e.Conn] = c
			order = append(order, e.Conn)
		}
		u := [2]int{e.Conn, e.Unit}
		sql, ok := Render(e.SQL, e.Params)
		k, f, key := classify(sql, e.Params, prisma)
		// A COMMIT that failed or was answered with ROLLBACK, and a connection
		// that closed mid-transaction, all end in a rollback.
		if (k == commit && (e.Error != "" || e.Tag == "ROLLBACK")) || (k == rollback && e.Error != "") {
			k = rollback
		} else if e.Error != "" {
			res.Failed = append(res.Failed, e)
			continue
		}
		if wrap[u] && !c.open && (k == keep || k == setting || k == marker) {
			c.buf = append(c.buf, line{"-- One multi-statement query: PostgreSQL ran it as one transaction.\nBEGIN", begin})
			c.open, c.begin, c.beginKeys, c.implicit = true, len(c.buf)-1, len(c.keys), true
		}
		switch k {
		case drop:
			res.Dropped++
		case keep, setting:
			c.buf = append(c.buf, line{sql, k})
			c.partial = c.partial || !ok
		case begin:
			c.buf = append(c.buf, line{sql, begin})
			c.open, c.begin, c.beginKeys, c.saves = true, len(c.buf)-1, len(c.keys), nil
		case savepoint:
			if c.open {
				c.saves = append(c.saves, save{key, len(c.buf), len(c.keys)})
			}
		case release, rollbackTo:
			j := len(c.saves) - 1
			for j >= 0 && c.saves[j].name != key {
				j--
			}
			if j < 0 {
				break
			}
			if k == release {
				c.saves = c.saves[:j]
				break
			}
			sp := c.saves[j]
			res.RolledBack += len(c.buf) - sp.buf
			c.buf, c.keys, c.saves = c.buf[:sp.buf], c.keys[:sp.keys], c.saves[:j+1]
		case commit, rollback:
			if !c.open {
				break
			}
			if k == rollback {
				res.RolledBack += len(c.buf) - c.begin - 1
				c.buf, c.keys = c.buf[:c.begin], c.keys[:c.beginKeys]
			} else {
				c.buf = append(c.buf, line{sql, commit})
			}
			c.open, c.implicit, c.saves = false, false, nil
			if len(c.keys) > 0 {
				flush(c)
			}
		case marker:
			c.fw = f
			c.keys = append(c.keys, key)
			if !c.open {
				flush(c)
			}
		}
		if c.implicit && unitLast[u] == i {
			c.buf = append(c.buf, line{"COMMIT", commit})
			c.open, c.implicit = false, false
			if len(c.keys) > 0 {
				flush(c)
			}
		}
	}
	for _, id := range order {
		c := conns[id]
		sql := clean(c.buf)
		if len(sql) == 0 {
			continue
		}
		if res.Unrecorded == nil {
			res.Unrecorded = &Migration{}
		}
		res.Unrecorded.Framework = c.fw
		res.Unrecorded.Keys = append(res.Unrecorded.Keys, c.keys...)
		res.Unrecorded.SQL = append(res.Unrecorded.SQL, sql...)
		res.Unrecorded.Partial = res.Unrecorded.Partial || c.partial
	}
	return res
}

// implicitUnits finds Query messages and Sync batches that ran several
// statements with no transaction open and no transaction control of their
// own: PostgreSQL wraps those in one implicit transaction.
func implicitUnits(events []Event) map[[2]int]bool {
	count := map[[2]int]int{}
	ctl := map[[2]int]bool{}
	inTx := map[[2]int]bool{}
	for _, e := range events {
		u := [2]int{e.Conn, e.Unit}
		count[u]++
		if e.InTx {
			inTx[u] = true
		}
		if f := parse.Split(e.SQL); len(f.Statements) == 1 && f.Statements[0].Node.GetTransactionStmt() != nil {
			ctl[u] = true
		}
	}
	out := map[[2]int]bool{}
	for u, n := range count {
		if n > 1 && !ctl[u] && !inTx[u] {
			out[u] = true
		}
	}
	return out
}

// clean drops transactions that ended up with nothing in them.
func clean(buf []line) []string {
	last := -1
	for i, l := range buf {
		if l.kind == keep {
			last = i
		}
	}
	var out []string
	var kinds []kind
	for i, l := range buf {
		if l.kind == setting && i > last {
			continue // nothing after it for the setting to affect
		}
		if l.kind == commit && len(kinds) > 0 && kinds[len(kinds)-1] == begin {
			out, kinds = out[:len(out)-1], kinds[:len(kinds)-1]
			continue
		}
		out, kinds = append(out, l.sql), append(kinds, l.kind)
	}
	if len(kinds) > 0 && kinds[len(kinds)-1] == begin {
		out = out[:len(out)-1]
	}
	return out
}

func classify(sql string, ps []Param, prisma map[string]string) (k kind, fw Framework, key string) {
	f := parse.Split(sql)
	switch {
	case len(f.Statements) == 0:
		return drop, "", "" // only comments, e.g. a driver's ping
	case len(f.Statements) != 1 || f.Statements[0].Node == nil:
		// Ran on the server but the bundled grammar cannot read it; keep it so
		// the analysis reports it.
		if bookkeeping.MatchString(sql) {
			return drop, "", ""
		}
		return keep, "", ""
	}
	n := f.Statements[0].Node
	if t := n.GetTransactionStmt(); t != nil {
		switch t.GetKind() {
		case pg_query.TransactionStmtKind_TRANS_STMT_BEGIN, pg_query.TransactionStmtKind_TRANS_STMT_START:
			return begin, "", ""
		case pg_query.TransactionStmtKind_TRANS_STMT_COMMIT:
			return commit, "", ""
		case pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK:
			return rollback, "", ""
		case pg_query.TransactionStmtKind_TRANS_STMT_SAVEPOINT:
			return savepoint, "", t.GetSavepointName()
		case pg_query.TransactionStmtKind_TRANS_STMT_RELEASE:
			return release, "", t.GetSavepointName()
		case pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK_TO:
			return rollbackTo, "", t.GetSavepointName()
		}
		return drop, "", ""
	}
	if fw, key, ok := markerOf(n, ps, prisma); ok {
		return marker, fw, key
	}
	switch {
	case n.GetSelectStmt() != nil && n.GetSelectStmt().GetIntoClause() == nil:
		return selectKind(n.GetSelectStmt()), "", ""
	case n.GetVariableShowStmt() != nil, n.GetExplainStmt() != nil,
		n.GetDiscardStmt() != nil, n.GetDeallocateStmt() != nil, n.GetPrepareStmt() != nil,
		n.GetListenStmt() != nil, n.GetUnlistenStmt() != nil, n.GetNotifyStmt() != nil:
		return drop, "", ""
	case n.GetVariableSetStmt() != nil:
		if keptSettings[strings.ToLower(n.GetVariableSetStmt().GetName())] {
			return setting, "", ""
		}
		return drop, "", ""
	}
	if bookkeeping.MatchString(sql) {
		return drop, "", ""
	}
	return keep, "", ""
}

// selectKind tells a SELECT that is only a call with side effects apart from
// a query: no FROM, and a top-level function that is not a read or a session
// lock. set_config of a kept setting is a setting; of anything else, setup.
func selectKind(sel *pg_query.SelectStmt) kind {
	if len(sel.GetFromClause()) > 0 {
		return drop
	}
	k := drop
	for _, t := range sel.GetTargetList() {
		fc := t.GetResTarget().GetVal().GetFuncCall()
		if fc == nil {
			continue
		}
		parts := fc.GetFuncname()
		name := strings.ToLower(parts[len(parts)-1].GetString_().GetSval())
		switch {
		case name == "set_config":
			if args := fc.GetArgs(); len(args) > 0 {
				if v, ok := value(args[0], nil); ok && keptSettings[strings.ToLower(v)] && k == drop {
					k = setting
				}
			}
		case !readOnlyCalls[name]:
			return keep
		}
	}
	return k
}

// markerOf recognises the write that records a migration as applied.
func markerOf(n *pg_query.Node, ps []Param, prisma map[string]string) (Framework, string, bool) {
	if ins := n.GetInsertStmt(); ins != nil {
		cols := insertValues(ins, ps)
		switch ins.GetRelation().GetRelname() {
		case "schema_migrations":
			return Rails, cols["version"], cols["version"] != ""
		case "django_migrations":
			return Django, cols["app"] + "." + cols["name"], cols["name"] != ""
		case "alembic_version":
			return Alembic, cols["version_num"], cols["version_num"] != ""
		case "__drizzle_migrations":
			if cols["name"] != "" {
				return Drizzle, cols["name"], true
			}
			return Drizzle, cols["created_at"], cols["created_at"] != ""
		case "_prisma_migrations":
			// Written before the script runs; the finished_at update below ends it.
			if cols["id"] != "" {
				prisma[cols["id"]] = cols["migration_name"]
			}
		}
		return "", "", false
	}
	up := n.GetUpdateStmt()
	if up == nil {
		return "", "", false
	}
	set := map[string]*pg_query.Node{}
	for _, t := range up.GetTargetList() {
		set[t.GetResTarget().GetName()] = t.GetResTarget().GetVal()
	}
	switch up.GetRelation().GetRelname() {
	case "alembic_version":
		if v, ok := value(set["version_num"], ps); ok {
			return Alembic, v, true
		}
	case "_prisma_migrations":
		if _, ok := set["finished_at"]; ok {
			if id, ok := whereEquals(up.GetWhereClause(), "id", ps); ok {
				name := prisma[id]
				if name == "" {
					name = id
				}
				return Prisma, name, true
			}
		}
	}
	return "", "", false
}

func insertValues(ins *pg_query.InsertStmt, ps []Param) map[string]string {
	out := map[string]string{}
	rows := ins.GetSelectStmt().GetSelectStmt().GetValuesLists()
	if len(rows) != 1 {
		return out
	}
	vals := rows[0].GetList().GetItems()
	for i, c := range ins.GetCols() {
		if i < len(vals) {
			if v, ok := value(vals[i], ps); ok {
				out[c.GetResTarget().GetName()] = v
			}
		}
	}
	return out
}

// value reads a constant, a cast constant, or a bound parameter.
func value(n *pg_query.Node, ps []Param) (string, bool) {
	switch {
	case n == nil:
		return "", false
	case n.GetTypeCast() != nil:
		return value(n.GetTypeCast().GetArg(), ps)
	case n.GetAConst() != nil:
		c := n.GetAConst()
		switch {
		case c.GetSval() != nil:
			return c.GetSval().GetSval(), true
		case c.GetIval() != nil:
			return fmt.Sprint(c.GetIval().GetIval()), true
		}
	case n.GetParamRef() != nil:
		i := int(n.GetParamRef().GetNumber()) - 1
		if i >= 0 && i < len(ps) {
			return ps[i].Text()
		}
	}
	return "", false
}

func whereEquals(n *pg_query.Node, col string, ps []Param) (string, bool) {
	e := n.GetAExpr()
	if e == nil {
		for _, a := range n.GetBoolExpr().GetArgs() {
			if v, ok := whereEquals(a, col, ps); ok {
				return v, true
			}
		}
		return "", false
	}
	fields := e.GetLexpr().GetColumnRef().GetFields()
	if len(fields) > 0 && fields[len(fields)-1].GetString_().GetSval() == col {
		return value(e.GetRexpr(), ps)
	}
	return "", false
}

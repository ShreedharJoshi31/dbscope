// Package timeouts tracks which lock_timeout and statement_timeout are in
// effect for each statement of a migration, the way Eugene's lint context
// does (github.com/kaaveland/eugene): it follows SET, SET LOCAL, RESET,
// set_config(), BEGIN/COMMIT/ROLLBACK and savepoints.
//
// PostgreSQL semantics modelled (SET and runtime-config-client docs):
//   - SET inside a transaction is undone if the transaction rolls back, and
//     kept after COMMIT;
//   - SET LOCAL lasts until the end of the transaction; outside a transaction
//     block it has no effect (PostgreSQL only warns);
//   - ROLLBACK TO SAVEPOINT undoes settings made after the savepoint;
//   - RESET and SET ... TO DEFAULT return to the role/database default, which
//     is the "external" value (pg_db_role_setting, or the deploy tool's own
//     lock_timeout when assumeLockTimeout is set).
//
// Each file starts from the external defaults: dbscope does not assume a
// SET in one migration file carries over to the next, since tools may run
// files on different connections (squawk applies the same file-level rule).
package timeouts

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/rules"
)

// tracked are the settings the machine follows.
var tracked = []string{"lock_timeout", "statement_timeout"}

// defaultValue marks "back to the role/database default".
const defaultValue = "\x00default"

type scope map[string]string

func (s scope) clone() scope {
	out := scope{}
	for k, v := range s {
		out[k] = v
	}
	return out
}

type savepoint struct {
	name      string
	txSession scope
	local     scope
}

// Machine is the setting state of one session.
type Machine struct {
	external scope
	session  scope // committed session-level values
	pending  scope // session-level SETs inside the open transaction
	local    scope // SET LOCAL values of the open transaction
	explicit bool  // an explicit BEGIN is open
	wrapped  bool  // the file runs inside an implicit or framework transaction
	saves    []savepoint
}

// External is a timeout configured outside the migration files.
type External struct {
	// LockTimeout is the role or database lock_timeout ("" or "0" = none).
	LockTimeout string
	// Assume: the deploy tool sets a lock_timeout itself.
	Assume bool
}

// New starts a machine with the given external defaults.
func New(ext External) *Machine {
	m := &Machine{external: scope{"lock_timeout": ext.LockTimeout, "statement_timeout": ""}}
	if ext.Assume && !enabled(ext.LockTimeout) {
		m.external["lock_timeout"] = "assumed"
	}
	m.reset()
	return m
}

func (m *Machine) reset() {
	m.session, m.pending, m.local = scope{}, scope{}, scope{}
	m.explicit, m.wrapped, m.saves = false, false, nil
}

// StartFile begins a migration file; wrapped means the tool runs the whole
// file in one transaction (implicit multi-statement query or framework).
func (m *Machine) StartFile(wrapped bool) {
	m.reset()
	m.wrapped = wrapped
}

// State is the setting state in effect for one statement.
type State struct {
	LockTimeout      bool
	LockTimeoutValue string // "" when none; "assumed" for assumeLockTimeout
	StatementTimeout string
	// InTransaction reports whether a transaction block (explicit or wrapping) is open.
	InTransaction bool
}

func (m *Machine) inTx() bool { return m.explicit || m.wrapped }

func (m *Machine) value(name string) string {
	for _, s := range []scope{m.local, m.pending, m.session} {
		if v, ok := s[name]; ok {
			if v == defaultValue {
				return m.external[name]
			}
			return v
		}
	}
	return m.external[name]
}

// enabled reports whether a timeout value is non-zero.
func enabled(v string) bool {
	v = strings.TrimSpace(strings.Trim(v, "'"))
	if v == "" {
		return false
	}
	n := strings.TrimRight(v, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ ")
	if f, err := strconv.ParseFloat(n, 64); err == nil {
		return f > 0
	}
	return true // "assumed" or an unparseable non-empty value
}

// Current is the state for the next statement, before it runs.
func (m *Machine) Current() State {
	lt := m.value("lock_timeout")
	st := State{LockTimeout: enabled(lt), StatementTimeout: m.value("statement_timeout"), InTransaction: m.inTx()}
	if st.LockTimeout {
		st.LockTimeoutValue = lt
	}
	return st
}

// Step returns the state in effect for n and then applies n's effect on the
// settings. It may return findings about n itself (SET LOCAL outside a
// transaction).
func (m *Machine) Step(n *pg_query.Node) (State, []rules.Finding) {
	st := m.Current()
	var out []rules.Finding
	switch {
	case n.GetVariableSetStmt() != nil:
		out = m.set(n.GetVariableSetStmt())
	case n.GetTransactionStmt() != nil:
		m.transaction(n.GetTransactionStmt())
	case n.GetSelectStmt() != nil:
		out = m.setConfig(n.GetSelectStmt())
	}
	return st, out
}

func (m *Machine) set(v *pg_query.VariableSetStmt) []rules.Finding {
	names := []string{v.GetName()}
	val := defaultValue
	switch v.GetKind() {
	case pg_query.VariableSetKind_VAR_SET_VALUE:
		val = argValue(v.GetArgs())
	case pg_query.VariableSetKind_VAR_SET_DEFAULT, pg_query.VariableSetKind_VAR_RESET:
	case pg_query.VariableSetKind_VAR_RESET_ALL:
		names = tracked
	default:
		return nil
	}
	if !slices.Contains(tracked, names[0]) {
		return nil
	}
	return m.assign(names, val, v.GetIsLocal(), "SET LOCAL")
}

func (m *Machine) assign(names []string, val string, local bool, verb string) []rules.Finding {
	switch {
	case local && !m.inTx():
		return []rules.Finding{{ID: "DBS-SET-LOCAL-NO-TX", Class: rules.Info,
			Message: fmt.Sprintf("%s %s outside a transaction has no effect on later statements", verb, strings.Join(names, ", ")),
			Safe:    "Use SET (session level), or run the migration inside BEGIN ... COMMIT"}}
	case local:
		for _, n := range names {
			m.local[n] = val
		}
	case m.inTx():
		for _, n := range names {
			m.pending[n] = val
			delete(m.local, n) // SET overrides an earlier SET LOCAL in the same transaction
		}
	default:
		for _, n := range names {
			m.session[n] = val
		}
	}
	return nil
}

// setConfig handles SELECT set_config('lock_timeout', '2s', is_local).
func (m *Machine) setConfig(sel *pg_query.SelectStmt) []rules.Finding {
	var out []rules.Finding
	for _, t := range sel.GetTargetList() {
		fc := t.GetResTarget().GetVal().GetFuncCall()
		if fc == nil {
			continue
		}
		if _, name := apply.QualifiedFromList(fc.GetFuncname()); name != "set_config" || len(fc.GetArgs()) != 3 {
			continue
		}
		args := fc.GetArgs()
		setting := args[0].GetAConst().GetSval().GetSval()
		if !slices.Contains(tracked, setting) {
			continue
		}
		val := argValue(args[1:2])
		local := args[2].GetAConst().GetBoolval().GetBoolval()
		out = append(out, m.assign([]string{setting}, val, local, "set_config(..., true)")...)
	}
	return out
}

func argValue(args []*pg_query.Node) string {
	if len(args) == 0 {
		return ""
	}
	c := args[0].GetAConst()
	switch {
	case c == nil:
		return "unknown"
	case c.GetSval() != nil:
		return c.GetSval().GetSval()
	case c.GetIval() != nil:
		return strconv.Itoa(int(c.GetIval().GetIval())) + "ms"
	case c.GetFval() != nil:
		return c.GetFval().GetFval() + "ms"
	}
	return "unknown"
}

func (m *Machine) transaction(t *pg_query.TransactionStmt) {
	switch t.GetKind() {
	case pg_query.TransactionStmtKind_TRANS_STMT_BEGIN, pg_query.TransactionStmtKind_TRANS_STMT_START:
		if !m.inTx() {
			m.explicit = true
		}
	case pg_query.TransactionStmtKind_TRANS_STMT_COMMIT:
		m.commit()
	case pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK:
		m.pending, m.local, m.saves = scope{}, scope{}, nil
		m.explicit, m.wrapped = false, false
	case pg_query.TransactionStmtKind_TRANS_STMT_SAVEPOINT:
		if m.inTx() {
			m.saves = append(m.saves, savepoint{name: t.GetSavepointName(), txSession: m.pending.clone(), local: m.local.clone()})
		}
	case pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK_TO:
		for i := len(m.saves) - 1; i >= 0; i-- {
			if m.saves[i].name == t.GetSavepointName() {
				m.pending, m.local = m.saves[i].txSession.clone(), m.saves[i].local.clone()
				m.saves = m.saves[:i+1]
				break
			}
		}
	case pg_query.TransactionStmtKind_TRANS_STMT_RELEASE:
		for i := len(m.saves) - 1; i >= 0; i-- {
			if m.saves[i].name == t.GetSavepointName() {
				m.saves = m.saves[:i]
				break
			}
		}
	}
}

func (m *Machine) commit() {
	for k, v := range m.pending {
		m.session[k] = v
	}
	m.pending, m.local, m.saves = scope{}, scope{}, nil
	m.explicit, m.wrapped = false, false
}

// EndFile commits a wrapping transaction (settings made in it are then lost
// with the per-file reset anyway).
func (m *Machine) EndFile() {
	if m.wrapped {
		m.commit()
	}
}

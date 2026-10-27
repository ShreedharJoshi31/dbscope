package workload

import (
	"slices"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/schema"
)

// Path is the connection's search_path and, for each of its schemas, the
// relation names the database has there, so an unqualified name resolves as
// PostgreSQL would for this connection's role.
type Path struct {
	Schemas []string
	// Names maps schema -> relation names in it; nil means unknown, and names
	// then resolve against the model alone.
	Names map[string]map[string]bool
}

// Attribute ties each statement to the tables and columns of m it uses.
// Only queries and data changes count (SELECT, INSERT, UPDATE, DELETE,
// MERGE); utility statements (COPY, VACUUM, EXPLAIN, DECLARE, DDL) are not
// tied to a table, and statements that read pg_stat_statements are skipped.
func Attribute(s *Snapshot, m *schema.Model, path Path, stmts []Statement) {
	for _, st := range stmts {
		if st.Hidden {
			continue // counted in HiddenCalls
		}
		u, kind := usedBy(st.Text)
		switch kind {
		case monitoringStmt:
			continue
		case otherStmt:
			s.Statements.UnattributedCalls += st.Calls
			continue
		}
		type found struct {
			key string
			rel *schema.Relation
		}
		var tables []found
		alias := map[string]int{} // qualifier -> index in tables
		for _, rv := range u.relations {
			if rv.schema == "" && u.ctes[rv.name] && !rv.cteSelf {
				continue
			}
			r := resolve(m, path, rv)
			if r == nil {
				continue
			}
			k := r.QName().String()
			i := slices.IndexFunc(tables, func(f found) bool { return f.key == k })
			if i < 0 {
				tables = append(tables, found{k, r})
				i = len(tables) - 1
			}
			alias[rv.name] = i
			if rv.alias != "" {
				alias[rv.alias] = i
			}
		}
		if len(tables) == 0 {
			s.Statements.UnattributedCalls += st.Calls
			continue
		}
		q := Query{QueryID: st.QueryID, Text: truncate(st.Text), Calls: st.Calls, TotalMs: st.TotalMs}
		if st.Calls > 0 {
			q.MeanMs = st.TotalMs / float64(st.Calls)
		}
		table := func(k string) *Table {
			t := s.Tables[k]
			if t == nil {
				t = &Table{} // a view, or a table without statistics yet
				s.Tables[k] = t
			}
			return t
		}
		for _, f := range tables {
			t := table(f.key)
			t.QueryCalls += st.Calls
			t.QueryTimeMs += st.TotalMs
			addTopQuery(t, q)
		}
		// A qualified column belongs to its table; an unqualified one only
		// when exactly one table in the statement has it.
		counted := map[[2]string]bool{}
		for _, c := range u.columns {
			var owners []found
			if c.qualifier != "" {
				if i, ok := alias[c.qualifier]; ok && hasColumn(tables[i].rel, c.name) {
					owners = []found{tables[i]}
				}
			} else {
				for _, f := range tables {
					if hasColumn(f.rel, c.name) {
						owners = append(owners, f)
					}
				}
			}
			if len(owners) != 1 || counted[[2]string{owners[0].key, c.name}] {
				continue
			}
			counted[[2]string{owners[0].key, c.name}] = true
			t := table(owners[0].key)
			if t.Columns == nil {
				t.Columns = map[string]ColumnUse{}
			}
			use := t.Columns[c.name]
			use.Queries++
			use.Calls += st.Calls
			t.Columns[c.name] = use
		}
		starred := map[string]bool{}
		for _, qual := range u.stars {
			for i, f := range tables {
				if j, ok := alias[qual]; (qual == "" || (ok && j == i)) && !starred[f.key] {
					starred[f.key] = true
					table(f.key).WildcardCalls += st.Calls
				}
			}
		}
	}
}

type stmtKind int

const (
	dataStmt stmtKind = iota
	otherStmt
	monitoringStmt
)

// monitoring is pg_stat_statements' own surface: reading it (as dbscope
// does) is not application traffic.
var monitoring = map[string]bool{"pg_stat_statements": true, "pg_stat_statements_info": true}

type rangeVar struct {
	schema, name, alias string
	// cteSelf: inside a non-recursive CTE that has the same name, which there
	// means the table, not the CTE.
	cteSelf bool
}

type columnRef struct{ qualifier, name string }

type usage struct {
	relations []rangeVar
	ctes      map[string]bool
	columns   []columnRef
	stars     []string // qualifiers; "" for a bare *
}

// usedBy parses one normalized statement ($n parameters parse like any
// parameter) and lists what it names.
func usedBy(text string) (usage, stmtKind) {
	tree, err := pgquery.Parse(text)
	if err != nil || len(tree.GetStmts()) != 1 {
		return usage{}, otherStmt
	}
	root := tree.GetStmts()[0].GetStmt()
	u := usage{ctes: map[string]bool{}}
	self := map[*pg_query.RangeVar]bool{}
	monitor := false
	apply.Walk(root, func(msg protoreflect.Message) {
		switch v := msg.Interface().(type) {
		case *pg_query.CommonTableExpr:
			u.ctes[v.GetCtename()] = true
			if !v.GetCterecursive() {
				apply.Walk(v.GetCtequery(), func(inner protoreflect.Message) {
					if rv, ok := inner.Interface().(*pg_query.RangeVar); ok && rv.GetSchemaname() == "" && rv.GetRelname() == v.GetCtename() {
						self[rv] = true
					}
				})
			}
		case *pg_query.FuncCall:
			if f := v.GetFuncname(); len(f) > 0 && monitoring[f[len(f)-1].GetString_().GetSval()] {
				monitor = true
			}
		case *pg_query.RangeVar:
			if monitoring[v.GetRelname()] {
				monitor = true
			}
		}
	})
	switch {
	case monitor:
		return usage{}, monitoringStmt
	case root.GetSelectStmt() == nil && root.GetInsertStmt() == nil && root.GetUpdateStmt() == nil &&
		root.GetDeleteStmt() == nil && root.GetMergeStmt() == nil:
		return usage{}, otherStmt
	}
	apply.Walk(root, func(msg protoreflect.Message) {
		switch v := msg.Interface().(type) {
		case *pg_query.RangeVar:
			u.relations = append(u.relations, rangeVar{v.GetSchemaname(), v.GetRelname(), v.GetAlias().GetAliasname(), self[v]})
		case *pg_query.ColumnRef:
			f := v.GetFields()
			if len(f) == 0 {
				return
			}
			qual := ""
			if len(f) >= 2 {
				qual = f[len(f)-2].GetString_().GetSval()
			}
			if f[len(f)-1].GetAStar() != nil {
				u.stars = append(u.stars, qual)
				return
			}
			u.columns = append(u.columns, columnRef{qual, f[len(f)-1].GetString_().GetSval()})
		}
	})
	return u, dataStmt
}

// resolve finds the relation a name means. An unqualified name takes the
// first schema on the path that has it in the database; when that relation
// is not in m (a schema not shown), it is not attributed rather than matched
// to a same-named table further down the path.
func resolve(m *schema.Model, path Path, rv rangeVar) *schema.Relation {
	if rv.schema != "" {
		return m.Relation(schema.QualifiedName{Schema: rv.schema, Name: rv.name})
	}
	for _, sc := range path.Schemas {
		r := m.Relation(schema.QualifiedName{Schema: sc, Name: rv.name})
		if path.Names == nil {
			if r != nil {
				return r
			}
			continue
		}
		if path.Names[sc][rv.name] {
			return r
		}
	}
	return nil
}

func hasColumn(r *schema.Relation, name string) bool {
	for _, c := range r.Columns {
		if c.Name == name {
			return true
		}
	}
	return false
}

func truncate(s string) string {
	if r := []rune(s); len(r) > maxTextLen {
		return string(r[:maxTextLen]) + "…"
	}
	return s
}

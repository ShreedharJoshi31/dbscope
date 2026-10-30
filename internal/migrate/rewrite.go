package migrate

import (
	"regexp"
	"slices"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/rules"
	"github.com/dbscope/dbscope/internal/schema"
)

// rewriteStatement writes a safer form of s for the findings it fixes, or
// returns nil. st must not yet include s.
func rewriteStatement(st *apply.State, s *ingest.Statement, eff *rules.Effect, findings []rules.Finding) *Rewrite {
	has := func(id string) bool {
		return slices.ContainsFunc(findings, func(f rules.Finding) bool { return f.ID == id })
	}
	inTx := s.Tx != ingest.TxNone
	sql, changed := s.SQL, false
	exposed := has("DBS-NO-LOCK-TIMEOUT")
	var notes []string
	switch {
	case has("DBS-INDEX-NOT-CONCURRENT") && slices.Contains(eff.Keys, "create_index"):
		if inTx {
			notes = append(notes, "CREATE INDEX CONCURRENTLY would let writes continue during the build, but it cannot run in a transaction: move it to its own migration that runs outside one.")
		} else if out, ok := insertAfter(sql, pg_query.Token_INDEX, "CONCURRENTLY"); ok {
			sql, changed, exposed = out, true, false
			notes = append(notes, "Builds the index CONCURRENTLY, under SHARE UPDATE EXCLUSIVE, so writes continue. It must stay outside a transaction block, takes longer, and if it fails it leaves an INVALID index to drop before retrying.")
		}
	case has("DBS-DROP-INDEX-NOT-CONCURRENT") && dropIndexConcurrentlyAllowed(st, s.Node.GetDropStmt()):
		if inTx {
			notes = append(notes, "DROP INDEX CONCURRENTLY would not block queries on the table, but it cannot run in a transaction: move it to its own migration that runs outside one.")
		} else if out, ok := insertAfter(sql, pg_query.Token_INDEX, "CONCURRENTLY"); ok {
			sql, changed, exposed = out, true, false
			notes = append(notes, "Drops the index CONCURRENTLY, under SHARE UPDATE EXCLUSIVE, so queries on the table continue. It must stay outside a transaction block.")
		}
	case has("DBS-VALIDATE-IN-ADD") && (slices.Contains(eff.Keys, "add_fk") || slices.Contains(eff.Keys, "add_check")):
		if inTx {
			notes = append(notes, "ADD ... NOT VALID followed by VALIDATE CONSTRAINT only helps when each runs in its own transaction; in this one the ADD's lock would be held during the validation. Split it into two migrations.")
		} else if out, ok := notValid(st, sql, s.Node.GetAlterTableStmt()); ok {
			sql, changed = out, true
			notes = append(notes, "Adds the constraint NOT VALID, which checks new rows at once without scanning, then validates the existing rows under SHARE UPDATE EXCLUSIVE, which lets reads and writes continue. The two statements must commit separately.")
		}
	}
	if exposed {
		set := "SET lock_timeout = '2s';\n"
		note := "Sets lock_timeout first so the lock request gives up after 2 s instead of queueing every later query on the table; retry the migration if it times out."
		if inTx {
			set = "SET LOCAL lock_timeout = '2s';\n"
		} else {
			note += " The setting lasts for the rest of the session."
		}
		sql, changed = set+sql, true
		notes = append(notes, note)
	}
	if !changed {
		return nil
	}
	return &Rewrite{SQL: sql, Note: strings.Join(notes, " ")}
}

// insertAfter inserts word after the first token tok of sql.
func insertAfter(sql string, tok pg_query.Token, word string) (string, bool) {
	res, err := pgquery.Scan(sql)
	if err != nil {
		return "", false
	}
	for _, t := range res.GetTokens() {
		if t.GetToken() == tok {
			return sql[:t.GetEnd()] + " " + word + sql[t.GetEnd():], true
		}
	}
	return "", false
}

// dropIndexConcurrentlyAllowed: CONCURRENTLY takes one index, no CASCADE, and
// not a partitioned index (PostgreSQL rejects all three).
func dropIndexConcurrentlyAllowed(st *apply.State, d *pg_query.DropStmt) bool {
	if d == nil || len(d.GetObjects()) != 1 || d.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE {
		return false
	}
	nsp, name := apply.QualifiedFromList(d.GetObjects()[0].GetList().GetItems())
	rel, _ := st.FindIndex(nsp, name)
	return rel != nil && rel.Kind != schema.KindPartitionedTable
}

// notValid splits a single ADD CONSTRAINT ... FOREIGN KEY | CHECK into
// ADD ... NOT VALID and VALIDATE CONSTRAINT, naming the constraint the way
// PostgreSQL would when the original did not.
func notValid(st *apply.State, sql string, a *pg_query.AlterTableStmt) (string, bool) {
	if a == nil || len(a.GetCmds()) != 1 || a.GetMissingOk() || !a.GetRelation().GetInh() {
		return "", false
	}
	cmd := a.GetCmds()[0].GetAlterTableCmd()
	con := cmd.GetDef().GetConstraint()
	if cmd.GetSubtype() != pg_query.AlterTableType_AT_AddConstraint || con == nil {
		return "", false
	}
	q := st.Resolve(a.GetRelation())
	rel := st.Lookup(q)
	if rel == nil || rel.Kind == schema.KindPartitionedTable {
		return "", false
	}
	name := con.GetConname()
	if name == "" {
		name = defaultConstraintName(st, rel, con)
		if name == "" {
			return "", false
		}
		var ok bool
		if sql, ok = insertAfter(sql, pg_query.Token_ADD_P, "CONSTRAINT "+quoteIdent(name)); !ok {
			return "", false
		}
	}
	table := quoteIdent(a.GetRelation().GetRelname())
	if nsp := a.GetRelation().GetSchemaname(); nsp != "" {
		table = quoteIdent(nsp) + "." + table
	}
	return sql + " NOT VALID;\nALTER TABLE " + table + " VALIDATE CONSTRAINT " + quoteIdent(name), true
}

// defaultConstraintName is ChooseConstraintName's choice (PostgreSQL
// src/backend/catalog/heap.c, tablecmds.c): table_cols_fkey with every FK
// column, table_col_check when the CHECK reads exactly one column, else
// table_check. It returns "" when PostgreSQL would truncate or number the
// name, since then the prediction is less certain.
func defaultConstraintName(st *apply.State, rel *schema.Relation, con *pg_query.Constraint) string {
	var cols []string
	label := "check"
	switch con.GetContype() {
	case pg_query.ConstrType_CONSTR_FOREIGN:
		label = "fkey"
		for _, n := range con.GetFkAttrs() {
			cols = append(cols, n.GetString_().GetSval())
		}
	case pg_query.ConstrType_CONSTR_CHECK:
		apply.Walk(con.GetRawExpr(), func(m protoreflect.Message) {
			if cr, ok := m.Interface().(*pg_query.ColumnRef); ok {
				f := cr.GetFields()
				if len(f) > 0 && f[len(f)-1].GetString_() != nil {
					if c := f[len(f)-1].GetString_().GetSval(); !slices.Contains(cols, c) {
						cols = append(cols, c)
					}
				}
			}
		})
		if len(cols) != 1 {
			cols = nil
		}
	default:
		return ""
	}
	name := strings.Join(append(append([]string{rel.Name}, cols...), label), "_")
	if len(name) > 63 || st.NameTaken(rel.Schema, name) {
		return ""
	}
	return name
}

var plainIdent = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)

func quoteIdent(s string) string {
	if plainIdent.MatchString(s) {
		if r, err := pgquery.Scan(s); err == nil && len(r.GetTokens()) == 1 && r.GetTokens()[0].GetKeywordKind() == pg_query.KeywordKind_NO_KEYWORD {
			return s
		}
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

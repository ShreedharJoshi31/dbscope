package apply

import (
	"fmt"
	"slices"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/dbscope/dbscope/internal/schema"
)

// noEffect are statements that never change structure; they are not
// counted. Everything else without a modeled effect (functions, triggers,
// policies, sequences, extensions, aggregates, ...) is counted in Skipped,
// which a schema dump reports as "not modeled".
var noEffect = map[string]bool{
	"VariableSetStmt": true, "VariableShowStmt": true, "TransactionStmt": true, "SelectStmt": true,
	"GrantStmt": true, "GrantRoleStmt": true, "AlterOwnerStmt": true, "AlterDefaultPrivilegesStmt": true,
	"InsertStmt": true, "UpdateStmt": true, "DeleteStmt": true, "MergeStmt": true, "CopyStmt": true,
	"VacuumStmt": true, "ReindexStmt": true, "RefreshMatViewStmt": true, "LockStmt": true,
	"CheckPointStmt": true, "DoStmt": true, "CallStmt": true, "TruncateStmt": true, "SecLabelStmt": true,
	"DiscardStmt": true, "NotifyStmt": true, "ListenStmt": true, "ExplainStmt": true,
	"AlterTableSpaceOptionsStmt": true, "AlterDatabaseSetStmt": true, "AlterRoleSetStmt": true,
	"CreateRoleStmt": true, "AlterRoleStmt": true,
}

// NodeName is the statement's message type, e.g. "AlterTableStmt".
func NodeName(n *pg_query.Node) string {
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

// Apply records n's effect on the model. Statements with no structural
// effect are ignored; ones that change structure dbscope does not model are
// counted in Skipped.
func (s *State) Apply(n *pg_query.Node) {
	switch {
	case n.GetCreateStmt() != nil:
		s.createTable(n.GetCreateStmt())
	case n.GetAlterTableStmt() != nil:
		s.alterTable(n.GetAlterTableStmt())
	case n.GetIndexStmt() != nil:
		s.createIndex(n.GetIndexStmt())
	case n.GetDropStmt() != nil:
		s.drop(n.GetDropStmt())
	case n.GetRenameStmt() != nil:
		s.rename(n.GetRenameStmt())
	case n.GetViewStmt() != nil:
		s.createView(n.GetViewStmt())
	case n.GetCreateTableAsStmt() != nil:
		s.createTableAs(n.GetCreateTableAsStmt())
	case n.GetCreateEnumStmt() != nil:
		e := n.GetCreateEnumStmt()
		nsp, name := QualifiedFromList(e.GetTypeName())
		if nsp == "" {
			nsp = s.firstSchema()
		}
		var vals []string
		for _, v := range e.GetVals() {
			vals = append(vals, v.GetString_().GetSval())
		}
		s.Model.Enums = append(s.Model.Enums, schema.Enum{Schema: nsp, Name: name, Values: vals})
	case n.GetAlterEnumStmt() != nil:
		s.alterEnum(n.GetAlterEnumStmt())
	case n.GetCreateDomainStmt() != nil:
		d := n.GetCreateDomainStmt()
		nsp, name := QualifiedFromList(d.GetDomainname())
		if nsp == "" {
			nsp = s.firstSchema()
		}
		if len(d.GetConstraints()) > 0 {
			for _, c := range d.GetConstraints() {
				if t := c.GetConstraint().GetContype(); t == pg_query.ConstrType_CONSTR_CHECK || t == pg_query.ConstrType_CONSTR_NOTNULL {
					s.Catalog.ConstrainedDomains[nsp+"."+name] = true
				}
			}
		}
	case n.GetCreateSchemaStmt() != nil:
		name := n.GetCreateSchemaStmt().GetSchemaname()
		if name != "" && !slices.Contains(s.Model.Schemas, name) {
			s.Model.Schemas = append(s.Model.Schemas, name)
		}
	case n.GetCommentStmt() != nil:
		s.comment(n.GetCommentStmt())
	case n.GetAlterObjectSchemaStmt() != nil:
		s.setSchema(n.GetAlterObjectSchemaStmt())
	case n.GetVariableSetStmt() != nil:
		s.setVariable(n.GetVariableSetStmt())
	case n.GetSelectStmt() != nil:
		s.selectSetConfig(n.GetSelectStmt())
	case n.GetClusterStmt() != nil:
		// CLUSTER t USING idx marks idx as the clustered index of t.
		if c := n.GetClusterStmt(); c.GetRelation() != nil && c.GetIndexname() != "" {
			s.Catalog.Clustered[s.Resolve(c.GetRelation()).String()] = true
		}
	default:
		if name := NodeName(n); !noEffect[name] {
			s.Skipped[name]++
		}
	}
}

func (s *State) firstSchema() string {
	if len(s.SearchPath) > 0 {
		return s.SearchPath[0]
	}
	return "public"
}

func (s *State) setVariable(v *pg_query.VariableSetStmt) {
	if v.GetName() != "search_path" {
		return
	}
	switch v.GetKind() {
	case pg_query.VariableSetKind_VAR_SET_VALUE:
		var path []string
		for _, a := range v.GetArgs() {
			if c := a.GetAConst(); c != nil && c.GetSval() != nil {
				for _, p := range strings.Split(c.GetSval().GetSval(), ",") {
					if p = strings.Trim(strings.TrimSpace(p), `"`); p != "" && p != "$user" {
						path = append(path, p)
					}
				}
			}
		}
		s.SearchPath = path
	case pg_query.VariableSetKind_VAR_SET_DEFAULT, pg_query.VariableSetKind_VAR_RESET, pg_query.VariableSetKind_VAR_RESET_ALL:
		s.SearchPath = slices.Clone(DefaultSearchPath)
	}
}

// selectSetConfig handles pg_dump's SELECT pg_catalog.set_config('search_path', ”, false).
func (s *State) selectSetConfig(sel *pg_query.SelectStmt) {
	for _, t := range sel.GetTargetList() {
		fc := t.GetResTarget().GetVal().GetFuncCall()
		if fc == nil {
			continue
		}
		_, name := QualifiedFromList(fc.GetFuncname())
		args := fc.GetArgs()
		if name != "set_config" || len(args) < 2 {
			continue
		}
		if args[0].GetAConst().GetSval().GetSval() != "search_path" {
			continue
		}
		var path []string
		for _, p := range strings.Split(args[1].GetAConst().GetSval().GetSval(), ",") {
			if p = strings.Trim(strings.TrimSpace(p), `"`); p != "" && p != "$user" {
				path = append(path, p)
			}
		}
		s.SearchPath = path
	}
}

// ---- CREATE TABLE ----

func (s *State) createTable(c *pg_query.CreateStmt) {
	q := s.Resolve(c.GetRelation())
	if c.GetRelation().GetRelpersistence() == "t" {
		return // temporary tables are not part of the schema
	}
	if c.GetPartbound() != nil && len(c.GetInhRelations()) == 1 {
		parentQ := s.Resolve(c.GetInhRelations()[0].GetRangeVar())
		parent := s.Lookup(parentQ)
		if parent == nil {
			s.warnf("%s is a partition of %s, which is not in the base schema", q, parentQ)
			return
		}
		if !slices.Contains(parent.Partitions, q) {
			parent.Partitions = append(parent.Partitions, q)
			slices.SortFunc(parent.Partitions, cmpQName)
		}
		if c.GetPartbound().GetIsDefault() {
			s.Catalog.DefaultPartitions[parentQ.String()] = q
		}
		s.created[q] = true
		return
	}
	if s.Lookup(q) != nil {
		if !c.GetIfNotExists() {
			s.warnf("CREATE TABLE %s: the relation already exists", q)
		}
		return
	}
	if c.GetOfTypename() != nil || len(c.GetInhRelations()) > 0 {
		s.warnf("CREATE TABLE %s: OF type and INHERITS are not modeled; columns may be incomplete", q)
	}
	r := schema.Relation{Schema: q.Schema, Name: q.Name, Kind: schema.KindTable, Columns: []schema.Column{}}
	if ps := c.GetPartspec(); ps != nil {
		r.Kind = schema.KindPartitionedTable
		var cols []string
		for _, p := range ps.GetPartParams() {
			pe := p.GetPartitionElem()
			if pe.GetName() != "" {
				cols = append(cols, pe.GetName())
			} else {
				cols = append(cols, Deparse(pe.GetExpr()))
			}
		}
		strategy := strings.ToUpper(strings.TrimPrefix(strings.ToLower(ps.GetStrategy().String()), "partition_strategy_"))
		r.PartitionKey = fmt.Sprintf("%s (%s)", strategy, strings.Join(cols, ", "))
	}
	s.Model.Relations = append(s.Model.Relations, r)
	s.created[q] = true
	rel := s.Lookup(q)
	var later []*pg_query.Constraint
	for _, elt := range c.GetTableElts() {
		switch {
		case elt.GetColumnDef() != nil:
			later = append(later, s.addColumnDef(rel, elt.GetColumnDef())...)
		case elt.GetConstraint() != nil:
			later = append(later, elt.GetConstraint())
		case elt.GetTableLikeClause() != nil:
			s.warnf("CREATE TABLE %s (LIKE ...) is not modeled", q)
		}
	}
	for _, con := range later {
		s.addConstraint(s.Lookup(q), con)
	}
}

// addColumnDef appends the column and returns its constraints that need
// table context (PK, UNIQUE, CHECK, FK), converted to table form.
func (s *State) addColumnDef(rel *schema.Relation, cd *pg_query.ColumnDef) []*pg_query.Constraint {
	t := TypeFromAST(cd.GetTypeName())
	col := schema.Column{Name: cd.GetColname(), Type: t.String(), Nullable: !cd.GetIsNotNull()}
	if t.Serial {
		col.Nullable = false
		col.Default = fmt.Sprintf("nextval('%s_%s_seq'::regclass)", rel.Name, cd.GetColname())
	}
	if d := cd.GetRawDefault(); d != nil {
		col.Default = Deparse(d)
	}
	var later []*pg_query.Constraint
	for _, cn := range cd.GetConstraints() {
		con := cn.GetConstraint()
		switch con.GetContype() {
		case pg_query.ConstrType_CONSTR_NOTNULL:
			col.Nullable = false
		case pg_query.ConstrType_CONSTR_NULL:
			col.Nullable = true
		case pg_query.ConstrType_CONSTR_DEFAULT:
			col.Default = Deparse(con.GetRawExpr())
		case pg_query.ConstrType_CONSTR_IDENTITY:
			col.Nullable = false
			col.Identity = "by_default"
			if con.GetGeneratedWhen() == "a" {
				col.Identity = "always"
			}
		case pg_query.ConstrType_CONSTR_GENERATED:
			col.Generated, col.GeneratedKind = Deparse(con.GetRawExpr()), "stored"
		case pg_query.ConstrType_CONSTR_PRIMARY:
			col.Nullable = false
			fallthrough
		case pg_query.ConstrType_CONSTR_UNIQUE, pg_query.ConstrType_CONSTR_CHECK, pg_query.ConstrType_CONSTR_FOREIGN:
			c := cloneConstraint(con)
			if len(c.Keys) == 0 && con.GetContype() != pg_query.ConstrType_CONSTR_CHECK {
				c.Keys = []*pg_query.Node{pg_query.MakeStrNode(cd.GetColname())}
			}
			if con.GetContype() == pg_query.ConstrType_CONSTR_FOREIGN && len(c.FkAttrs) == 0 {
				c.FkAttrs = []*pg_query.Node{pg_query.MakeStrNode(cd.GetColname())}
			}
			if con.GetContype() == pg_query.ConstrType_CONSTR_CHECK && c.Conname == "" {
				c.Conname = s.chooseName(rel.Schema, rel.Name, []string{cd.GetColname()}, "check")
			}
			later = append(later, c)
		}
	}
	if c := rel.Column(col.Name); c != nil {
		s.warnf("%s.%s: column %s already exists", rel.Schema, rel.Name, col.Name)
		return nil
	}
	rel.Columns = append(rel.Columns, col)
	return later
}

func cloneConstraint(c *pg_query.Constraint) *pg_query.Constraint {
	return proto.Clone(c).(*pg_query.Constraint)
}

// chooseName mimics ChooseConstraintName/ChooseRelationName: table_cols_suffix,
// truncated to 63 bytes, numbered when taken.
func (s *State) chooseName(nsp, table string, cols []string, suffix string) string {
	base := table
	if len(cols) > 0 {
		base += "_" + strings.Join(cols, "_")
	}
	if over := len(base) + len(suffix) + 1 - 63; over > 0 {
		base = base[:max(1, len(base)-over)]
	}
	name := base + "_" + suffix
	for i := 1; s.nameTaken(nsp, name); i++ {
		name = fmt.Sprintf("%s_%s%d", base, suffix, i)
	}
	return name
}

func (s *State) nameTaken(nsp, name string) bool {
	if s.Lookup(QName{Schema: nsp, Name: name}) != nil {
		return true
	}
	for _, r := range s.Model.Relations {
		if r.Schema != nsp {
			continue
		}
		for _, ix := range r.Indexes {
			if ix.Name == name {
				return true
			}
		}
		for _, fk := range r.ForeignKeys {
			if fk.Name == name {
				return true
			}
		}
		for _, ck := range r.Checks {
			if ck.Name == name {
				return true
			}
		}
	}
	return false
}

func (s *State) addConstraint(rel *schema.Relation, con *pg_query.Constraint) {
	if rel == nil {
		return
	}
	keys := strs(con.GetKeys())
	switch con.GetContype() {
	case pg_query.ConstrType_CONSTR_PRIMARY, pg_query.ConstrType_CONSTR_UNIQUE:
		primary := con.GetContype() == pg_query.ConstrType_CONSTR_PRIMARY
		name := con.GetConname()
		var ix *schema.Index
		if con.GetIndexname() != "" { // ... USING INDEX idx
			for i := range rel.Indexes {
				if rel.Indexes[i].Name == con.GetIndexname() {
					ix = &rel.Indexes[i]
				}
			}
			if ix == nil {
				s.warnf("%s: index %s is not in the schema", rel.QName(), con.GetIndexname())
				return
			}
			keys = slices.Clone(ix.Columns)
			if name != "" {
				ix.Name = name // the index is renamed to the constraint name
			}
			ix.Unique, ix.Primary = true, primary
		}
		if name == "" {
			if primary {
				name = s.chooseName(rel.Schema, rel.Name, nil, "pkey")
			} else {
				name = s.chooseName(rel.Schema, rel.Name, keys, "key")
			}
		}
		if ix == nil {
			rel.Indexes = append(rel.Indexes, schema.Index{Name: name, Columns: keys, Include: strs(con.GetIncluding()),
				Unique: true, Primary: primary, Method: "btree", Valid: true})
		}
		if primary {
			rel.PrimaryKey = keys
			for _, k := range keys {
				if c := rel.Column(k); c != nil {
					c.Nullable = false
				}
			}
		} else {
			rel.Uniques = append(rel.Uniques, keys)
		}
	case pg_query.ConstrType_CONSTR_CHECK:
		name := con.GetConname()
		if name == "" {
			name = s.chooseName(rel.Schema, rel.Name, firstColumnRef(con.GetRawExpr()), "check")
		}
		rel.Checks = append(rel.Checks, schema.Check{Name: name, Expr: Deparse(con.GetRawExpr()), Validated: !con.GetSkipValidation()})
	case pg_query.ConstrType_CONSTR_FOREIGN:
		ref := s.Resolve(con.GetPktable())
		cols := strs(con.GetFkAttrs())
		refCols := strs(con.GetPkAttrs())
		if len(refCols) == 0 {
			if target := s.Lookup(ref); target != nil {
				refCols = slices.Clone(target.PrimaryKey)
			}
		}
		name := con.GetConname()
		if name == "" {
			name = s.chooseName(rel.Schema, rel.Name, cols, "fkey")
		}
		rel.ForeignKeys = append(rel.ForeignKeys, schema.ForeignKey{
			Name: name, Columns: cols, Ref: ref, RefCols: refCols,
			OnDelete: fkAction(con.GetFkDelAction()), OnUpdate: fkAction(con.GetFkUpdAction()),
			Validated: !con.GetSkipValidation(),
		})
	case pg_query.ConstrType_CONSTR_EXCLUSION:
		name := con.GetConname()
		if name == "" {
			name = s.chooseName(rel.Schema, rel.Name, nil, "excl")
		}
		var cols []string
		for _, ex := range con.GetExclusions() {
			if l := ex.GetList(); l != nil && len(l.GetItems()) > 0 {
				if ie := l.GetItems()[0].GetIndexElem(); ie != nil {
					cols = append(cols, cmpOr(ie.GetName(), Deparse(ie.GetExpr())))
				}
			}
		}
		rel.Indexes = append(rel.Indexes, schema.Index{Name: name, Columns: cols, Method: cmpOr(con.GetAccessMethod(), "gist"), Valid: true})
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var fkActions = map[string]string{"a": "no action", "r": "restrict", "c": "cascade", "n": "set null", "d": "set default"}

func fkAction(code string) string {
	if a, ok := fkActions[code]; ok {
		return a
	}
	return "no action"
}

func firstColumnRef(n *pg_query.Node) []string {
	var out []string
	Walk(n, func(m protoreflect.Message) {
		if len(out) > 0 {
			return
		}
		if cr, ok := m.Interface().(*pg_query.ColumnRef); ok {
			f := cr.GetFields()
			if len(f) > 0 && f[len(f)-1].GetString_() != nil {
				out = []string{f[len(f)-1].GetString_().GetSval()}
			}
		}
	})
	return out
}

package apply

import (
	"slices"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/dbscope/dbscope/internal/schema"
)

// ---- ALTER TABLE ----

func (s *State) alterTable(a *pg_query.AlterTableStmt) {
	q := s.Resolve(a.GetRelation())
	if a.GetObjtype() == pg_query.ObjectType_OBJECT_INDEX {
		return // ALTER INDEX ... ATTACH PARTITION / SET (...): no modeled effect
	}
	rel := s.Lookup(q)
	if rel == nil {
		if s.PartitionParent(q) == nil && !a.GetMissingOk() {
			s.warnf("ALTER TABLE %s: the relation is not in the base schema", q)
		}
		return
	}
	for _, cn := range a.GetCmds() {
		rel = s.Lookup(q) // a previous subcommand may have reallocated the slice
		if rel == nil {
			return
		}
		cmd := cn.GetAlterTableCmd()
		switch cmd.GetSubtype() {
		case pg_query.AlterTableType_AT_AddColumn:
			cd := cmd.GetDef().GetColumnDef()
			if cmd.GetMissingOk() && rel.Column(cd.GetColname()) != nil {
				continue
			}
			for _, con := range s.addColumnDef(rel, cd) {
				s.addConstraint(s.Lookup(q), con)
			}
		case pg_query.AlterTableType_AT_DropColumn:
			s.dropColumn(rel, cmd.GetName(), cmd.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE, cmd.GetMissingOk())
		case pg_query.AlterTableType_AT_AlterColumnType:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Type = TypeFromAST(cmd.GetDef().GetColumnDef().GetTypeName()).String()
			}
		case pg_query.AlterTableType_AT_ColumnDefault:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Default = Deparse(cmd.GetDef())
			}
		case pg_query.AlterTableType_AT_SetNotNull:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Nullable = false
			}
		case pg_query.AlterTableType_AT_DropNotNull:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Nullable = true
			}
		case pg_query.AlterTableType_AT_AddConstraint:
			s.addConstraint(rel, cmd.GetDef().GetConstraint())
		case pg_query.AlterTableType_AT_DropConstraint:
			s.dropConstraint(rel, cmd.GetName(), cmd.GetMissingOk())
		case pg_query.AlterTableType_AT_ValidateConstraint:
			for i := range rel.ForeignKeys {
				if rel.ForeignKeys[i].Name == cmd.GetName() {
					rel.ForeignKeys[i].Validated = true
				}
			}
			for i := range rel.Checks {
				if rel.Checks[i].Name == cmd.GetName() {
					rel.Checks[i].Validated = true
				}
			}
		case pg_query.AlterTableType_AT_AddIdentity:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Identity, c.Nullable = "by_default", false
				if cmd.GetDef().GetConstraint().GetGeneratedWhen() == "a" {
					c.Identity = "always"
				}
			}
		case pg_query.AlterTableType_AT_DropIdentity:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Identity = ""
			}
		case pg_query.AlterTableType_AT_DropExpression:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Generated, c.GeneratedKind = "", ""
			}
		case pg_query.AlterTableType_AT_SetExpression:
			if c := s.column(rel, cmd.GetName()); c != nil {
				c.Generated = Deparse(cmd.GetDef())
			}
		case pg_query.AlterTableType_AT_ClusterOn:
			s.Catalog.Clustered[q.String()] = true
		case pg_query.AlterTableType_AT_DropCluster:
			delete(s.Catalog.Clustered, q.String())
		case pg_query.AlterTableType_AT_AttachPartition:
			s.attach(rel, cmd.GetDef().GetPartitionCmd())
		case pg_query.AlterTableType_AT_DetachPartition:
			s.detach(rel, cmd.GetDef().GetPartitionCmd())
		}
	}
}

func (s *State) column(rel *schema.Relation, name string) *schema.Column {
	c := rel.Column(name)
	if c == nil {
		s.warnf("%s: column %s is not in the schema", rel.QName(), name)
	}
	return c
}

func (s *State) dropColumn(rel *schema.Relation, name string, cascade, missingOK bool) {
	i := slices.IndexFunc(rel.Columns, func(c schema.Column) bool { return c.Name == name })
	if i < 0 {
		if !missingOK {
			s.warnf("%s: column %s is not in the schema", rel.QName(), name)
		}
		return
	}
	rel.Columns = slices.Delete(rel.Columns, i, i+1)
	if slices.Contains(rel.PrimaryKey, name) {
		rel.PrimaryKey = nil
	}
	rel.Uniques = slices.DeleteFunc(rel.Uniques, func(u []string) bool { return slices.Contains(u, name) })
	rel.Indexes = slices.DeleteFunc(rel.Indexes, func(ix schema.Index) bool {
		return slices.Contains(ix.Columns, name) || slices.Contains(ix.Include, name)
	})
	rel.ForeignKeys = slices.DeleteFunc(rel.ForeignKeys, func(fk schema.ForeignKey) bool { return slices.Contains(fk.Columns, name) })
	rel.Checks = slices.DeleteFunc(rel.Checks, func(c schema.Check) bool { return mentions(c.Expr, name) })
	if cascade {
		q := rel.QName()
		for i := range s.Model.Relations {
			o := &s.Model.Relations[i]
			o.ForeignKeys = slices.DeleteFunc(o.ForeignKeys, func(fk schema.ForeignKey) bool {
				return fk.Ref == q && slices.Contains(fk.RefCols, name)
			})
		}
	}
	if m := s.renamed[rel.QName()]; m != nil {
		delete(m, name)
	}
}

// mentions reports whether expr contains name as a whole identifier.
func mentions(expr, name string) bool {
	for i := strings.Index(expr, name); i >= 0; {
		before := i == 0 || !identByte(expr[i-1])
		end := i + len(name)
		after := end >= len(expr) || !identByte(expr[end])
		if before && after {
			return true
		}
		next := strings.Index(expr[i+1:], name)
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return false
}

func identByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func (s *State) dropConstraint(rel *schema.Relation, name string, missingOK bool) {
	for i, ix := range rel.Indexes {
		if ix.Name != name || !(ix.Primary || ix.Unique) {
			continue
		}
		if ix.Primary {
			rel.PrimaryKey = nil
		} else {
			rel.Uniques = slices.DeleteFunc(rel.Uniques, func(u []string) bool { return slices.Equal(u, ix.Columns) })
		}
		rel.Indexes = slices.Delete(rel.Indexes, i, i+1)
		return
	}
	if i := slices.IndexFunc(rel.ForeignKeys, func(fk schema.ForeignKey) bool { return fk.Name == name }); i >= 0 {
		rel.ForeignKeys = slices.Delete(rel.ForeignKeys, i, i+1)
		return
	}
	if i := slices.IndexFunc(rel.Checks, func(c schema.Check) bool { return c.Name == name }); i >= 0 {
		rel.Checks = slices.Delete(rel.Checks, i, i+1)
		return
	}
	if !missingOK {
		s.warnf("%s: constraint %s is not in the schema", rel.QName(), name)
	}
}

func (s *State) attach(parent *schema.Relation, pc *pg_query.PartitionCmd) {
	pq := parent.QName()
	q := s.Resolve(pc.GetName())
	if i := slices.IndexFunc(s.Model.Relations, func(r schema.Relation) bool { return r.QName() == q }); i >= 0 {
		s.Model.Relations = slices.Delete(s.Model.Relations, i, i+1)
	}
	parent = s.Lookup(pq)
	if !slices.Contains(parent.Partitions, q) {
		parent.Partitions = append(parent.Partitions, q)
		slices.SortFunc(parent.Partitions, cmpQName)
	}
	if pc.GetBound().GetIsDefault() {
		s.Catalog.DefaultPartitions[pq.String()] = q
	}
}

func (s *State) detach(parent *schema.Relation, pc *pg_query.PartitionCmd) {
	q := s.Resolve(pc.GetName())
	i := slices.Index(parent.Partitions, q)
	if i < 0 {
		s.warnf("%s: %s is not a known partition", parent.QName(), q)
		return
	}
	parent.Partitions = slices.Delete(parent.Partitions, i, i+1)
	if s.Catalog.DefaultPartitions[parent.QName().String()] == q {
		delete(s.Catalog.DefaultPartitions, parent.QName().String())
	}
	r := schema.Relation{Schema: q.Schema, Name: q.Name, Kind: schema.KindTable,
		Columns: slices.Clone(parent.Columns), PrimaryKey: slices.Clone(parent.PrimaryKey)}
	s.Model.Relations = append(s.Model.Relations, r)
}

// ---- CREATE INDEX ----

func (s *State) createIndex(ix *pg_query.IndexStmt) {
	q := s.Resolve(ix.GetRelation())
	rel := s.Lookup(q)
	if rel == nil {
		if s.PartitionParent(q) == nil {
			s.warnf("CREATE INDEX on %s: the relation is not in the base schema", q)
		}
		return
	}
	var cols, nameCols []string
	for _, p := range ix.GetIndexParams() {
		e := p.GetIndexElem()
		if e.GetName() != "" {
			cols = append(cols, e.GetName())
			nameCols = append(nameCols, e.GetName())
			continue
		}
		cols = append(cols, Deparse(e.GetExpr()))
		label := "expr"
		if fc := e.GetExpr().GetFuncCall(); fc != nil {
			_, label = QualifiedFromList(fc.GetFuncname())
		}
		nameCols = append(nameCols, label)
	}
	name := ix.GetIdxname()
	if name == "" {
		name = s.chooseName(rel.Schema, rel.Name, nameCols, "idx")
	}
	for _, existing := range rel.Indexes {
		if existing.Name == name {
			if !ix.GetIfNotExists() {
				s.warnf("CREATE INDEX %s: the index already exists", name)
			}
			return
		}
	}
	var include []string
	for _, p := range ix.GetIndexIncludingParams() {
		include = append(include, p.GetIndexElem().GetName())
	}
	rel.Indexes = append(rel.Indexes, schema.Index{
		Name: name, Columns: cols, Include: include, Unique: ix.GetUnique(), Primary: ix.GetPrimary(),
		Method: cmpOr(ix.GetAccessMethod(), "btree"), Partial: Deparse(ix.GetWhereClause()), Valid: true,
	})
}

// ---- DROP ----

func (s *State) drop(d *pg_query.DropStmt) {
	cascade := d.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE
	for _, obj := range d.GetObjects() {
		switch d.GetRemoveType() {
		case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW,
			pg_query.ObjectType_OBJECT_FOREIGN_TABLE:
			nsp, name := QualifiedFromList(obj.GetList().GetItems())
			s.dropRelation(s.resolveName(nsp, name), cascade, d.GetMissingOk())
		case pg_query.ObjectType_OBJECT_INDEX:
			nsp, name := QualifiedFromList(obj.GetList().GetItems())
			rel, ix := s.FindIndex(nsp, name)
			if rel == nil {
				if !d.GetMissingOk() {
					s.warnf("DROP INDEX %s: the index is not in the schema", name)
				}
				continue
			}
			if ix.Primary {
				rel.PrimaryKey = nil
			} else if ix.Unique {
				cols := ix.Columns
				rel.Uniques = slices.DeleteFunc(rel.Uniques, func(u []string) bool { return slices.Equal(u, cols) })
			}
			rel.Indexes = slices.DeleteFunc(rel.Indexes, func(x schema.Index) bool { return x.Name == name })
		case pg_query.ObjectType_OBJECT_TYPE, pg_query.ObjectType_OBJECT_DOMAIN:
			nsp, name := QualifiedFromList(obj.GetTypeName().GetNames())
			if e := s.Enum(nsp, name); e != nil {
				key := *e
				s.Model.Enums = slices.DeleteFunc(s.Model.Enums, func(x schema.Enum) bool {
					return x.Schema == key.Schema && x.Name == key.Name
				})
			}
			if nsp == "" {
				nsp = s.firstSchema()
			}
			delete(s.Catalog.ConstrainedDomains, nsp+"."+name)
		case pg_query.ObjectType_OBJECT_SCHEMA:
			nsp := obj.GetString_().GetSval()
			if !cascade {
				continue
			}
			for _, r := range slices.Clone(s.Model.Relations) {
				if r.Schema == nsp {
					s.dropRelation(r.QName(), true, true)
				}
			}
			s.Model.Enums = slices.DeleteFunc(s.Model.Enums, func(e schema.Enum) bool { return e.Schema == nsp })
			s.Model.Schemas = slices.DeleteFunc(s.Model.Schemas, func(x string) bool { return x == nsp })
		}
	}
}

func (s *State) dropRelation(q QName, cascade, missingOK bool) {
	if p := s.PartitionParent(q); p != nil {
		p.Partitions = slices.DeleteFunc(p.Partitions, func(x QName) bool { return x == q })
		if s.Catalog.DefaultPartitions[p.QName().String()] == q {
			delete(s.Catalog.DefaultPartitions, p.QName().String())
		}
		return
	}
	if s.Lookup(q) == nil {
		if !missingOK {
			s.warnf("DROP %s: the relation is not in the schema", q)
		}
		return
	}
	var dependents []QName
	if cascade {
		dependents = s.Dependents(q)
	}
	drop := append([]QName{q}, dependents...)
	s.Model.Relations = slices.DeleteFunc(s.Model.Relations, func(r schema.Relation) bool { return slices.Contains(drop, r.QName()) })
	for i := range s.Model.Relations {
		r := &s.Model.Relations[i]
		r.ForeignKeys = slices.DeleteFunc(r.ForeignKeys, func(fk schema.ForeignKey) bool { return slices.Contains(drop, fk.Ref) })
	}
	delete(s.Catalog.DefaultPartitions, q.String())
	delete(s.Catalog.Clustered, q.String())
	delete(s.renamed, q)
}

// ---- RENAME ----

func (s *State) rename(r *pg_query.RenameStmt) {
	switch r.GetRenameType() {
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW,
		pg_query.ObjectType_OBJECT_FOREIGN_TABLE:
		old := s.Resolve(r.GetRelation())
		s.moveRelation(old, QName{Schema: old.Schema, Name: r.GetNewname()}, r.GetMissingOk())
	case pg_query.ObjectType_OBJECT_COLUMN:
		q := s.Resolve(r.GetRelation())
		rel := s.Lookup(q)
		if rel == nil {
			if s.PartitionParent(q) == nil && !r.GetMissingOk() {
				s.warnf("RENAME COLUMN on %s: the relation is not in the schema", q)
			}
			return
		}
		s.renameColumn(rel, r.GetSubname(), r.GetNewname())
	case pg_query.ObjectType_OBJECT_INDEX:
		rel, ix := s.FindIndex(r.GetRelation().GetSchemaname(), r.GetRelation().GetRelname())
		if rel == nil {
			if !r.GetMissingOk() {
				s.warnf("ALTER INDEX %s RENAME: the index is not in the schema", r.GetRelation().GetRelname())
			}
			return
		}
		ix.Name = r.GetNewname()
	case pg_query.ObjectType_OBJECT_TABCONSTRAINT:
		rel := s.Lookup(s.Resolve(r.GetRelation()))
		if rel == nil {
			return
		}
		for i := range rel.ForeignKeys {
			if rel.ForeignKeys[i].Name == r.GetSubname() {
				rel.ForeignKeys[i].Name = r.GetNewname()
			}
		}
		for i := range rel.Checks {
			if rel.Checks[i].Name == r.GetSubname() {
				rel.Checks[i].Name = r.GetNewname()
			}
		}
		for i := range rel.Indexes {
			if rel.Indexes[i].Name == r.GetSubname() {
				rel.Indexes[i].Name = r.GetNewname()
			}
		}
	case pg_query.ObjectType_OBJECT_TYPE:
		nsp, name := QualifiedFromList(r.GetObject().GetList().GetItems())
		if e := s.Enum(nsp, name); e != nil {
			e.Name = r.GetNewname()
		}
	}
}

// moveRelation renames or re-schemas a relation and every reference to it.
func (s *State) moveRelation(old, new QName, missingOK bool) {
	if p := s.PartitionParent(old); p != nil {
		for i, x := range p.Partitions {
			if x == old {
				p.Partitions[i] = new
			}
		}
		return
	}
	rel := s.Lookup(old)
	if rel == nil {
		if !missingOK {
			s.warnf("%s: the relation is not in the schema", old)
		}
		return
	}
	rel.Schema, rel.Name = new.Schema, new.Name
	for i := range s.Model.Relations {
		o := &s.Model.Relations[i]
		for j := range o.ForeignKeys {
			if o.ForeignKeys[j].Ref == old {
				o.ForeignKeys[j].Ref = new
			}
		}
		for j := range o.DependsOn {
			if o.DependsOn[j] == old {
				o.DependsOn[j] = new
			}
		}
	}
	if d, ok := s.Catalog.DefaultPartitions[old.String()]; ok {
		delete(s.Catalog.DefaultPartitions, old.String())
		s.Catalog.DefaultPartitions[new.String()] = d
	}
	if s.Catalog.Clustered[old.String()] {
		delete(s.Catalog.Clustered, old.String())
		s.Catalog.Clustered[new.String()] = true
	}
	if s.created[old] {
		delete(s.created, old)
		s.created[new] = true
	}
	if m, ok := s.renamed[old]; ok {
		delete(s.renamed, old)
		s.renamed[new] = m
	}
}

func (s *State) renameColumn(rel *schema.Relation, old, new string) {
	c := rel.Column(old)
	if c == nil {
		s.warnf("%s: column %s is not in the schema", rel.QName(), old)
		return
	}
	c.Name = new
	swap := func(list []string) {
		for i := range list {
			if list[i] == old {
				list[i] = new
			}
		}
	}
	swap(rel.PrimaryKey)
	for _, u := range rel.Uniques {
		swap(u)
	}
	for i := range rel.Indexes {
		swap(rel.Indexes[i].Columns)
		swap(rel.Indexes[i].Include)
	}
	for i := range rel.ForeignKeys {
		swap(rel.ForeignKeys[i].Columns)
	}
	q := rel.QName()
	for i := range s.Model.Relations {
		o := &s.Model.Relations[i]
		for j := range o.ForeignKeys {
			if o.ForeignKeys[j].Ref == q {
				swap(o.ForeignKeys[j].RefCols)
			}
		}
	}
	if s.created[q] {
		return
	}
	m := s.renamed[q]
	if m == nil {
		m = map[string]string{}
		s.renamed[q] = m
	}
	orig := old
	if o, ok := m[old]; ok {
		orig = o
		delete(m, old)
	}
	if orig != new {
		m[new] = orig
	}
}

func (s *State) setSchema(a *pg_query.AlterObjectSchemaStmt) {
	switch a.GetObjectType() {
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW:
		old := s.Resolve(a.GetRelation())
		s.moveRelation(old, QName{Schema: a.GetNewschema(), Name: old.Name}, a.GetMissingOk())
	case pg_query.ObjectType_OBJECT_TYPE:
		nsp, name := QualifiedFromList(a.GetObject().GetList().GetItems())
		if e := s.Enum(nsp, name); e != nil {
			e.Schema = a.GetNewschema()
		}
	default:
		s.Skipped["AlterObjectSchemaStmt"]++
	}
}

// ---- views ----

func (s *State) createView(v *pg_query.ViewStmt) {
	q := s.Resolve(v.GetView())
	s.defineView(q, schema.KindView, v.GetQuery(), strs(v.GetAliases()), v.GetReplace())
}

func (s *State) createTableAs(c *pg_query.CreateTableAsStmt) {
	q := s.Resolve(c.GetInto().GetRel())
	kind := schema.KindTable
	if c.GetObjtype() == pg_query.ObjectType_OBJECT_MATVIEW {
		kind = schema.KindMatView
	}
	if s.Lookup(q) != nil && c.GetIfNotExists() {
		return
	}
	s.defineView(q, kind, c.GetQuery(), strs(c.GetInto().GetColNames()), false)
	if kind == schema.KindTable {
		if r := s.Lookup(q); r != nil {
			r.DependsOn = nil
		}
	}
}

func (s *State) defineView(q QName, kind schema.RelKind, query *pg_query.Node, aliases []string, replace bool) {
	deps := s.RangeVars(query)
	cols := s.selectColumns(query, deps)
	for i := range cols {
		if i < len(aliases) {
			cols[i].Name = aliases[i]
		}
	}
	if existing := s.Lookup(q); existing != nil {
		if !replace {
			s.warnf("CREATE VIEW %s: the relation already exists", q)
			return
		}
		existing.Columns, existing.DependsOn = cols, deps
		return
	}
	s.Model.Relations = append(s.Model.Relations, schema.Relation{
		Schema: q.Schema, Name: q.Name, Kind: kind, Columns: cols, DependsOn: deps,
	})
	s.created[q] = true
}

// selectColumns names the output columns of a SELECT, taking the type from
// the base column when an output is a plain column reference.
func (s *State) selectColumns(query *pg_query.Node, deps []QName) []schema.Column {
	sel := query.GetSelectStmt()
	for sel != nil && sel.GetLarg() != nil {
		sel = sel.GetLarg()
	}
	cols := []schema.Column{}
	if sel == nil {
		return cols
	}
	find := func(name string) *schema.Column {
		var found *schema.Column
		for _, d := range deps {
			if r := s.Lookup(d); r != nil {
				if c := r.Column(name); c != nil {
					if found != nil {
						return nil // ambiguous
					}
					found = c
				}
			}
		}
		return found
	}
	for _, t := range sel.GetTargetList() {
		rt := t.GetResTarget()
		name, typ := rt.GetName(), "unknown"
		if cr := rt.GetVal().GetColumnRef(); cr != nil {
			f := cr.GetFields()
			if len(f) > 0 && f[len(f)-1].GetAStar() != nil {
				if len(deps) == 1 {
					if r := s.Lookup(deps[0]); r != nil {
						for _, c := range r.Columns {
							cols = append(cols, schema.Column{Name: c.Name, Type: c.Type, Nullable: true})
						}
					}
				}
				continue
			}
			last := f[len(f)-1].GetString_().GetSval()
			if name == "" {
				name = last
			}
			if c := find(last); c != nil {
				typ = c.Type
			}
		} else if fc := rt.GetVal().GetFuncCall(); fc != nil && name == "" {
			_, name = QualifiedFromList(fc.GetFuncname())
		}
		if name == "" {
			name = "?column?"
		}
		cols = append(cols, schema.Column{Name: name, Type: typ, Nullable: true})
	}
	return cols
}

// ---- enums and comments ----

func (s *State) alterEnum(a *pg_query.AlterEnumStmt) {
	nsp, name := QualifiedFromList(a.GetTypeName())
	e := s.Enum(nsp, name)
	if e == nil {
		s.warnf("ALTER TYPE %s: the enum is not in the schema", name)
		return
	}
	if a.GetOldVal() != "" {
		for i, v := range e.Values {
			if v == a.GetOldVal() {
				e.Values[i] = a.GetNewVal()
			}
		}
		return
	}
	if slices.Contains(e.Values, a.GetNewVal()) {
		return
	}
	at := len(e.Values)
	if n := a.GetNewValNeighbor(); n != "" {
		if i := slices.Index(e.Values, n); i >= 0 {
			at = i
			if a.GetNewValIsAfter() {
				at = i + 1
			}
		}
	}
	e.Values = slices.Insert(e.Values, at, a.GetNewVal())
}

func (s *State) comment(c *pg_query.CommentStmt) {
	items := c.GetObject().GetList().GetItems()
	switch c.GetObjtype() {
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW:
		nsp, name := QualifiedFromList(items)
		if r := s.Lookup(s.resolveName(nsp, name)); r != nil {
			r.Comment = c.GetComment()
		}
	case pg_query.ObjectType_OBJECT_COLUMN:
		names := strs(items)
		if len(names) < 2 {
			return
		}
		col := names[len(names)-1]
		nsp, name := QualifiedFromList(items[:len(items)-1])
		if r := s.Lookup(s.resolveName(nsp, name)); r != nil {
			if cc := r.Column(col); cc != nil {
				cc.Comment = c.GetComment()
			}
		}
	}
}

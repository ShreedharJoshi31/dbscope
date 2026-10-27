package rules

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/schema"
)

type QName = apply.QName

// Class says how a finding's rating is decided (docs/MIGRATIONS.md, Overrides).
type Class string

const (
	// Risk findings take the statement's score band.
	Risk Class = "risk"
	// Fail: PostgreSQL will reject the statement -> Critical.
	Fail Class = "fail"
	// DataLoss on a table that may have rows -> Critical.
	DataLoss Class = "data_loss"
	// Breaking: at least High when something depends on the object, else Medium.
	Breaking Class = "breaking"
	// Info is always Low.
	Info Class = "info"
	// Backfill is at least Medium.
	Backfill Class = "backfill"
)

// Finding is one problem with a statement.
type Finding struct {
	ID      string
	Class   Class
	Message string
	Safe    string
	// Subject is the relation (and column) a breaking change is about, used
	// to look up dependents.
	Subject *Subject
}

// Subject names what a breaking change or drop affects.
type Subject struct {
	Relation QName
	Column   string
	// Restrict marks a DROP COLUMN without CASCADE. Column-level view and
	// policy dependents are only known from the live catalog (impact), so
	// "will fail" is decided there; table and view drops are decided here.
	Restrict bool
}

// LockOn is a lock on one relation.
type LockOn struct {
	Relation QName
	Mode     Mode
	// Index marks a lock on an index rather than a table.
	Index bool
}

// SizeSpec says which storage drives the statement's run time: the heap of
// Rel, plus its indexes for a rewrite, or only indexes for REINDEX.
type SizeSpec struct {
	Rel     QName
	Heap    bool
	Indexes bool
	// Index limits Indexes to the one named index.
	Index string
}

// Effect is what a statement does.
type Effect struct {
	Kind     string
	Keys     []string
	Target   *QName
	Locks    []LockOn
	Work     Work
	Size     SizeSpec
	InTx     bool
	Findings []Finding
	// Unknown are relations the statement names that are not in the schema.
	Unknown []QName
	// AddsEnumValue marks ALTER TYPE ... ADD VALUE.
	AddsEnumValue bool
}

// Classify describes n against st, which must not yet include n's effect.
func Classify(n *pg_query.Node, st *apply.State, major int) *Effect {
	c := &classifier{st: st, major: major, e: &Effect{InTx: true}}
	c.statement(n)
	if c.e.Kind == "" {
		c.e.Kind = label(apply.NodeName(n))
	}
	return c.e
}

type classifier struct {
	st    *apply.State
	major int
	e     *Effect
	kinds []string
}

// ctx carries the relations a rule's roles refer to and the text variables.
type ctx struct {
	target     QName
	referenced []QName
	partition  QName
	parent     QName
	index      string
	indexes    []string
	baseTables []QName
	mode       Mode // LOCK TABLE
	workOn     *QName
	vars       map[string]string
	subject    *Subject
	class      Class
}

func (c *classifier) use(key string, x ctx) {
	r, err := Lookup(key, c.major)
	if err != nil {
		panic(err) // the table covers every key the classifier emits (tested)
	}
	c.e.Keys = append(c.e.Keys, key)
	if r.Kind != "" && !slices.Contains(c.kinds, r.Kind) {
		c.kinds = append(c.kinds, r.Kind)
		c.e.Kind = strings.Join(c.kinds, ", ")
	}
	locks := r.Locks
	if key == "lock_table" {
		locks = []Lock{{Target, x.mode}}
	}
	for _, l := range locks {
		for _, rel := range c.relations(l.Role, x) {
			c.lock(rel, l.Mode, l.Role == Index || l.Role == Indexes)
		}
	}
	if r.Work > c.e.Work || (r.Work == c.e.Work && c.e.Size.Rel == (QName{})) {
		c.e.Work = r.Work
		on := x.target
		if x.workOn != nil {
			on = *x.workOn
		}
		c.e.Size = SizeSpec{Rel: on, Heap: true, Indexes: r.Work == Rewrite}
		if key == "reindex" || key == "reindex.concurrently" {
			c.e.Size = SizeSpec{Rel: on, Indexes: true, Index: x.index}
		}
	}
	if !r.InTx {
		c.e.InTx = false
	}
	vars := map[string]string{"table": x.target.String(), "index": x.index, "partition": x.partition.String()}
	if len(x.referenced) > 0 {
		vars["ref"] = joinNames(x.referenced)
	}
	for k, v := range x.vars {
		vars[k] = v
	}
	fails := r.Fails
	if fails == "" && r.FailsWithRows != "" && !c.st.Empty(x.target) {
		fails = r.FailsWithRows
	}
	if r.Finding == "" || (r.FailsWithRows != "" && fails == "") {
		return
	}
	class := x.class
	if class == "" {
		class = Risk
		if fails != "" {
			class = Fail
		}
	}
	c.e.Findings = append(c.e.Findings, Finding{ID: r.Finding, Class: class, Message: Text(r.Message, vars),
		Safe: Text(r.Safe, vars), Subject: x.subject})
}

func (c *classifier) relations(role Role, x ctx) []QName {
	switch role {
	case Target:
		if x.target == (QName{}) {
			return nil
		}
		return []QName{x.target}
	case Referenced:
		return x.referenced
	case Referencing:
		owner := x.target
		if x.parent != (QName{}) {
			owner = x.parent // CREATE TABLE ... PARTITION OF: tables referencing the parent
		}
		return slices.DeleteFunc(c.st.Referencing(owner), func(q QName) bool { return q == owner })
	case Partition:
		if x.partition == (QName{}) {
			return nil
		}
		return []QName{x.partition}
	case DefaultPartition:
		owner := x.target
		if x.parent != (QName{}) {
			owner = x.parent
		}
		if d, ok := c.st.Catalog.DefaultPartitions[owner.String()]; ok && d != x.partition {
			return []QName{d}
		}
	case Partitions:
		if r := c.st.Lookup(x.target); r != nil {
			return r.Partitions
		}
	case Index:
		if x.index != "" {
			return []QName{{Schema: x.target.Schema, Name: x.index}}
		}
	case Indexes:
		var out []QName
		for _, ix := range x.indexes {
			out = append(out, QName{Schema: x.target.Schema, Name: ix})
		}
		return out
	case BaseTables:
		return x.baseTables
	case Parent:
		if x.parent != (QName{}) {
			return []QName{x.parent}
		}
	case Dependents:
		return c.st.Dependents(x.target)
	case Clustered:
		var out []QName
		for k := range c.st.Catalog.Clustered {
			out = append(out, splitQName(k))
		}
		slices.SortFunc(out, func(a, b QName) int { return strings.Compare(a.String(), b.String()) })
		return out
	}
	return nil
}

func (c *classifier) lock(q QName, m Mode, index bool) {
	for i := range c.e.Locks {
		if c.e.Locks[i].Relation == q {
			c.e.Locks[i].Mode = max(c.e.Locks[i].Mode, m)
			return
		}
	}
	c.e.Locks = append(c.e.Locks, LockOn{Relation: q, Mode: m, Index: index})
}

func (c *classifier) known(q QName) {
	if c.st.Lookup(q) == nil && c.st.PartitionParent(q) == nil && !slices.Contains(c.e.Unknown, q) {
		c.e.Unknown = append(c.e.Unknown, q)
	}
}

func (c *classifier) setTarget(q QName) {
	if c.e.Target == nil {
		c.e.Target = &q
	}
	c.known(q)
}

func splitQName(k string) QName {
	nsp, name, _ := strings.Cut(k, ".")
	return QName{Schema: nsp, Name: name}
}

func joinNames(qs []QName) string {
	s := make([]string, len(qs))
	for i, q := range qs {
		s[i] = q.String()
	}
	return strings.Join(s, ", ")
}

func (c *classifier) statement(n *pg_query.Node) {
	switch {
	case n.GetAlterTableStmt() != nil:
		c.alterTable(n.GetAlterTableStmt())
	case n.GetIndexStmt() != nil:
		c.createIndex(n.GetIndexStmt())
	case n.GetDropStmt() != nil:
		c.drop(n.GetDropStmt())
	case n.GetRenameStmt() != nil:
		c.rename(n.GetRenameStmt())
	case n.GetCreateStmt() != nil:
		c.createTable(n.GetCreateStmt())
	case n.GetTruncateStmt() != nil:
		t := n.GetTruncateStmt()
		key := "truncate"
		if t.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE {
			key = "truncate.cascade"
		}
		for _, r := range t.GetRelations() {
			q := c.st.Resolve(r.GetRangeVar())
			c.setTarget(q)
			class := DataLoss
			if c.st.Empty(q) {
				class = Breaking
			}
			c.use(key, ctx{target: q, class: class, subject: &Subject{Relation: q}})
		}
	case n.GetVacuumStmt() != nil:
		c.vacuum(n.GetVacuumStmt())
	case n.GetClusterStmt() != nil:
		cl := n.GetClusterStmt()
		if cl.GetRelation() == nil {
			c.use("cluster.all", ctx{})
			return
		}
		q := c.st.Resolve(cl.GetRelation())
		c.setTarget(q)
		c.use("cluster", ctx{target: q})
	case n.GetReindexStmt() != nil:
		c.reindex(n.GetReindexStmt())
	case n.GetRefreshMatViewStmt() != nil:
		r := n.GetRefreshMatViewStmt()
		q := c.st.Resolve(r.GetRelation())
		c.setTarget(q)
		key := "refresh_matview"
		if r.GetConcurrent() {
			key = "refresh_matview.concurrently"
		}
		x := ctx{target: q}
		if mv := c.st.Lookup(q); mv != nil {
			x.baseTables = mv.DependsOn
		}
		c.use(key, x)
	case n.GetCreateTrigStmt() != nil:
		q := c.st.Resolve(n.GetCreateTrigStmt().GetRelation())
		c.setTarget(q)
		c.use("create_trigger", ctx{target: q})
	case n.GetViewStmt() != nil:
		v := n.GetViewStmt()
		q := c.st.Resolve(v.GetView())
		x := ctx{target: q, baseTables: c.st.RangeVars(v.GetQuery())}
		if v.GetReplace() && c.st.Lookup(q) != nil {
			c.setTarget(q)
			c.use("create_view.replace", x)
		} else {
			c.e.Target = &q
			c.use("create_view", x)
		}
	case n.GetCreateTableAsStmt() != nil:
		ct := n.GetCreateTableAsStmt()
		q := c.st.Resolve(ct.GetInto().GetRel())
		c.e.Target = &q
		base := c.st.RangeVars(ct.GetQuery())
		x := ctx{target: q, baseTables: base}
		if len(base) > 0 {
			x.workOn = &base[0]
		}
		c.use("create_matview", x)
		if ct.GetObjtype() != pg_query.ObjectType_OBJECT_MATVIEW {
			c.e.Kind = "CREATE TABLE AS"
		}
	case n.GetCommentStmt() != nil:
		c.comment(n.GetCommentStmt())
	case n.GetLockStmt() != nil:
		l := n.GetLockStmt()
		mode := Mode(l.GetMode())
		for _, r := range l.GetRelations() {
			q := c.st.Resolve(r.GetRangeVar())
			c.setTarget(q)
			c.use("lock_table", ctx{target: q, mode: mode, vars: map[string]string{"detail": mode.String()}})
		}
	case n.GetAlterEnumStmt() != nil:
		a := n.GetAlterEnumStmt()
		if a.GetOldVal() != "" {
			c.use("alter_enum.rename_value", ctx{})
		} else {
			c.use("alter_enum.add_value", ctx{})
			c.e.AddsEnumValue = true
		}
	case n.GetAlterObjectSchemaStmt() != nil:
		a := n.GetAlterObjectSchemaStmt()
		if a.GetRelation() == nil {
			c.use("other", ctx{})
			return
		}
		q := c.st.Resolve(a.GetRelation())
		c.setTarget(q)
		c.use("set_schema", ctx{target: q, class: Breaking, subject: &Subject{Relation: q},
			vars: map[string]string{"detail": a.GetNewschema()}})
	case n.GetInsertStmt() != nil:
		c.dml(n.GetInsertStmt().GetRelation(), "INSERT", true)
	case n.GetUpdateStmt() != nil:
		u := n.GetUpdateStmt()
		c.dml(u.GetRelation(), "UPDATE", u.GetWhereClause() != nil)
	case n.GetDeleteStmt() != nil:
		d := n.GetDeleteStmt()
		c.dml(d.GetRelation(), "DELETE", d.GetWhereClause() != nil)
	case n.GetTransactionStmt() != nil, n.GetVariableSetStmt() != nil, n.GetSelectStmt() != nil:
		c.e.Kind = label(apply.NodeName(n))
		if t := n.GetTransactionStmt(); t != nil {
			c.e.Kind = txLabel(t.GetKind())
		}
		if v := n.GetVariableSetStmt(); v != nil && (v.GetKind() == pg_query.VariableSetKind_VAR_RESET || v.GetKind() == pg_query.VariableSetKind_VAR_RESET_ALL) {
			c.e.Kind = "RESET"
		}
		c.use("other", ctx{})
	case n.GetCreatedbStmt() != nil, n.GetDropdbStmt() != nil, n.GetAlterSystemStmt() != nil,
		n.GetCreateTableSpaceStmt() != nil, n.GetDropTableSpaceStmt() != nil:
		c.use("other.no_tx", ctx{})
	default:
		c.use("other", ctx{})
	}
}

func (c *classifier) dml(rv *pg_query.RangeVar, verb string, filtered bool) {
	q := c.st.Resolve(rv)
	c.setTarget(q)
	c.kinds = []string{verb}
	c.e.Kind = verb
	key := "dml"
	if !filtered && verb != "INSERT" && !c.st.Empty(q) {
		key = "dml.all_rows"
	}
	c.use(key, ctx{target: q, class: Backfill, vars: map[string]string{"detail": verb}})
	c.kinds = []string{verb}
	c.e.Kind = verb
}

// ---- ALTER TABLE ----

func (c *classifier) alterTable(a *pg_query.AlterTableStmt) {
	q := c.st.Resolve(a.GetRelation())
	if a.GetObjtype() == pg_query.ObjectType_OBJECT_INDEX {
		// ALTER INDEX ... SET/ATTACH: treat as an index-level change.
		rel, _ := c.st.FindIndex(a.GetRelation().GetSchemaname(), a.GetRelation().GetRelname())
		x := ctx{index: a.GetRelation().GetRelname()}
		if rel != nil {
			x.target = rel.QName()
		}
		c.use("rename_index", x)
		c.kinds = []string{"ALTER INDEX"}
		c.e.Kind = "ALTER INDEX"
		return
	}
	c.setTarget(q)
	rel := c.st.Lookup(q)
	for _, cn := range a.GetCmds() {
		c.alterCmd(q, rel, cn.GetAlterTableCmd())
	}
}

func (c *classifier) alterCmd(q QName, rel *schema.Relation, cmd *pg_query.AlterTableCmd) {
	x := ctx{target: q}
	col := cmd.GetName()
	x.vars = map[string]string{"column": col}
	switch cmd.GetSubtype() {
	case pg_query.AlterTableType_AT_AddColumn:
		c.addColumn(q, rel, cmd.GetDef().GetColumnDef())
	case pg_query.AlterTableType_AT_DropColumn:
		x.subject = &Subject{Relation: q, Column: col, Restrict: cmd.GetBehavior() != pg_query.DropBehavior_DROP_CASCADE}
		x.class = DataLoss
		if c.st.Empty(q) {
			x.class = Breaking
		}
		c.use("drop_column", x)
		if cmd.GetBehavior() != pg_query.DropBehavior_DROP_CASCADE {
			if refs := c.referencingColumn(q, col); len(refs) > 0 {
				c.fail(fmt.Sprintf("DROP COLUMN %s fails without CASCADE: foreign keys on %s reference it", col, joinNames(refs)),
					"Drop the referencing foreign keys first, or use CASCADE knowingly")
			}
		}
	case pg_query.AlterTableType_AT_AlterColumnType:
		c.alterType(q, rel, col, cmd.GetDef().GetColumnDef())
	case pg_query.AlterTableType_AT_ColumnDefault:
		if cmd.GetDef() == nil {
			c.use("drop_default", x)
		} else {
			c.use("set_default", x)
		}
	case pg_query.AlterTableType_AT_SetNotNull:
		if rel != nil && provesNotNull(rel, col) {
			c.use("set_not_null.proven", x)
		} else {
			c.use("set_not_null", x)
		}
		if rel != nil && !c.st.Created(q) {
			if cc := rel.Column(col); cc == nil || cc.Nullable {
				c.e.Findings = append(c.e.Findings, Finding{ID: "DBS-NEW-NOT-NULL", Class: Breaking,
					Message: fmt.Sprintf("%s.%s becomes NOT NULL; writers that insert NULL will fail", q, col),
					Safe:    "Deploy the application change that always writes a value before this migration",
					Subject: &Subject{Relation: q, Column: col}})
			}
		}
	case pg_query.AlterTableType_AT_DropNotNull:
		c.use("drop_not_null", x)
	case pg_query.AlterTableType_AT_AddConstraint:
		c.addConstraint(q, rel, cmd.GetDef().GetConstraint())
	case pg_query.AlterTableType_AT_DropConstraint:
		if fk := findFK(rel, col); fk != nil {
			x.referenced = []QName{fk.Ref}
			c.use("drop_constraint.fk", x)
		} else {
			c.use("drop_constraint", x)
		}
	case pg_query.AlterTableType_AT_ValidateConstraint:
		if fk := findFK(rel, col); fk != nil {
			x.referenced = []QName{fk.Ref}
			c.use("validate_constraint.fk", x)
		} else {
			c.use("validate_constraint", x)
		}
	case pg_query.AlterTableType_AT_AddIdentity:
		c.use("add_identity", x)
	case pg_query.AlterTableType_AT_DropExpression:
		c.use("drop_expression", x)
	case pg_query.AlterTableType_AT_SetStatistics:
		c.use("set_statistics", x)
	case pg_query.AlterTableType_AT_AttachPartition:
		pc := cmd.GetDef().GetPartitionCmd()
		x.partition = c.st.Resolve(pc.GetName())
		x.workOn = &x.partition
		c.known(x.partition)
		c.use("attach_partition", x)
	case pg_query.AlterTableType_AT_DetachPartition:
		pc := cmd.GetDef().GetPartitionCmd()
		x.partition = c.st.Resolve(pc.GetName())
		x.subject = &Subject{Relation: x.partition}
		if pc.GetConcurrent() {
			c.use("detach_partition.concurrently", x)
			if _, ok := c.st.Catalog.DefaultPartitions[q.String()]; ok {
				c.fail(fmt.Sprintf("DETACH PARTITION CONCURRENTLY fails: %s has a default partition", q),
					"Detach without CONCURRENTLY in a quiet window, or remove the default partition first")
			}
		} else {
			c.use("detach_partition", x)
		}
	case pg_query.AlterTableType_AT_DetachPartitionFinalize:
		x.partition = c.st.Resolve(cmd.GetDef().GetPartitionCmd().GetName())
		c.use("detach_partition.finalize", x)
	case pg_query.AlterTableType_AT_SetTableSpace:
		c.use("set_tablespace", x)
	case pg_query.AlterTableType_AT_SetLogged, pg_query.AlterTableType_AT_SetUnLogged:
		c.use("set_logged", x)
	case pg_query.AlterTableType_AT_SetAccessMethod:
		c.use("set_access_method", x)
	case pg_query.AlterTableType_AT_SetRelOptions, pg_query.AlterTableType_AT_ResetRelOptions:
		c.use("set_options", x)
	case pg_query.AlterTableType_AT_ClusterOn, pg_query.AlterTableType_AT_DropCluster:
		c.use("cluster_on", x)
	case pg_query.AlterTableType_AT_EnableTrig, pg_query.AlterTableType_AT_EnableAlwaysTrig, pg_query.AlterTableType_AT_EnableReplicaTrig,
		pg_query.AlterTableType_AT_DisableTrig, pg_query.AlterTableType_AT_EnableTrigAll, pg_query.AlterTableType_AT_DisableTrigAll,
		pg_query.AlterTableType_AT_EnableTrigUser, pg_query.AlterTableType_AT_DisableTrigUser:
		c.use("enable_trigger", x)
	default:
		c.use("alter_table", x)
	}
}

func (c *classifier) fail(msg, safe string) {
	c.e.Findings = append(c.e.Findings, Finding{ID: "DBS-WILL-FAIL", Class: Fail, Message: msg, Safe: safe})
}

// referencingColumn lists tables whose foreign keys reference q.col.
func (c *classifier) referencingColumn(q QName, col string) []QName {
	var out []QName
	for _, r := range c.st.Model.Relations {
		for _, fk := range r.ForeignKeys {
			if fk.Ref == q && r.QName() != q && !fk.Inferred && slices.Contains(fk.RefCols, col) && !slices.Contains(out, r.QName()) {
				out = append(out, r.QName())
			}
		}
	}
	return out
}

func findFK(rel *schema.Relation, name string) *schema.ForeignKey {
	if rel == nil {
		return nil
	}
	for i := range rel.ForeignKeys {
		if rel.ForeignKeys[i].Name == name {
			return &rel.ForeignKeys[i]
		}
	}
	return nil
}

func (c *classifier) addColumn(q QName, rel *schema.Relation, cd *pg_query.ColumnDef) {
	t := apply.TypeFromAST(cd.GetTypeName())
	x := ctx{target: q, vars: map[string]string{"column": cd.GetColname()}}
	var def *pg_query.Node = cd.GetRawDefault()
	notNull, identity, generated := cd.GetIsNotNull(), false, false
	var extra []*pg_query.Constraint
	for _, cn := range cd.GetConstraints() {
		con := cn.GetConstraint()
		switch con.GetContype() {
		case pg_query.ConstrType_CONSTR_DEFAULT:
			def = con.GetRawExpr()
		case pg_query.ConstrType_CONSTR_NOTNULL:
			notNull = true
		case pg_query.ConstrType_CONSTR_IDENTITY:
			identity = true
		case pg_query.ConstrType_CONSTR_GENERATED:
			generated = true
		case pg_query.ConstrType_CONSTR_PRIMARY:
			notNull = true
			extra = append(extra, con)
		case pg_query.ConstrType_CONSTR_UNIQUE, pg_query.ConstrType_CONSTR_CHECK, pg_query.ConstrType_CONSTR_FOREIGN:
			extra = append(extra, con)
		}
	}
	switch {
	case generated:
		c.use("add_column.generated_stored", x)
	case identity:
		c.use("add_column.identity", x)
	case t.Serial:
		x.vars["detail"] = strings.ToLower(namesOf(cd.GetTypeName()))
		c.use("add_column.serial", x)
	case def != nil:
		if v := ExprVolatility(def); v.Volatile {
			detail := strings.Join(v.Functions, ", ")
			if len(v.Assumed) > 0 {
				detail = strings.TrimPrefix(detail+", ", ", ") + strings.Join(v.Assumed, ", ") +
					" (not a known built-in, so dbscope assumes VOLATILE)"
			}
			x.vars["detail"] = detail
			c.use("add_column.volatile_default", x)
		} else if c.st.ConstrainedDomain(t) {
			x.vars["detail"] = t.String()
			c.use("add_column.domain", x)
		} else {
			c.use("add_column.default", x)
		}
	case c.st.ConstrainedDomain(t):
		x.vars["detail"] = t.String()
		c.use("add_column.domain", x)
	case notNull:
		c.use("add_column.not_null_no_default", x)
	default:
		c.use("add_column", x)
	}
	for _, con := range extra {
		con = cloneCon(con)
		if len(con.GetKeys()) == 0 {
			con.Keys = []*pg_query.Node{pg_query.MakeStrNode(cd.GetColname())}
		}
		c.addConstraint(q, rel, con)
	}
}

func namesOf(tn *pg_query.TypeName) string {
	var s []string
	for _, n := range tn.GetNames() {
		s = append(s, n.GetString_().GetSval())
	}
	return strings.Join(s, ".")
}

func cloneCon(con *pg_query.Constraint) *pg_query.Constraint {
	return &pg_query.Constraint{Contype: con.GetContype(), Conname: con.GetConname(), SkipValidation: con.GetSkipValidation(),
		InitiallyValid: con.GetInitiallyValid(), RawExpr: con.GetRawExpr(), Keys: con.GetKeys(), Indexname: con.GetIndexname(),
		Pktable: con.GetPktable(), FkAttrs: con.GetFkAttrs(), PkAttrs: con.GetPkAttrs()}
}

func (c *classifier) alterType(q QName, rel *schema.Relation, col string, cd *pg_query.ColumnDef) {
	newT := apply.TypeFromAST(cd.GetTypeName())
	var oldT apply.Type
	if rel != nil {
		if cc := rel.Column(col); cc != nil {
			oldT = apply.ParseType(cc.Type)
		}
	}
	ch := apply.TypeChange(oldT, newT)
	if using := cd.GetRawDefault(); using != nil && !trivialUsing(using, col) {
		ch.Rewrite = true
		ch.Reason = "the USING expression computes every value"
	}
	x := ctx{target: q, vars: map[string]string{"column": col, "detail": ch.Reason}}
	if ch.Rewrite {
		c.use("alter_type.rewrite", x)
	} else {
		c.use("alter_type.no_rewrite", x)
	}
	if ch.Narrowing && !c.st.Created(q) {
		c.e.Findings = append(c.e.Findings, Finding{ID: "DBS-TYPE-NARROWING", Class: Breaking,
			Message: fmt.Sprintf("%s.%s: %s", q, col, ch.Reason),
			Safe:    "Check that every existing value fits and that writers are updated first",
			Subject: &Subject{Relation: q, Column: col}})
	}
}

// trivialUsing reports USING col or USING col::type, which PostgreSQL treats
// like no USING clause.
func trivialUsing(n *pg_query.Node, col string) bool {
	if tc := n.GetTypeCast(); tc != nil {
		n = tc.GetArg()
	}
	cr := n.GetColumnRef()
	if cr == nil {
		return false
	}
	f := cr.GetFields()
	return len(f) == 1 && f[0].GetString_().GetSval() == col
}

func (c *classifier) addConstraint(q QName, rel *schema.Relation, con *pg_query.Constraint) {
	x := ctx{target: q, vars: map[string]string{"column": strings.Join(conKeys(con), ", ")}}
	partitioned := rel != nil && rel.Kind == schema.KindPartitionedTable
	switch con.GetContype() {
	case pg_query.ConstrType_CONSTR_FOREIGN:
		ref := c.st.Resolve(con.GetPktable())
		c.known(ref)
		x.referenced = []QName{ref}
		x.vars["detail"] = strconv.Itoa(c.major)
		switch {
		case con.GetSkipValidation() && partitioned:
			c.use("add_fk.not_valid.partitioned", x)
		case con.GetSkipValidation(), c.st.Created(q):
			c.use("add_fk.not_valid", x)
		default:
			c.use("add_fk", x)
		}
	case pg_query.ConstrType_CONSTR_CHECK:
		if con.GetSkipValidation() || c.st.Created(q) {
			c.use("add_check.not_valid", x)
		} else {
			c.use("add_check", x)
		}
	case pg_query.ConstrType_CONSTR_UNIQUE:
		if con.GetIndexname() != "" {
			c.use("add_unique.using_index", x)
		} else {
			c.use("add_unique", x)
		}
	case pg_query.ConstrType_CONSTR_PRIMARY:
		if con.GetIndexname() == "" {
			c.use("add_pk", x)
			return
		}
		var cols []string
		if rel != nil {
			for _, ix := range rel.Indexes {
				if ix.Name == con.GetIndexname() {
					cols = ix.Columns
				}
			}
		}
		var nullable []string
		for _, k := range cols {
			if cc := rel.Column(k); cc != nil && cc.Nullable {
				nullable = append(nullable, k)
			}
		}
		if len(nullable) > 0 {
			x.vars["column"] = strings.Join(nullable, ", ")
			c.use("add_pk.using_index.nullable", x)
		} else {
			c.use("add_pk.using_index", x)
		}
	case pg_query.ConstrType_CONSTR_EXCLUSION:
		c.use("add_exclude", x)
	default:
		c.use("alter_table", x)
	}
}

func conKeys(con *pg_query.Constraint) []string {
	var out []string
	for _, k := range append(con.GetKeys(), con.GetFkAttrs()...) {
		out = append(out, k.GetString_().GetSval())
	}
	return out
}

// provesNotNull reports a validated CHECK constraint of the form
// "col IS NOT NULL" (possibly AND-ed with other conditions), which lets
// SET NOT NULL skip its scan (PostgreSQL 12+).
func provesNotNull(rel *schema.Relation, col string) bool {
	for _, ck := range rel.Checks {
		if !ck.Validated {
			continue
		}
		tree, err := pgquery.Parse("SELECT " + ck.Expr)
		if err != nil || len(tree.GetStmts()) != 1 {
			continue
		}
		targets := tree.GetStmts()[0].GetStmt().GetSelectStmt().GetTargetList()
		if len(targets) == 1 && conjunctProves(targets[0].GetResTarget().GetVal(), col) {
			return true
		}
	}
	return false
}

func conjunctProves(n *pg_query.Node, col string) bool {
	if b := n.GetBoolExpr(); b != nil && b.GetBoolop() == pg_query.BoolExprType_AND_EXPR {
		for _, a := range b.GetArgs() {
			if conjunctProves(a, col) {
				return true
			}
		}
		return false
	}
	nt := n.GetNullTest()
	if nt == nil || nt.GetNulltesttype() != pg_query.NullTestType_IS_NOT_NULL {
		return false
	}
	f := nt.GetArg().GetColumnRef().GetFields()
	return len(f) > 0 && f[len(f)-1].GetString_().GetSval() == col
}

// ---- other statements ----

func (c *classifier) createIndex(ix *pg_query.IndexStmt) {
	q := c.st.Resolve(ix.GetRelation())
	c.setTarget(q)
	rel := c.st.Lookup(q)
	partitioned := rel != nil && rel.Kind == schema.KindPartitionedTable && ix.GetRelation().GetInh()
	x := ctx{target: q}
	switch {
	case ix.GetConcurrent() && partitioned:
		c.use("create_index.concurrently.partitioned", x)
	case ix.GetConcurrent():
		c.use("create_index.concurrently", x)
	case c.st.Created(q):
		// An index on a table created by this migration blocks nobody.
		c.useQuiet("create_index", x)
	case partitioned:
		c.use("create_index.partitioned", x)
	default:
		c.use("create_index", x)
	}
}

// useQuiet applies a rule without its finding.
func (c *classifier) useQuiet(key string, x ctx) {
	n := len(c.e.Findings)
	c.use(key, x)
	c.e.Findings = c.e.Findings[:n]
}

func (c *classifier) drop(d *pg_query.DropStmt) {
	cascade := d.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE
	switch d.GetRemoveType() {
	case pg_query.ObjectType_OBJECT_INDEX:
		if d.GetConcurrent() && (len(d.GetObjects()) > 1 || cascade) {
			c.fail("DROP INDEX CONCURRENTLY takes exactly one index and no CASCADE", "Drop each index in its own statement")
		}
		for _, obj := range d.GetObjects() {
			nsp, name := apply.QualifiedFromList(obj.GetList().GetItems())
			rel, _ := c.st.FindIndex(nsp, name)
			x := ctx{index: name}
			if rel != nil {
				x.target = rel.QName()
				c.setTarget(x.target)
			} else if !d.GetMissingOk() {
				c.e.Unknown = append(c.e.Unknown, QName{Schema: cmpOr(nsp, firstOf(c.st.SearchPath)), Name: name})
			}
			switch {
			case d.GetConcurrent():
				c.use("drop_index.concurrently", x)
			case rel != nil && c.st.Created(rel.QName()):
				c.useQuiet("drop_index", x)
			default:
				c.use("drop_index", x)
			}
		}
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_FOREIGN_TABLE:
		for _, obj := range d.GetObjects() {
			nsp, name := apply.QualifiedFromList(obj.GetList().GetItems())
			q := c.resolve(nsp, name)
			c.setTarget(q)
			x := ctx{target: q, subject: &Subject{Relation: q}, class: DataLoss}
			if c.st.Empty(q) {
				x.class = Breaking
			}
			if rel := c.st.Lookup(q); rel != nil {
				for _, fk := range rel.ForeignKeys {
					if fk.Ref != q && !fk.Inferred && !slices.Contains(x.referenced, fk.Ref) {
						x.referenced = append(x.referenced, fk.Ref)
					}
				}
			}
			key := "drop_table"
			if cascade {
				key = "drop_table.cascade"
			}
			c.use(key, x)
			if !cascade {
				deps := append(slices.DeleteFunc(c.st.Referencing(q), func(r QName) bool { return r == q }), c.st.Dependents(q)...)
				if len(deps) > 0 {
					c.fail(fmt.Sprintf("DROP TABLE %s fails without CASCADE: %s depend on it", q, joinNames(deps)),
						"Drop or change the dependent objects first, or use CASCADE knowingly")
				}
			}
		}
	case pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW:
		word := "VIEW"
		if d.GetRemoveType() == pg_query.ObjectType_OBJECT_MATVIEW {
			word = "MATERIALIZED VIEW"
		}
		for _, obj := range d.GetObjects() {
			nsp, name := apply.QualifiedFromList(obj.GetList().GetItems())
			q := c.resolve(nsp, name)
			c.setTarget(q)
			c.use("drop_view", ctx{target: q, class: Breaking, subject: &Subject{Relation: q}, vars: map[string]string{"detail": word}})
			c.kinds = []string{"DROP " + word}
			c.e.Kind = "DROP " + word
			if deps := c.st.Dependents(q); !cascade && len(deps) > 0 {
				c.fail(fmt.Sprintf("DROP %s %s fails without CASCADE: %s depend on it", word, q, joinNames(deps)),
					"Drop the dependent views first, or use CASCADE knowingly")
			}
		}
	case pg_query.ObjectType_OBJECT_TRIGGER:
		for _, obj := range d.GetObjects() {
			items := obj.GetList().GetItems()
			if len(items) < 2 {
				continue
			}
			nsp, name := apply.QualifiedFromList(items[:len(items)-1])
			q := c.resolve(nsp, name)
			c.setTarget(q)
			c.use("drop_trigger", ctx{target: q})
		}
	default:
		c.use("other", ctx{})
		c.e.Kind = "DROP " + strings.TrimPrefix(d.GetRemoveType().String(), "OBJECT_")
	}
}

func (c *classifier) resolve(nsp, name string) QName {
	return c.st.Resolve(&pg_query.RangeVar{Schemaname: nsp, Relname: name})
}

func firstOf(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return "public"
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c *classifier) rename(r *pg_query.RenameStmt) {
	switch r.GetRenameType() {
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW,
		pg_query.ObjectType_OBJECT_FOREIGN_TABLE:
		q := c.st.Resolve(r.GetRelation())
		c.setTarget(q)
		x := ctx{target: q, class: Breaking, subject: &Subject{Relation: q}, vars: map[string]string{"detail": r.GetNewname()}}
		if c.st.Created(q) {
			c.useQuiet("rename_table", x)
		} else {
			c.use("rename_table", x)
		}
	case pg_query.ObjectType_OBJECT_COLUMN:
		q := c.st.Resolve(r.GetRelation())
		c.setTarget(q)
		x := ctx{target: q, class: Breaking, subject: &Subject{Relation: q, Column: r.GetSubname()},
			vars: map[string]string{"column": r.GetSubname(), "detail": r.GetNewname()}}
		if c.st.Created(q) {
			c.useQuiet("rename_column", x)
		} else {
			c.use("rename_column", x)
		}
	case pg_query.ObjectType_OBJECT_INDEX:
		rel, _ := c.st.FindIndex(r.GetRelation().GetSchemaname(), r.GetRelation().GetRelname())
		x := ctx{index: r.GetRelation().GetRelname()}
		if rel != nil {
			x.target = rel.QName()
			c.setTarget(x.target)
		}
		c.use("rename_index", x)
	case pg_query.ObjectType_OBJECT_TABCONSTRAINT:
		q := c.st.Resolve(r.GetRelation())
		c.setTarget(q)
		c.use("rename_constraint", ctx{target: q})
	default:
		c.use("other", ctx{})
		c.e.Kind = "ALTER " + strings.TrimPrefix(r.GetRenameType().String(), "OBJECT_") + " RENAME"
	}
}

func (c *classifier) createTable(ct *pg_query.CreateStmt) {
	q := c.st.Resolve(ct.GetRelation())
	c.e.Target = &q
	if ct.GetPartbound() != nil && len(ct.GetInhRelations()) == 1 {
		parent := c.st.Resolve(ct.GetInhRelations()[0].GetRangeVar())
		c.known(parent)
		x := ctx{target: q, parent: parent}
		if c.st.Created(parent) {
			c.useQuiet("create_table.partition_of", x)
		} else {
			c.use("create_table.partition_of", x)
		}
		return
	}
	var refs []QName
	collect := func(con *pg_query.Constraint) {
		if con.GetContype() == pg_query.ConstrType_CONSTR_FOREIGN {
			ref := c.st.Resolve(con.GetPktable())
			if ref != q && !slices.Contains(refs, ref) {
				refs = append(refs, ref)
				c.known(ref)
			}
		}
	}
	for _, elt := range ct.GetTableElts() {
		if con := elt.GetConstraint(); con != nil {
			collect(con)
		}
		for _, cn := range elt.GetColumnDef().GetConstraints() {
			collect(cn.GetConstraint())
		}
	}
	if len(refs) > 0 {
		c.use("create_table.fk", ctx{target: q, referenced: refs})
	} else {
		c.use("create_table", ctx{target: q})
	}
}

func (c *classifier) vacuum(v *pg_query.VacuumStmt) {
	full, analyzeOnly := false, !v.GetIsVacuumcmd()
	for _, o := range v.GetOptions() {
		if o.GetDefElem().GetDefname() == "full" {
			full = true
		}
	}
	key := "vacuum"
	switch {
	case full:
		key = "vacuum_full"
	case analyzeOnly:
		key = "analyze"
	}
	if len(v.GetRels()) == 0 {
		c.use(key, ctx{})
		return
	}
	for _, r := range v.GetRels() {
		q := c.st.Resolve(r.GetVacuumRelation().GetRelation())
		c.setTarget(q)
		c.use(key, ctx{target: q})
	}
}

func (c *classifier) reindex(r *pg_query.ReindexStmt) {
	concurrent := false
	for _, p := range r.GetParams() {
		if p.GetDefElem().GetDefname() == "concurrently" {
			concurrent = true
		}
	}
	switch r.GetKind() {
	case pg_query.ReindexObjectType_REINDEX_OBJECT_INDEX:
		rel, _ := c.st.FindIndex(r.GetRelation().GetSchemaname(), r.GetRelation().GetRelname())
		x := ctx{index: r.GetRelation().GetRelname(), indexes: []string{r.GetRelation().GetRelname()},
			vars: map[string]string{"detail": "index " + r.GetRelation().GetRelname()}}
		if rel != nil {
			x.target = rel.QName()
			c.setTarget(x.target)
		}
		c.use(map[bool]string{false: "reindex", true: "reindex.concurrently"}[concurrent], x)
	case pg_query.ReindexObjectType_REINDEX_OBJECT_TABLE:
		q := c.st.Resolve(r.GetRelation())
		c.setTarget(q)
		x := ctx{target: q, vars: map[string]string{"detail": "every index of " + q.String()}}
		if rel := c.st.Lookup(q); rel != nil {
			for _, ix := range rel.Indexes {
				x.indexes = append(x.indexes, ix.Name)
			}
		}
		c.use(map[bool]string{false: "reindex", true: "reindex.concurrently"}[concurrent], x)
	default:
		c.use("reindex.schema", ctx{})
	}
}

func (c *classifier) comment(cm *pg_query.CommentStmt) {
	items := cm.GetObject().GetList().GetItems()
	switch cm.GetObjtype() {
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW:
		nsp, name := apply.QualifiedFromList(items)
		q := c.resolve(nsp, name)
		c.setTarget(q)
		c.use("comment", ctx{target: q})
	case pg_query.ObjectType_OBJECT_COLUMN:
		if len(items) >= 2 {
			nsp, name := apply.QualifiedFromList(items[:len(items)-1])
			q := c.resolve(nsp, name)
			c.setTarget(q)
			c.use("comment", ctx{target: q})
		}
	default:
		c.use("other", ctx{})
		c.e.Kind = "COMMENT ON"
	}
}

func txLabel(k pg_query.TransactionStmtKind) string {
	switch k {
	case pg_query.TransactionStmtKind_TRANS_STMT_BEGIN, pg_query.TransactionStmtKind_TRANS_STMT_START:
		return "BEGIN"
	case pg_query.TransactionStmtKind_TRANS_STMT_COMMIT:
		return "COMMIT"
	case pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK:
		return "ROLLBACK"
	case pg_query.TransactionStmtKind_TRANS_STMT_SAVEPOINT:
		return "SAVEPOINT"
	case pg_query.TransactionStmtKind_TRANS_STMT_RELEASE:
		return "RELEASE SAVEPOINT"
	case pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK_TO:
		return "ROLLBACK TO SAVEPOINT"
	}
	return "transaction control"
}

var labels = map[string]string{
	"VariableSetStmt": "SET", "SelectStmt": "SELECT", "CreateSchemaStmt": "CREATE SCHEMA",
	"CreateExtensionStmt": "CREATE EXTENSION", "CreateFunctionStmt": "CREATE FUNCTION", "CreateSeqStmt": "CREATE SEQUENCE",
	"AlterSeqStmt": "ALTER SEQUENCE", "CreateEnumStmt": "CREATE TYPE", "CreateDomainStmt": "CREATE DOMAIN",
	"GrantStmt": "GRANT/REVOKE", "DoStmt": "DO", "CreatePolicyStmt": "CREATE POLICY", "CreatedbStmt": "CREATE DATABASE",
	"AlterSystemStmt": "ALTER SYSTEM", "CreateTableSpaceStmt": "CREATE TABLESPACE", "DropTableSpaceStmt": "DROP TABLESPACE",
	"DropdbStmt": "DROP DATABASE", "CommentStmt": "COMMENT ON", "AlterOwnerStmt": "ALTER OWNER",
}

func label(name string) string {
	if l, ok := labels[name]; ok {
		return l
	}
	var b strings.Builder
	for i, r := range strings.TrimSuffix(name, "Stmt") {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

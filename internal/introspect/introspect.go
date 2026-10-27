// Package introspect reads a PostgreSQL database's structure from the system
// catalogs into a schema.Model. It only runs SELECTs, inside one
// REPEATABLE READ READ ONLY transaction, so the snapshot is consistent and a
// SELECT-only role is enough.
package introspect

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/schema"
)

// DB is satisfied by *pgx.Conn and *pgxpool.Pool.
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

type Namespace struct {
	Name          string `json:"name"`
	RelationCount int    `json:"relationCount"`
}

var txOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

type session struct {
	tx       pgx.Tx
	version  int
	defaults []string
}

// begin opens the read-only transaction and applies the statement timeout;
// a timeout of zero or less leaves the server's setting alone.
func begin(ctx context.Context, db DB, timeout time.Duration) (*session, error) {
	tx, err := db.BeginTx(ctx, txOptions)
	if err != nil {
		return nil, err
	}
	var setting *string // nil keeps the server's statement_timeout
	if ms := timeout.Milliseconds(); ms > 0 {
		v := strconv.FormatInt(ms, 10)
		setting = &v
	}
	s := &session{tx: tx}
	if err := tx.QueryRow(ctx, setupQuery, setting).Scan(&s.version, new(string), &s.defaults); err != nil {
		tx.Rollback(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("introspect setup: %w", err)
	}
	return s, nil
}

func (s *session) end(ctx context.Context) {
	s.tx.Rollback(context.WithoutCancel(ctx))
}

// Namespaces lists the non-system schemas the role has USAGE on, with the
// number of relations each holds (partitions not counted), sorted by name.
func Namespaces(ctx context.Context, db DB, timeout time.Duration) ([]Namespace, error) {
	s, err := begin(ctx, db, timeout)
	if err != nil {
		return nil, err
	}
	defer s.end(ctx)
	rows, err := s.tx.Query(ctx, namespacesQuery)
	if err != nil {
		return nil, fmt.Errorf("introspect namespaces: %w", err)
	}
	out := []Namespace{}
	var ns Namespace
	if _, err := pgx.ForEachRow(rows, []any{&ns.Name, &ns.RelationCount}, func() error {
		out = append(out, ns)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("introspect namespaces: %w", err)
	}
	return out, nil
}

type rel struct {
	r         schema.Relation
	partition bool
	root      uint32
	leaf      bool // a partition that holds rows itself (not partitioned again)
	reltuples float64
	pages     int64
}

type pendingFK struct {
	owner  uint32
	index  int
	refOID uint32
}

// partitionFK is a foreign key declared directly on a partition rather than
// inherited from the partitioned table.
type partitionFK struct {
	partition uint32
	refOID    uint32
	fk        schema.ForeignKey
}

// Introspect reads every relation in the given schemas. An empty list means
// every non-system schema the role has USAGE on; schemas named explicitly
// are read even without USAGE, since the catalogs are readable regardless.
// timeout becomes the transaction's statement_timeout.
func Introspect(ctx context.Context, db DB, schemas []string, timeout time.Duration) (*schema.Model, error) {
	start := time.Now()
	s, err := begin(ctx, db, timeout)
	if err != nil {
		return nil, err
	}
	defer s.end(ctx)

	selected := slices.Clone(schemas)
	if len(selected) == 0 {
		selected = s.defaults
	}
	slices.Sort(selected)
	selected = slices.Compact(selected)
	if selected == nil {
		selected = []string{}
	}

	b := &pgx.Batch{}
	for _, q := range []string{relationsQuery, columnsQuery, constraintsQuery, indexesQuery, viewDepsQuery, enumsQuery} {
		b.Queue(q, selected)
	}
	br := s.tx.SendBatch(ctx, b)
	a := &assembler{rels: map[uint32]*rel{}, version: s.version}
	steps := []struct {
		name string
		read func(pgx.Rows) error
	}{
		{"relations", a.relations}, {"columns", a.columns}, {"constraints", a.constraints},
		{"indexes", a.indexes}, {"view dependencies", a.viewDeps}, {"enums", a.enums},
	}
	for _, step := range steps {
		rows, err := br.Query()
		if err == nil {
			err = step.read(rows)
		}
		if err != nil {
			br.Close()
			return nil, fmt.Errorf("introspect %s: %w", step.name, err)
		}
	}
	if err := br.Close(); err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}

	a.promotePartitionFKs()
	if missing := a.missingRefs(); len(missing) > 0 {
		rows, err := s.tx.Query(ctx, stubsQuery, missing)
		if err == nil {
			err = a.stubs(rows)
		}
		if err != nil {
			return nil, fmt.Errorf("introspect stub relations: %w", err)
		}
	}

	m := a.model()
	m.ServerVersion = s.version
	m.Schemas = selected
	m.IntrospectedAt = start.UTC().Truncate(time.Millisecond)
	m.DurationMs = time.Since(start).Milliseconds()
	return m, nil
}

type assembler struct {
	version int
	order   []uint32
	rels    map[uint32]*rel
	fks     []pendingFK
	partFKs []partitionFK
	enumsV  []schema.Enum
}

var kinds = map[string]schema.RelKind{
	"r": schema.KindTable, "p": schema.KindPartitionedTable, "v": schema.KindView,
	"m": schema.KindMatView, "f": schema.KindForeignTable,
}

var fkActions = map[string]string{
	"a": "no action", "r": "restrict", "c": "cascade", "n": "set null", "d": "set default",
}

func (a *assembler) relations(rows pgx.Rows) error {
	var (
		oid, root                  uint32
		nsp, name, kind, key, comm string
		partition                  bool
		reltuples                  float64
		pages                      int64
	)
	_, err := pgx.ForEachRow(rows, []any{&oid, &nsp, &name, &kind, &partition, &root, &key, &reltuples, &pages, &comm}, func() error {
		x := &rel{
			r: schema.Relation{
				Schema: nsp, Name: name, Kind: kinds[kind], Comment: comm, PartitionKey: key,
				Pages: pages, Columns: []schema.Column{},
			},
			partition: partition, root: root, leaf: kind != "p", reltuples: reltuples, pages: pages,
		}
		// A partitioned table's estimate is the sum over its partitions.
		if !partition && kind != "p" {
			x.r.RowEstimate = a.estimate(reltuples, pages)
		}
		a.rels[oid] = x
		a.order = append(a.order, oid)
		return nil
	})
	return err
}

// estimate turns reltuples into a row estimate. From PostgreSQL 14 a negative
// value means never analyzed; before 14, 0 with no pages is the closest signal.
func (a *assembler) estimate(reltuples float64, pages int64) *int64 {
	if reltuples < 0 || (a.version < 140000 && reltuples == 0 && pages == 0) {
		return nil
	}
	n := int64(math.Round(reltuples))
	return &n
}

func (a *assembler) columns(rows pgx.Rows) error {
	var (
		oid                                  uint32
		name, typ, expr, ident, gen, comment string
		notNull                              bool
	)
	_, err := pgx.ForEachRow(rows, []any{&oid, &name, &typ, &notNull, &expr, &ident, &gen, &comment}, func() error {
		x := a.rels[oid]
		if x == nil {
			return nil
		}
		c := schema.Column{Name: name, Type: typ, Nullable: !notNull, Comment: comment}
		switch ident {
		case "a":
			c.Identity = "always"
		case "d":
			c.Identity = "by_default"
		}
		switch gen {
		case "s":
			c.Generated, c.GeneratedKind = expr, "stored"
		case "v":
			c.Generated, c.GeneratedKind = expr, "virtual"
		default:
			c.Default = expr
		}
		x.r.Columns = append(x.r.Columns, c)
		return nil
	})
	return err
}

func (a *assembler) constraints(rows pgx.Rows) error {
	var (
		oid, refOID                        uint32
		name, typ, onDel, onUpd, checkExpr string
		validated                          bool
		cols, refCols                      []string
	)
	_, err := pgx.ForEachRow(rows, []any{&oid, &name, &typ, &validated, &cols, &refOID, &refCols, &onDel, &onUpd, &checkExpr}, func() error {
		x := a.rels[oid]
		if x == nil {
			return nil
		}
		if x.partition {
			if typ == "f" {
				a.partFKs = append(a.partFKs, partitionFK{partition: oid, refOID: refOID, fk: schema.ForeignKey{
					Name: name, Columns: slices.Clone(cols), RefCols: slices.Clone(refCols),
					OnDelete: fkActions[onDel], OnUpdate: fkActions[onUpd], Validated: validated,
				}})
			}
			return nil
		}
		r := &x.r
		switch typ {
		case "p":
			r.PrimaryKey = slices.Clone(cols)
		case "u":
			r.Uniques = append(r.Uniques, slices.Clone(cols))
		case "c":
			r.Checks = append(r.Checks, schema.Check{Name: name, Expr: checkExpr, Validated: validated})
		case "f":
			r.ForeignKeys = append(r.ForeignKeys, schema.ForeignKey{
				Name: name, Columns: slices.Clone(cols), RefCols: slices.Clone(refCols),
				OnDelete: fkActions[onDel], OnUpdate: fkActions[onUpd], Validated: validated,
			})
			a.fks = append(a.fks, pendingFK{owner: oid, index: len(r.ForeignKeys) - 1, refOID: refOID})
		}
		return nil
	})
	return err
}

func (a *assembler) indexes(rows pgx.Rows) error {
	var (
		oid                    uint32
		name, method, pred     string
		unique, primary, valid bool
		keys                   int
		elems                  []string
	)
	_, err := pgx.ForEachRow(rows, []any{&oid, &name, &method, &unique, &primary, &valid, &keys, &elems, &pred}, func() error {
		x := a.rels[oid]
		if x == nil {
			return nil
		}
		keys = min(keys, len(elems))
		ix := schema.Index{
			Name: name, Columns: slices.Clone(elems[:keys]), Unique: unique, Primary: primary,
			Method: method, Partial: pred, Valid: valid,
		}
		if keys < len(elems) {
			ix.Include = slices.Clone(elems[keys:])
		}
		x.r.Indexes = append(x.r.Indexes, ix)
		return nil
	})
	return err
}

func (a *assembler) viewDeps(rows pgx.Rows) error {
	var (
		view, base uint32
		nsp, name  string
	)
	_, err := pgx.ForEachRow(rows, []any{&view, &base, &nsp, &name}, func() error {
		x := a.rels[view]
		if x == nil {
			return nil
		}
		q := schema.QualifiedName{Schema: nsp, Name: name}
		if a.rels[base] != nil {
			q = a.visible(base)
		}
		if !slices.Contains(x.r.DependsOn, q) {
			x.r.DependsOn = append(x.r.DependsOn, q)
		}
		return nil
	})
	return err
}

func (a *assembler) enums(rows pgx.Rows) error {
	var e schema.Enum
	_, err := pgx.ForEachRow(rows, []any{&e.Schema, &e.Name, &e.Values}, func() error {
		a.enumsV = append(a.enumsV, schema.Enum{Schema: e.Schema, Name: e.Name, Values: slices.Clone(e.Values)})
		return nil
	})
	return err
}

// visible maps a relation to the name the model shows for it: a partition
// is shown as its partition root.
func (a *assembler) visible(oid uint32) schema.QualifiedName {
	x := a.rels[oid]
	if x.partition {
		if root := a.rels[x.root]; root != nil {
			return root.r.QName()
		}
	}
	return x.r.QName()
}

// promotePartitionFKs moves a foreign key declared separately on partitions
// up to their root when every leaf partition declares it (same columns, same
// target), as schemas that predate partitioned-table foreign keys do. A key on
// only some partitions is not a fact about the table, so it is left out.
func (a *assembler) promotePartitionFKs() {
	leaves := map[uint32]int{}
	for _, x := range a.rels {
		if x.partition && x.leaf {
			leaves[x.root]++
		}
	}
	type key struct {
		root, ref     uint32
		cols, refCols string
	}
	type group struct {
		fk         schema.ForeignKey
		partitions map[uint32]bool
		validated  bool
	}
	groups := map[key]*group{}
	var order []key
	for _, p := range a.partFKs {
		x := a.rels[p.partition]
		if !x.leaf {
			continue
		}
		k := key{x.root, p.refOID, strings.Join(p.fk.Columns, ","), strings.Join(p.fk.RefCols, ",")}
		g := groups[k]
		if g == nil {
			g = &group{fk: p.fk, partitions: map[uint32]bool{}, validated: true}
			groups[k] = g
			order = append(order, k)
		}
		g.partitions[p.partition] = true
		g.validated = g.validated && p.fk.Validated
	}
	for _, k := range order {
		g, root := groups[k], a.rels[k.root]
		if root == nil || root.partition || len(g.partitions) != leaves[k.root] {
			continue
		}
		if slices.ContainsFunc(root.r.ForeignKeys, func(f schema.ForeignKey) bool {
			return slices.Equal(f.Columns, g.fk.Columns)
		}) {
			continue
		}
		fk := g.fk
		fk.Name = fmt.Sprintf("%s (declared on each of %d partitions)", fk.Name, len(g.partitions))
		fk.Validated = g.validated
		root.r.ForeignKeys = append(root.r.ForeignKeys, fk)
		a.fks = append(a.fks, pendingFK{owner: k.root, index: len(root.r.ForeignKeys) - 1, refOID: k.ref})
	}
}

func (a *assembler) missingRefs() []uint32 {
	var out []uint32
	for _, fk := range a.fks {
		if a.rels[fk.refOID] == nil && !slices.Contains(out, fk.refOID) {
			out = append(out, fk.refOID)
		}
	}
	return out
}

func (a *assembler) stubs(rows pgx.Rows) error {
	var (
		oid             uint32
		nsp, name, kind string
		pk              []string
	)
	_, err := pgx.ForEachRow(rows, []any{&oid, &nsp, &name, &kind, &pk}, func() error {
		a.rels[oid] = &rel{r: schema.Relation{
			Schema: nsp, Name: name, Kind: kinds[kind], Columns: []schema.Column{},
			PrimaryKey: slices.Clone(pk), Stub: true,
		}}
		a.order = append(a.order, oid)
		return nil
	})
	return err
}

func (a *assembler) model() *schema.Model {
	for _, fk := range a.fks {
		owner := a.rels[fk.owner]
		if a.rels[fk.refOID] != nil {
			owner.r.ForeignKeys[fk.index].Ref = a.visible(fk.refOID)
		}
	}
	// Collapse partitions into their root. Partitions whose root is not
	// selected are dropped: their columns and keys belong to the root.
	for _, oid := range a.order {
		x := a.rels[oid]
		if !x.partition {
			continue
		}
		root := a.rels[x.root]
		if root == nil || root.partition {
			continue
		}
		root.r.Partitions = append(root.r.Partitions, x.r.QName())
		if x.leaf {
			root.r.Pages += x.pages
			if n := a.estimate(x.reltuples, x.pages); n != nil {
				if root.r.RowEstimate == nil {
					root.r.RowEstimate = n
				} else {
					*root.r.RowEstimate += *n
				}
			}
		}
	}
	m := &schema.Model{Relations: []schema.Relation{}, Enums: a.enumsV}
	if m.Enums == nil {
		m.Enums = []schema.Enum{}
	}
	for _, oid := range a.order {
		x := a.rels[oid]
		if x.partition {
			continue
		}
		slices.SortFunc(x.r.Partitions, cmpName)
		m.Relations = append(m.Relations, x.r)
	}
	slices.SortFunc(m.Relations, func(p, q schema.Relation) int { return cmpName(p.QName(), q.QName()) })
	return m
}

func cmpName(p, q schema.QualifiedName) int {
	return cmp.Or(cmp.Compare(p.Schema, q.Schema), cmp.Compare(p.Name, q.Name))
}

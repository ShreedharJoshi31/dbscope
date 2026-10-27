package migrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/stats"
	"github.com/dbscope/dbscope/internal/schema"
)

// Size is the storage of one relation.
type Size struct {
	Table   int64
	Indexes int64
}

// Impacter finds dependents of changed objects.
type Impacter interface {
	Impact(ctx context.Context, targets []impact.Target) (impact.Result, []string, error)
}

// Base is the schema a migration is analyzed against.
type Base struct {
	Model   *schema.Model
	Catalog *apply.Catalog
	// Sizes are keyed "schema.name" (tables, partitions, matviews); nil
	// means use Model pages x BlockSize. IndexSizes are keyed "schema.index".
	Sizes      map[string]Size
	IndexSizes map[string]int64
	BlockSize  int64
	// LockTimeout is the role or database lock_timeout ("" for none).
	LockTimeout string
	// Source is live, stats, dump or sample.
	Source string
	// Impact is the live dependents source; nil derives from Model.
	Impact   Impacter
	Warnings []string
	// Scans (sequential plus index) and Writes (rows inserted, updated,
	// deleted) per relation since ActivitySince, keyed "schema.name"; a
	// partitioned table has its partitions'. Empty without activity counters
	// (a schema dump alone).
	Scans, Writes map[string]int64
	// TotalScans and TotalWrites are the database's, or when unknown the sum
	// over the schemas read (DatabaseTotals false).
	TotalScans, TotalWrites int64
	DatabaseTotals          bool
	ActivitySince           *time.Time
	InRecovery              bool
}

// Hot tables: at least this share of the database's activity, and at least
// minHotCount events, so a nearly idle database has no hot tables.
const (
	hotShare    = 0.05
	minHotCount = 1000
)

// hotness is how much traffic waits on a lock of class L on a relation:
// ACCESS EXCLUSIVE stops reads, so its scans count; the SHARE-class locks
// stop only writes, so its row writes count.
type hotness struct {
	hot   bool
	share float64
	n     int64
	reads bool
	// of names what share is of, e.g. "this database's table scans".
	of string
}

func (b *Base) hot(rel apply.QName, L int) hotness {
	h := hotness{reads: L >= 3}
	unit := "row writes"
	n, total := b.Writes[rel.String()], b.TotalWrites
	if h.reads {
		unit = "table scans"
		n, total = b.Scans[rel.String()], b.TotalScans
	}
	h.of = "this database's " + unit
	if !b.DatabaseTotals {
		h.of = "the " + unit + " in the schemas read"
	}
	h.n = n
	if L < 2 || total == 0 || n < minHotCount {
		return h
	}
	h.share = float64(n) / float64(total)
	h.hot = h.share >= hotShare
	return h
}

// LiveBase introspects a database: structure, sizes, role/database
// timeouts, and live impact queries. db should be a pool or connection for
// a (possibly SELECT-only) role.
func LiveBase(ctx context.Context, db introspect.DB, schemas []string, timeout time.Duration) (*Base, error) {
	m, err := introspect.Introspect(ctx, db, schemas, timeout)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if ms := timeout.Milliseconds(); ms > 0 {
		if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", fmt.Sprint(ms)); err != nil {
			return nil, err
		}
	}
	f, warnings, err := stats.Read(ctx, tx, m.Schemas)
	if err != nil {
		return nil, err
	}
	b := FromStats(m, f)
	b.Source = "live"
	b.Warnings = append(b.Warnings, warnings...)
	b.Impact = &impact.Live{DB: db, Timeout: timeout, StatStatements: true}
	return b, nil
}

// FromStats combines a model (from a dump or introspection) with a stats export.
func FromStats(m *schema.Model, f *stats.File) *Base {
	b := &Base{Model: m, Catalog: &apply.Catalog{ConstrainedDomains: map[string]bool{}, DefaultPartitions: map[string]apply.QName{}, Clustered: map[string]bool{}},
		Sizes: map[string]Size{}, IndexSizes: map[string]int64{}, BlockSize: int64(f.BlockSize), Source: "stats",
		LockTimeout: f.LockTimeout(f.CurrentRole)}
	if m.ServerVersion == 0 {
		m.ServerVersion = f.ServerVersion
	}
	b.ActivitySince, b.InRecovery = f.ActivitySince, f.InRecovery
	b.Scans, b.Writes = map[string]int64{}, map[string]int64{}
	var schemaScans, schemaWrites int64
	for _, r := range f.Relations {
		key := r.Schema + "." + r.Name
		b.Sizes[key] = Size{Table: r.TableBytes, Indexes: r.IndexBytes}
		b.Scans[key], b.Writes[key] = r.Scans, r.RowsWritten
		schemaScans += r.Scans
		schemaWrites += r.RowsWritten
		if rel := m.Relation(schema.QualifiedName{Schema: r.Schema, Name: r.Name}); rel != nil && !r.Partition {
			if rel.RowEstimate == nil && r.RowEstimate != nil {
				v := *r.RowEstimate
				rel.RowEstimate = &v
			}
			if rel.Pages == 0 {
				rel.Pages = r.Pages
			}
		}
	}
	b.TotalScans, b.TotalWrites, b.DatabaseTotals = f.TotalScans, f.TotalRowsWritten, f.TotalScans > 0
	if !b.DatabaseTotals {
		b.TotalScans, b.TotalWrites = schemaScans, schemaWrites
	}
	// The executor scans and writes the leaf partitions, so a partitioned
	// table's activity is theirs, nested levels included.
	var sum func(rel *schema.Relation) (int64, int64)
	sum = func(rel *schema.Relation) (int64, int64) {
		k := rel.QName().String()
		scans, writes := b.Scans[k], b.Writes[k]
		for _, p := range rel.Partitions {
			if child := m.Relation(p); child != nil {
				s, w := sum(child)
				scans, writes = scans+s, writes+w
			}
		}
		return scans, writes
	}
	for i := range m.Relations {
		if rel := &m.Relations[i]; len(rel.Partitions) > 0 {
			b.Scans[rel.QName().String()], b.Writes[rel.QName().String()] = sum(rel)
		}
	}
	// A partitioned parent's size is the sum of its partitions.
	for i := range m.Relations {
		rel := &m.Relations[i]
		if len(rel.Partitions) == 0 {
			continue
		}
		var sum Size
		var rows int64
		known := false
		for _, p := range rel.Partitions {
			s := b.Sizes[p.String()]
			sum.Table += s.Table
			sum.Indexes += s.Indexes
			for _, r := range f.Relations {
				if r.Schema == p.Schema && r.Name == p.Name && r.RowEstimate != nil {
					rows += *r.RowEstimate
					known = true
				}
			}
		}
		b.Sizes[rel.QName().String()] = sum
		if rel.RowEstimate == nil && known {
			rel.RowEstimate = &rows
		}
	}
	for _, ix := range f.Indexes {
		b.IndexSizes[ix.Schema+"."+ix.Name] = ix.Bytes
	}
	for k, v := range f.Catalog.ConstrainedDomains {
		b.Catalog.ConstrainedDomains[k] = v
	}
	for k, v := range f.Catalog.DefaultPartitions {
		b.Catalog.DefaultPartitions[k] = v
	}
	for k, v := range f.Catalog.Clustered {
		b.Catalog.Clustered[k] = v
	}
	return b
}

// DumpBase builds a base from `pg_dump --schema-only` output, optionally
// with a stats export for sizes. Without stats, sizes are unknown.
func DumpBase(dump string, f *stats.File) *Base {
	d := apply.LoadDump(dump)
	var b *Base
	if f != nil {
		b = FromStats(d.Model, f)
		for k, v := range d.Catalog.ConstrainedDomains {
			b.Catalog.ConstrainedDomains[k] = v
		}
		for k, v := range d.Catalog.DefaultPartitions {
			b.Catalog.DefaultPartitions[k] = v
		}
		for k, v := range d.Catalog.Clustered {
			b.Catalog.Clustered[k] = v
		}
		b.Source = "stats"
	} else {
		b = &Base{Model: d.Model, Catalog: d.Catalog, Source: "dump"}
		for i := range d.Model.Relations {
			d.Model.Relations[i].RowEstimate = nil // unknown, not empty
		}
	}
	b.Warnings = append(b.Warnings, d.Warnings...)
	return b
}

// SampleBase wraps the sample model: sizes come from its embedded page
// estimates; impact is model-derived.
func SampleBase(m *schema.Model) *Base {
	return &Base{Model: m, Source: "sample", BlockSize: 8192}
}

// size returns the bytes for spec and whether they are known.
func (b *Base) size(rel apply.QName, heap, indexes bool, index string, st *apply.State) (int64, bool) {
	if st.Created(rel) {
		return 0, true
	}
	bs := b.BlockSize
	if bs <= 0 {
		bs = 8192
	}
	key := rel.String()
	if b.Sizes != nil {
		s, ok := b.Sizes[key]
		if !ok {
			return 0, false
		}
		var n int64
		if heap {
			n += s.Table
		}
		if indexes {
			if index != "" {
				v, ok := b.IndexSizes[rel.Schema+"."+index]
				if !ok {
					return n, heap
				}
				n += v
			} else {
				n += s.Indexes
			}
		}
		return n, true
	}
	// Model pages: heap only; index sizes are unknown.
	r := b.Model.Relation(rel)
	if r == nil {
		parent := partitionParent(b.Model, rel)
		if parent == nil || len(parent.Partitions) == 0 || b.Source == "dump" {
			return 0, false
		}
		return parent.Pages / int64(len(parent.Partitions)) * bs, heap
	}
	if b.Source == "dump" {
		return 0, false
	}
	if !heap {
		return 0, false
	}
	return r.Pages * bs, true
}

func partitionParent(m *schema.Model, q apply.QName) *schema.Relation {
	for i := range m.Relations {
		for _, p := range m.Relations[i].Partitions {
			if p == q {
				return &m.Relations[i]
			}
		}
	}
	return nil
}

// versionText renders server_version_num as "17" or "17.2".
func versionText(v int) string {
	if v%10000 == 0 {
		return fmt.Sprint(v / 10000)
	}
	return fmt.Sprintf("%d.%d", v/10000, v%10000)
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, strings.TrimSuffix(word, "s"))
}

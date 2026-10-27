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
	for _, r := range f.Relations {
		key := r.Schema + "." + r.Name
		b.Sizes[key] = Size{Table: r.TableBytes, Indexes: r.IndexBytes}
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

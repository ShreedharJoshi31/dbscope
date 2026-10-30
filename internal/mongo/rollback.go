package mongo

import (
	"slices"
	"sort"

	"github.com/dbscope/dbscope/internal/migrate/down"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// rbObject is a collection or index as the up and then the down leave it.
// Scripts are not run, so the round trip is followed by name.
type rbObject struct {
	kind, name, coll string
	before, now      bool
}

type rollbackModel struct {
	base  *Base
	stale map[string]bool // collections an earlier file changed
	objs  map[string]*rbObject
	order []string
	down  bool
	drops []string
}

// checkDown follows f's up function and then its down function by name.
func (a *mongoAnalysis) checkDown(f ingest.Input, d *ingest.Down) down.Result {
	stale := map[string]bool{}
	for k := range a.altered {
		stale[k] = true
	}
	for _, k := range append(slices.Clone(a.rep.Diff.Added), a.rep.Diff.Dropped...) {
		stale[k] = true
	}
	m := &rollbackModel{base: a.base, stale: stale, objs: map[string]*rbObject{}}
	for _, c := range findCalls(f.SQL) {
		m.call(c)
	}
	m.down = true
	for _, c := range findCalls(d.Text) {
		m.call(c)
	}
	return m.result()
}

func (m *rollbackModel) info(coll string) *CollectionInfo {
	if m.stale[coll] {
		return nil
	}
	return m.base.Collections[coll]
}

// get returns the object with its state before the up: from the base when
// it describes the collection, else inferred from the first call (one that
// creates the object means it did not exist).
func (m *rollbackModel) get(kind, coll, name string, creates bool) *rbObject {
	k := kind + " " + coll + "." + name
	if o := m.objs[k]; o != nil {
		return o
	}
	o := &rbObject{kind: kind, coll: coll, name: name, before: !creates}
	switch known := len(m.base.Collections) > 0; {
	case kind == "collection" && known && !m.stale[name]:
		o.before = m.base.Collections[name] != nil
	case kind == "index" && known && !m.stale[coll]:
		info := m.base.Collections[coll]
		o.before = info != nil && findIndex(info, name, "") != nil
	}
	o.now = o.before
	m.objs[k] = o
	m.order = append(m.order, k)
	return o
}

func (m *rollbackModel) coll(name string) *rbObject { return m.get("collection", "", name, false) }

func (m *rollbackModel) set(o *rbObject, present bool) {
	if !present && o.now && o.before && !m.down && o.kind == "collection" {
		m.drops = append(m.drops, "collection "+o.name)
	}
	o.now = present
}

// ensure creates a collection implicitly, as an insert or index build does.
// Without a base, the collection is assumed to exist already.
func (m *rollbackModel) ensure(name string) {
	if o := m.get("collection", "", name, false); !o.now {
		m.set(o, true)
	}
}

func (m *rollbackModel) dropCollection(name string) {
	m.set(m.coll(name), false)
	// Its indexes go with it; a recreated collection needs them built again.
	if info := m.info(name); info != nil {
		for _, ix := range info.Indexes {
			if ix.Name != "_id_" {
				m.get("index", name, ix.Name, false).now = false
			}
		}
	}
	for _, o := range m.objs {
		if o.kind == "index" && o.coll == name {
			o.now = false
		}
	}
}

func (m *rollbackModel) call(c call) {
	arg := func(i int) jsValue {
		if i < len(c.Args) {
			return c.Args[i]
		}
		return nil
	}
	switch c.Method {
	case "createCollection":
		if n, ok := arg(0).(string); ok {
			m.set(m.get("collection", "", n, true), true)
		}
	case "drop":
		m.dropCollection(c.Collection)
	case "dropDatabase":
		if !m.down {
			m.drops = append(m.drops, "the whole database")
		}
	case "renameCollection":
		to, _ := arg(0).(string)
		if to == "" {
			return
		}
		if t := m.coll(to); t.now && arg(1) == true {
			m.dropCollection(to)
		}
		m.coll(c.Collection).now = false // a rename keeps the documents
		m.set(m.get("collection", "", to, true), true)
	case "createIndex", "ensureIndex":
		m.ensure(c.Collection)
		m.createIndex(c.Collection, arg(0), arg(1))
	case "createIndexes":
		m.ensure(c.Collection)
		if specs, ok := arg(0).([]jsValue); ok {
			for _, s := range specs {
				m.createIndex(c.Collection, s, arg(1))
			}
		}
	case "dropIndex", "dropIndexes":
		n := indexName(arg(0))
		if c.Method == "dropIndexes" && (arg(0) == nil || n == "*") {
			if info := m.info(c.Collection); info != nil {
				for _, ix := range info.Indexes {
					if ix.Name != "_id_" {
						m.get("index", c.Collection, ix.Name, false)
					}
				}
			}
			for _, o := range m.objs {
				if o.kind == "index" && o.coll == c.Collection {
					o.now = false
				}
			}
			return
		}
		if n != "" && n != "_id_" {
			m.set(m.get("index", c.Collection, n, false), false)
		}
	case "insertOne", "insertMany":
		m.ensure(c.Collection)
	case "updateMany", "update", "updateOne":
		if u, ok := arg(1).(jsObject); ok && !m.down {
			if unset, ok := u.get("$unset").(jsObject); ok {
				for _, kv := range unset {
					m.drops = append(m.drops, "field "+c.Collection+"."+kv.Key)
				}
			}
		}
	case "runCommand", "command":
		o, ok := arg(0).(jsObject)
		if !ok || len(o) == 0 {
			return
		}
		n, _ := o[0].Value.(string)
		switch o[0].Key {
		case "create":
			m.set(m.get("collection", "", n, true), true)
		case "drop":
			m.dropCollection(n)
		}
	}
}

func (m *rollbackModel) createIndex(coll string, spec, opts jsValue) {
	keys, options := spec, opts
	if o, ok := spec.(jsObject); ok {
		if k := o.get("key"); k != nil {
			keys, options = k, spec
		}
	}
	kl := keyList(keys)
	if len(kl) == 0 {
		return
	}
	o, _ := options.(jsObject)
	name, _ := o.get("name").(string)
	if name == "" {
		name = defaultIndexName(kl)
	}
	m.set(m.get("index", coll, name, true), true)
}

func (m *rollbackModel) result() down.Result {
	r := down.Result{Drops: m.drops}
	sort.Strings(m.order)
	for _, k := range m.order {
		o := m.objs[k]
		if o.kind == "index" {
			if c := m.objs["collection ."+o.coll]; c != nil && !c.now {
				continue // reported with its collection, or gone on both sides
			}
		}
		what := o.kind + " " + o.name
		if o.kind == "index" {
			what = "index " + o.name + " on " + o.coll
		}
		switch {
		case o.before && !o.now:
			r.Left = append(r.Left, what+" is not recreated")
		case !o.before && o.now:
			r.Left = append(r.Left, what+" is still there")
		}
	}
	return r
}

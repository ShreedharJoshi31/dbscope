package mysql

import (
	"slices"
	"sort"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate/down"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// rbObject is a table, column, index or column default as the up and then
// the down leave it. MySQL migrations are not replayed on a server, so the
// round trip is followed by name: what exists, and for columns the type and
// nullability when the base knows them.
type rbObject struct {
	kind, name string // "table", "column", "index", "default"; display name
	table      string // the object's table key ("" for tables)
	before     bool   // existed before the up
	now        bool
	known      bool   // before is known, not inferred from the first statement
	beforeDef  string // column type and nullability before the up; "" when unknown
	def        string
	defSet     bool // a statement set def
	changedUp  bool // the up changed its definition
	touchedDn  bool // the down set its definition
}

type rollbackModel struct {
	a *ddlAnalysis
	// stale are tables an earlier file changed, so the base no longer describes them.
	stale map[string]bool
	objs  map[string]*rbObject
	order []string
	down  bool // replaying the down
	drops []string
}

// checkDown follows f and then its down by name, starting from the base
// as earlier files left it.
func (a *ddlAnalysis) checkDown(f ingest.Input, d *ingest.Down) down.Result {
	stale := map[string]bool{}
	for k := range a.altered {
		stale[strings.ToLower(k)] = true
	}
	for _, k := range append(slices.Clone(a.rep.Diff.Added), a.rep.Diff.Dropped...) {
		stale[strings.ToLower(k)] = true
	}
	m := &rollbackModel{a: &ddlAnalysis{base: a.base}, stale: stale, objs: map[string]*rbObject{}}
	for _, p := range Split(f.SQL) {
		m.statement(p)
	}
	m.down = true
	for _, p := range Split(d.Text) {
		m.statement(p)
	}
	return m.result()
}

// baseTable is the base's description of a table, if it still applies.
func (m *rollbackModel) baseTable(key string) *TableInfo {
	if m.stale[key] || m.a.base.Source != "live" {
		return nil
	}
	return m.a.base.Tables[key]
}

// get returns the object, creating it with its state before the up: from
// the base when it knows, else inferred from the first statement (one that
// adds the object means it did not exist).
func (m *rollbackModel) get(kind, table, key, display string, adds bool) *rbObject {
	k := kind + " " + table + "." + key
	if kind == "table" {
		k = "table " + key
	}
	if o := m.objs[k]; o != nil {
		return o
	}
	o := &rbObject{kind: kind, name: display, table: table, before: !adds}
	switch kind {
	case "table":
		if m.a.base.Source == "live" && !m.stale[key] {
			o.known, o.before = true, m.a.base.Tables[key] != nil
		}
	case "column":
		if t := m.baseTable(table); t != nil {
			c, ok := t.Columns[key]
			o.known, o.before = true, ok
			if ok {
				o.beforeDef = baseColumnDef(c)
			}
		}
	}
	o.now = o.before
	m.objs[k] = o
	m.order = append(m.order, k)
	return o
}

func (m *rollbackModel) set(o *rbObject, present bool) {
	if !present && o.now && o.before && !m.down && (o.kind == "table" || o.kind == "column") {
		m.drops = append(m.drops, o.kind+" "+o.name)
	}
	o.now = present
}

func (m *rollbackModel) define(o *rbObject, def string) {
	o.def, o.defSet = def, true
	if m.down {
		o.touchedDn = true
	} else {
		o.changedUp = true
	}
}

func (m *rollbackModel) table(display, key string) *rbObject {
	return m.get("table", "", key, display, false)
}

// statement follows one statement's effect on tables, columns and indexes.
func (m *rollbackModel) statement(p Piece) {
	toks := Tokens(p.SQL)
	w := words(toks)
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	skip := func(i int, ws ...string) int {
		for i < len(toks) && slices.Contains(ws, toks[i].Word) {
			i++
		}
		return i
	}
	switch {
	case at(0) == "create" && (at(1) == "table" || at(1) == "temporary" && at(2) == "table"):
		if at(1) == "temporary" {
			return
		}
		display, key, next := m.a.tableName(toks, skip(2, "if", "not", "exists"))
		o := m.get("table", "", key, display, true)
		m.set(o, true)
		if cols, ok := createColumns(toks[next:]); ok {
			m.define(o, cols)
		} else {
			o.defSet = false
		}
	case at(0) == "drop" && (at(1) == "table" || at(1) == "temporary"):
		if at(1) == "temporary" {
			return
		}
		for i := skip(2, "if", "exists"); i < len(toks); {
			display, key, next := m.a.tableName(toks, i)
			m.set(m.table(display, key), false)
			if next >= len(toks) || toks[next].Text != "," {
				break
			}
			i = next + 1
		}
	case at(0) == "rename" && at(1) == "table":
		for i := 2; i < len(toks); {
			fromD, fromK, next := m.a.tableName(toks, i)
			if next >= len(toks) || toks[next].Word != "to" {
				return
			}
			toD, toK, after := m.a.tableName(toks, next+1)
			m.renameTable(fromD, fromK, toD, toK)
			if after >= len(toks) || toks[after].Text != "," {
				break
			}
			i = after + 1
		}
	case at(0) == "create" && slices.Contains(w, "index"):
		ix := slices.IndexFunc(toks, func(t Token) bool { return t.Word == "index" })
		on := slices.IndexFunc(toks, func(t Token) bool { return t.Word == "on" })
		if ix < 0 || on < 0 || ix+1 >= len(toks) {
			return
		}
		_, key, _ := m.a.tableName(toks, on+1)
		n := name(toks[skip(ix+1, "if", "not", "exists")])
		m.set(m.get("index", key, strings.ToLower(n), n+" on "+key, true), true)
	case at(0) == "drop" && at(1) == "index":
		on := slices.IndexFunc(toks, func(t Token) bool { return t.Word == "on" })
		if on < 0 {
			return
		}
		_, key, _ := m.a.tableName(toks, on+1)
		n := name(toks[skip(2, "if", "exists")])
		m.set(m.get("index", key, strings.ToLower(n), n+" on "+key, false), false)
	case at(0) == "alter" && slices.Contains(w, "table"):
		i := skip(1, "online", "ignore")
		if i >= len(toks) || toks[i].Word != "table" {
			return
		}
		display, key, next := m.a.tableName(toks, i+1)
		for _, c := range clauseSplit(toks[next:]) {
			if len(c) > 0 {
				display, key = m.alter(display, key, c)
			}
		}
	}
}

func (m *rollbackModel) renameTable(fromD, fromK, toD, toK string) {
	from := m.table(fromD, fromK)
	to := m.get("table", "", toK, toD, true)
	to.now, from.now = true, false
	if from.defSet {
		to.def, to.defSet = from.def, true
	}
}

// alter applies one ALTER TABLE clause and returns the table's name after it.
func (m *rollbackModel) alter(display, key string, c []Token) (string, string) {
	w := words(c)
	kind := clauseKind(w)
	col := func(n string, adds bool) *rbObject {
		return m.get("column", key, strings.ToLower(n), display+"."+n, adds)
	}
	switch kind {
	case "add column":
		n, _, def := columnOf(kind, c)
		if n == "" || n == "(" {
			return display, key
		}
		o := col(n, true)
		m.set(o, true)
		m.define(o, columnDef(def))
	case "drop column":
		if n, _, _ := columnOf(kind, c); n != "" {
			m.set(col(n, false), false)
		}
	case "modify column":
		n, _, def := columnOf(kind, c)
		o := col(n, false)
		m.define(o, columnDef(def))
	case "change column", "rename column":
		old, nw, def := columnOf(kind, c)
		from := col(old, false)
		if !strings.EqualFold(old, nw) {
			to := col(nw, true)
			m.set(to, true)
			from.now = false // a rename keeps the data
			if from.defSet {
				to.def, to.defSet = from.def, true
			} else if from.beforeDef != "" {
				to.def, to.defSet = from.beforeDef, true
			}
			if kind == "change column" {
				m.define(to, columnDef(def))
			}
		} else if kind == "change column" {
			m.define(from, columnDef(def))
		}
	case "alter column":
		if len(w) > 2 {
			n := w[1]
			if n == "column" {
				n = w[2]
			}
			m.define(m.get("default", key, n, "default of "+display+"."+n, false), "")
		}
	case "add index", "add unique", "add fulltext", "add spatial":
		if n := indexNameOf(c); n != "" {
			m.set(m.get("index", key, strings.ToLower(n), n+" on "+display, true), true)
		}
	case "add primary key":
		m.set(m.get("index", key, "primary", "primary key on "+display, true), true)
	case "drop index":
		if len(c) > 2 {
			n := name(c[2])
			m.set(m.get("index", key, strings.ToLower(n), n+" on "+display, false), false)
		}
	case "drop primary key":
		m.set(m.get("index", key, "primary", "primary key on "+display, false), false)
	case "rename table":
		i := 1
		if i < len(c) && (c[i].Word == "to" || c[i].Word == "as") {
			i++
		}
		toD, toK, _ := m.a.tableName(c, i)
		m.renameTable(display, key, toD, toK)
		return toD, toK
	}
	return display, key
}

// indexNameOf is the name in ADD [UNIQUE|FULLTEXT|SPATIAL] [INDEX|KEY] name (...)
// or ADD CONSTRAINT name UNIQUE (...); "" for an unnamed index.
func indexNameOf(c []Token) string {
	if len(c) > 2 && c[1].Word == "constraint" && c[2].Text != "(" {
		if w := c[2].Word; w != "unique" && w != "primary" && w != "foreign" && w != "check" {
			return name(c[2])
		}
	}
	for _, t := range c[1:] {
		switch t.Word {
		case "unique", "fulltext", "spatial", "index", "key", "constraint", "if", "not", "exists":
			continue
		}
		if t.Text == "(" || t.Word == "using" {
			return ""
		}
		return name(t)
	}
	return ""
}

// columnType normalises a type for comparing a definition with
// COLUMN_TYPE, which prints BOOL as tinyint(1).
func columnType(t string) string {
	t = strings.Join(strings.Fields(strings.ToLower(t)), " ")
	if t == "bool" || t == "boolean" {
		t = "tinyint"
	}
	return normType(t)
}

func baseColumnDef(c ColumnInfo) string {
	if c.Nullable {
		return columnType(c.Type) + " null"
	}
	return columnType(c.Type) + " not null"
}

var typeEnd = map[string]bool{"null": true, "not": true, "default": true, "auto_increment": true, "comment": true,
	"primary": true, "unique": true, "key": true, "collate": true, "character": true, "charset": true, "generated": true,
	"as": true, "after": true, "first": true, "on": true, "invisible": true, "visible": true, "check": true,
	"references": true, "srid": true, "column_format": true, "storage": true, "constraint": true}

// columnDef is a column definition's type and nullability.
func columnDef(def []Token) string {
	var b strings.Builder
	i := 0
	for ; i < len(def) && !typeEnd[def[i].Word]; i++ {
		t := strings.ToLower(def[i].Text)
		if b.Len() > 0 && t != "(" && t != ")" && t != "," && !strings.HasSuffix(b.String(), "(") && !strings.HasSuffix(b.String(), ",") {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	w := words(def[i:])
	nullable := true
	for j := range w {
		if w[j] == "primary" || (w[j] == "not" && j+1 < len(w) && w[j+1] == "null") {
			nullable = false
		}
	}
	if nullable {
		return columnType(b.String()) + " null"
	}
	return columnType(b.String()) + " not null"
}

// createColumns is a CREATE TABLE's columns and definitions, sorted, or
// false for CREATE TABLE ... LIKE or ... AS SELECT.
func createColumns(toks []Token) (string, bool) {
	if len(toks) == 0 || toks[0].Text != "(" {
		return "", false
	}
	depth, end := 0, -1
	for i, t := range toks {
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return "", false
	}
	var cols []string
	for _, c := range clauseSplit(toks[1:end]) {
		if len(c) == 0 {
			continue
		}
		switch c[0].Word {
		case "primary", "key", "index", "unique", "constraint", "foreign", "check", "fulltext", "spatial", "like":
			continue
		}
		cols = append(cols, strings.ToLower(name(c[0]))+" "+columnDef(c[1:]))
	}
	sort.Strings(cols)
	return strings.Join(cols, ", "), true
}

func baseTableDef(t *TableInfo) string {
	var cols []string
	for n, c := range t.Columns {
		cols = append(cols, n+" "+baseColumnDef(c))
	}
	sort.Strings(cols)
	return strings.Join(cols, ", ")
}

// result compares the objects after the down with before the up.
func (m *rollbackModel) result() down.Result {
	r := down.Result{Drops: m.drops}
	gone := func(table string) bool {
		t := m.objs["table "+table]
		return t != nil && !t.now
	}
	for _, k := range m.order {
		o := m.objs[k]
		if o.table != "" && gone(o.table) {
			continue // its table is reported, or no longer exists on either side
		}
		switch {
		case o.before && !o.now:
			r.Left = append(r.Left, o.kind+" "+o.name+" is not recreated")
		case !o.before && o.now:
			r.Left = append(r.Left, o.kind+" "+o.name+" is still there")
		case !o.now:
		case o.kind == "table":
			if t := m.baseTable(strings.TrimPrefix(k, "table ")); t != nil && o.defSet && o.def != baseTableDef(t) {
				r.Left = append(r.Left, "table "+o.name+" is recreated with different columns")
			}
		case o.kind == "column" && o.beforeDef != "" && o.defSet:
			if o.def != o.beforeDef {
				r.Left = append(r.Left, "column "+o.name+" is defined differently")
			}
		case o.changedUp && !o.touchedDn:
			r.Left = append(r.Left, o.name+" is still changed")
		}
	}
	return r
}

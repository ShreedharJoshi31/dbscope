package sqlite

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate/down"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/schema"
)

// checkDown runs up and then its down in a separate scratch database that
// starts from the main scratch's current schema, so SQLite itself decides
// whether the down runs and what it leaves behind.
func (a *analysis) checkDown(ctx context.Context, f ingest.Input, d *ingest.Down) (down.Result, error) {
	var r down.Result
	if d.Missing {
		return r, nil
	}
	ddl, err := schemaDDL(ctx, a.scratch)
	if err != nil {
		return r, err
	}
	s, err := openScratch(ctx)
	if err != nil {
		return r, err
	}
	defer s.Close()
	for _, q := range ddl {
		if err := s.exec(ctx, q); err != nil {
			r.Unchecked = fmt.Sprintf("the schema before it could not be copied (%v)", err)
			return r, nil
		}
	}
	pre, err := Introspect(ctx, s.conn)
	if err != nil {
		return r, err
	}
	origin := map[string]string{} // current table, lower-cased -> name before the up
	for _, rel := range pre.Relations {
		origin[strings.ToLower(rel.Name)] = rel.Name
	}
	renamedCols := map[string]map[string]string{} // base table -> new column -> old column, lower-cased
	copiedInto := map[string]bool{}               // tables an INSERT ... SELECT filled, lower-cased
	for _, p := range Split(f.SQL) {
		st := classify(f.Name, p, false)
		if err := s.exec(ctx, p.SQL); err != nil {
			r.Unchecked = fmt.Sprintf("its statement at line %d fails (%v)", p.Line, err)
			return r, nil
		}
		key := strings.ToLower(st.table)
		switch st.kind {
		case "ALTER TABLE RENAME TO":
			to := strings.ToLower(st.to)
			if b, ok := origin[key]; ok {
				delete(origin, key)
				origin[to] = b
			}
			copiedInto[to] = copiedInto[key]
		case "INSERT", "REPLACE":
			if slices.Contains(st.words, "select") {
				copiedInto[key] = true
			}
		case "ALTER TABLE RENAME COLUMN":
			if k := slices.Index(st.words, "to"); k > 0 && k+1 < len(st.words) {
				if b, ok := origin[key]; ok {
					b = strings.ToLower(b)
					if renamedCols[b] == nil {
						renamedCols[b] = map[string]string{}
					}
					old := st.words[k-1]
					if o, ok := renamedCols[b][old]; ok {
						old = o
					}
					renamedCols[b][st.words[k+1]] = old
				}
			}
		case "DROP TABLE":
			delete(origin, key)
		}
	}
	mid, err := Introspect(ctx, s.conn)
	if err != nil {
		return r, err
	}
	r.Drops = dataDrops(pre, mid, origin, renamedCols, copiedInto)
	for _, p := range Split(d.Text) {
		if err := s.exec(ctx, p.SQL); err != nil {
			r.Failed = fmt.Sprintf("line %d: %v", p.Line, err)
			return r, nil
		}
	}
	post, err := Introspect(ctx, s.conn)
	if err != nil {
		return r, err
	}
	r.Left = down.Describe(diff(pre, post))
	return r, nil
}

// dataDrops names the tables and columns of pre whose data is gone in mid.
// A renamed table or column is not gone; a table rebuilt under its old name
// with INSERT ... SELECT keeps the columns it still has.
func dataDrops(pre, mid *schema.Model, origin map[string]string, renamedCols map[string]map[string]string, copiedInto map[string]bool) []string {
	now := map[string]*schema.Relation{} // base name, lower-cased -> table in mid
	for i := range mid.Relations {
		rel := &mid.Relations[i]
		if rel.Kind != schema.KindTable {
			continue
		}
		if b, ok := origin[strings.ToLower(rel.Name)]; ok {
			now[strings.ToLower(b)] = rel
		} else if pre.Relation(rel.QName()) != nil && copiedInto[strings.ToLower(rel.Name)] {
			now[strings.ToLower(rel.Name)] = rel // recreated under the same name, as the 12-step rebuild does
		}
	}
	var out []string
	for _, b := range pre.Relations {
		if b.Kind != schema.KindTable {
			continue
		}
		cur := now[strings.ToLower(b.Name)]
		if cur == nil {
			out = append(out, "table "+b.Name)
			continue
		}
		have := map[string]bool{}
		for _, c := range cur.Columns {
			name := strings.ToLower(c.Name)
			if o, ok := renamedCols[strings.ToLower(b.Name)][name]; ok {
				name = o
			}
			have[name] = true
		}
		for _, c := range b.Columns {
			if !have[strings.ToLower(c.Name)] {
				out = append(out, "column "+b.Name+"."+c.Name)
			}
		}
	}
	return out
}

// schemaDDL reads the scratch database's schema in creation order, without
// the shadow tables a virtual table creates for itself.
func schemaDDL(ctx context.Context, s *scratchDB) ([]string, error) {
	rows, err := s.conn.QueryContext(ctx, `SELECT s.sql FROM sqlite_schema s
		LEFT JOIN pragma_table_list l ON l.schema = 'main' AND l.name = s.name
		WHERE s.sql IS NOT NULL AND s.name NOT LIKE 'sqlite\_%' ESCAPE '\' AND coalesce(l.type, '') <> 'shadow'
		ORDER BY s.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

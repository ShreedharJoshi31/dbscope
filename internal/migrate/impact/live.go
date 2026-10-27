package impact

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/dbscope/dbscope/internal/migrate/apply"
)

// DB opens transactions (*pgx.Conn, *pgxpool.Pool).
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Live queries the catalogs of a live database. Every query is a SELECT in
// a REPEATABLE READ READ ONLY transaction; a SELECT-only role is enough.
type Live struct {
	DB      DB
	Timeout time.Duration
	// StatStatements also matches query texts from pg_stat_statements when
	// the extension is installed and readable.
	StatStatements bool
}

// regexWord builds a case-insensitive whole-word pattern for an identifier.
const regexWord = `('\m' || regexp_replace(%s, '([^[:alnum:]_])', '\\\1', 'g') || '\M')`

var catalogQuery = `WITH RECURSIVE t AS (
  SELECT u.ord::int AS ord, c.oid AS rel, c.relname::text AS relname, u.col,
         (SELECT a.attnum FROM pg_attribute a
          WHERE a.attrelid = c.oid AND a.attname = u.col AND NOT a.attisdropped AND a.attnum > 0) AS attnum
  FROM unnest($1::text[], $2::text[], $3::text[]) WITH ORDINALITY AS u(nsp, rel, col, ord)
  JOIN pg_namespace n ON n.nspname = u.nsp
  JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = u.rel
), tt AS (
  SELECT * FROM t WHERE t.col = '' OR t.attnum IS NOT NULL
), v(ord, oid, depth) AS (
  SELECT tt.ord, r.ev_class, 1
  FROM tt
  JOIN pg_depend d ON d.refclassid = 'pg_class'::regclass AND d.refobjid = tt.rel
       AND d.classid = 'pg_rewrite'::regclass AND (tt.col = '' OR d.refobjsubid = tt.attnum)
  JOIN pg_rewrite r ON r.oid = d.objid
  WHERE r.ev_class <> tt.rel
  UNION
  SELECT v.ord, r.ev_class, v.depth + 1
  FROM v
  JOIN pg_depend d ON d.refclassid = 'pg_class'::regclass AND d.refobjid = v.oid AND d.classid = 'pg_rewrite'::regclass
  JOIN pg_rewrite r ON r.oid = d.objid
  WHERE r.ev_class <> v.oid AND v.depth < 32
)
SELECT tt.ord, 'fk_child', cn.nspname || '.' || cc.relname || ' (' || con.conname || ')', 'catalog'
FROM tt
JOIN pg_constraint con ON con.confrelid = tt.rel AND con.contype = 'f' AND con.conparentid = 0 AND con.conrelid <> tt.rel
JOIN pg_class cc ON cc.oid = con.conrelid
JOIN pg_namespace cn ON cn.oid = cc.relnamespace
WHERE tt.col = '' OR tt.attnum = ANY(con.confkey)
UNION
SELECT v.ord, CASE c.relkind WHEN 'm' THEN 'matview' ELSE 'view' END, n.nspname || '.' || c.relname, 'catalog'
FROM v JOIN pg_class c ON c.oid = v.oid JOIN pg_namespace n ON n.oid = c.relnamespace
UNION
SELECT tt.ord, 'function', pn.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', 'catalog'
FROM tt
JOIN pg_depend d ON d.refclassid = 'pg_class'::regclass AND d.refobjid = tt.rel AND d.classid = 'pg_proc'::regclass
     AND (tt.col = '' OR d.refobjsubid = tt.attnum)
JOIN pg_proc p ON p.oid = d.objid
JOIN pg_namespace pn ON pn.oid = p.pronamespace
UNION
SELECT tt.ord, 'function', pn.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', 'heuristic'
FROM tt
JOIN pg_proc p ON p.prosqlbody IS NULL AND p.prosrc ~* ` + fmt.Sprintf(regexWord, "tt.relname") + `
     AND (tt.col = '' OR p.prosrc ~* ` + fmt.Sprintf(regexWord, "tt.col") + `)
JOIN pg_namespace pn ON pn.oid = p.pronamespace
JOIN pg_language l ON l.oid = p.prolang
WHERE pn.nspname NOT IN ('pg_catalog', 'information_schema') AND l.lanname NOT IN ('c', 'internal')
  AND NOT EXISTS (SELECT 1 FROM pg_depend e WHERE e.classid = 'pg_proc'::regclass AND e.objid = p.oid AND e.deptype = 'e')
UNION
SELECT tt.ord, 'policy', pol.polname || ' ON ' || pn.nspname || '.' || pc.relname, 'catalog'
FROM tt
JOIN pg_depend d ON d.refclassid = 'pg_class'::regclass AND d.refobjid = tt.rel AND d.classid = 'pg_policy'::regclass
     AND (tt.col = '' OR d.refobjsubid = tt.attnum)
JOIN pg_policy pol ON pol.oid = d.objid
JOIN pg_class pc ON pc.oid = pol.polrelid
JOIN pg_namespace pn ON pn.oid = pc.relnamespace
UNION
SELECT tt.ord, 'generated_column', n.nspname || '.' || c.relname || '.' || a.attname, 'catalog'
FROM tt
JOIN pg_depend d ON d.refclassid = 'pg_class'::regclass AND d.refobjid = tt.rel AND d.classid = 'pg_attrdef'::regclass
     AND d.refobjsubid = tt.attnum
JOIN pg_attrdef ad ON ad.oid = d.objid AND ad.adnum <> tt.attnum
JOIN pg_attribute a ON a.attrelid = ad.adrelid AND a.attnum = ad.adnum AND a.attgenerated <> ''
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE tt.attnum IS NOT NULL
UNION
-- PostgreSQL 14 records a generated column's dependency as column on column.
SELECT tt.ord, 'generated_column', n.nspname || '.' || c.relname || '.' || a.attname, 'catalog'
FROM tt
JOIN pg_depend d ON d.classid = 'pg_class'::regclass AND d.objid = tt.rel AND d.objsubid > 0 AND d.objsubid <> tt.attnum
     AND d.refclassid = 'pg_class'::regclass AND d.refobjid = tt.rel AND d.refobjsubid = tt.attnum
JOIN pg_attribute a ON a.attrelid = tt.rel AND a.attnum = d.objsubid AND a.attgenerated <> ''
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE tt.attnum IS NOT NULL
UNION
SELECT tt.ord, 'trigger', tg.tgname || ' ON ' || n.nspname || '.' || c.relname,
       CASE WHEN tt.col = '' OR tt.attnum = ANY(tg.tgattr::int2[]) THEN 'catalog' ELSE 'heuristic' END
FROM tt
JOIN pg_trigger tg ON tg.tgrelid = tt.rel AND NOT tg.tgisinternal
JOIN pg_proc p ON p.oid = tg.tgfoid
JOIN pg_class c ON c.oid = tg.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE tt.col = '' OR tt.attnum = ANY(tg.tgattr::int2[]) OR p.prosrc ~* ` + fmt.Sprintf(regexWord, "tt.col") + `
UNION
SELECT tt.ord, 'index', n.nspname || '.' || ic.relname, 'catalog'
FROM tt
JOIN pg_index i ON i.indrelid = tt.rel
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = ic.relnamespace
WHERE tt.attnum IS NOT NULL
  AND (tt.attnum = ANY(i.indkey::int2[])
       OR EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = i.indexrelid
                  AND d.refclassid = 'pg_class'::regclass AND d.refobjid = tt.rel AND d.refobjsubid = tt.attnum))
ORDER BY 1, 2, 3`

// statementsQuery reads up to 2000 of this database's top-level statement
// texts, most-called first, from the extension's schema (%s).
const statementsQuery = `SELECT query, calls FROM %s.pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND toplevel
  AND query IS NOT NULL AND query <> '<insufficient privilege>'
ORDER BY calls DESC LIMIT 2000`

// Impact runs the catalog queries for every target. The warnings explain
// sources that were skipped (e.g. pg_stat_statements without privileges).
func (l *Live) Impact(ctx context.Context, targets []Target) (Result, []string, error) {
	out := Result{}
	if len(targets) == 0 {
		return out, nil, nil
	}
	tx, err := l.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if ms := l.Timeout.Milliseconds(); ms > 0 {
		if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", strconv.FormatInt(ms, 10)); err != nil {
			return nil, nil, err
		}
	}
	nsps, rels, cols := make([]string, len(targets)), make([]string, len(targets)), make([]string, len(targets))
	for i, t := range targets {
		nsps[i], rels[i], cols[i] = t.Relation.Schema, t.Relation.Name, t.Column
	}
	rows, err := tx.Query(ctx, catalogQuery, nsps, rels, cols)
	if err != nil {
		return nil, nil, fmt.Errorf("impact catalog query: %w", err)
	}
	var ord int
	var it Item
	if _, err := pgx.ForEachRow(rows, []any{&ord, &it.Kind, &it.Name, &it.Certainty}, func() error {
		if ord >= 1 && ord <= len(targets) {
			out.add(targets[ord-1].Key(), it)
		}
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("impact catalog query: %w", err)
	}
	var warnings []string
	if l.StatStatements {
		if w := statStatements(ctx, tx, targets, out); w != "" {
			warnings = append(warnings, w)
		}
	}
	out.sort()
	return out, warnings, nil
}

// statStatements matches pg_stat_statements texts against the targets. It
// never fails the analysis: any problem becomes the returned warning.
func statStatements(ctx context.Context, tx pgx.Tx, targets []Target, out Result) string {
	// Its schema need not be on the search_path.
	var nsp string
	if err := tx.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_stat_statements'`).Scan(&nsp); err != nil {
		return "pg_stat_statements is not installed in this database; query matches were skipped"
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "pg_stat_statements was skipped: " + err.Error()
	}
	defer sp.Rollback(context.WithoutCancel(ctx))
	rows, err := sp.Query(ctx, fmt.Sprintf(statementsQuery, pgx.Identifier{nsp}.Sanitize()))
	var texts []QueryText
	if err == nil {
		var q QueryText
		_, err = pgx.ForEachRow(rows, []any{&q.Text, &q.Calls}, func() error { texts = append(texts, q); return nil })
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return fmt.Sprintf("pg_stat_statements could not be read (%s, SQLSTATE %s); query matches were skipped", pgErr.Message, pgErr.Code)
		}
		return "pg_stat_statements could not be read; query matches were skipped"
	}
	MatchQueries(texts, targets, out)
	return ""
}

// QueryText is one pg_stat_statements statement and how often it ran.
type QueryText struct {
	Text  string
	Calls int64
}

// maxQueryMatches caps query matches per target.
const maxQueryMatches = 20

// MatchQueries parses each text with libpg_query and records it under every
// target whose relation it reads (RangeVar) and, for column targets, whose
// column it names (ColumnRef). Unqualified names match any schema, so the
// matches are heuristic.
func MatchQueries(texts []QueryText, targets []Target, out Result) {
	for _, qt := range texts {
		text := qt.Text
		tree, err := pgquery.Parse(text)
		if err != nil {
			continue
		}
		var rels []*pg_query.RangeVar
		cols := map[string]bool{}
		for _, raw := range tree.GetStmts() {
			apply.Walk(raw.GetStmt(), func(m protoreflect.Message) {
				switch v := m.Interface().(type) {
				case *pg_query.RangeVar:
					rels = append(rels, v)
				case *pg_query.ColumnRef:
					if f := v.GetFields(); len(f) > 0 {
						cols[f[len(f)-1].GetString_().GetSval()] = true
					}
				}
			})
		}
		name := summarize(text)
		for _, t := range targets {
			if !slices.ContainsFunc(rels, func(rv *pg_query.RangeVar) bool {
				return rv.GetRelname() == t.Relation.Name && (rv.GetSchemaname() == "" || rv.GetSchemaname() == t.Relation.Schema)
			}) {
				continue
			}
			if t.Column != "" && !cols[t.Column] {
				continue
			}
			key := t.Key()
			n := 0
			for _, it := range out[key] {
				if it.Kind == "query" {
					n++
				}
			}
			if n < maxQueryMatches {
				out.add(key, Item{Kind: "query", Name: name, Certainty: Heuristic, Calls: qt.Calls})
			}
		}
	}
}

// summarize collapses whitespace and shortens a query text for display.
func summarize(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > 160 {
		q = string(r[:157]) + "..."
	}
	return q
}

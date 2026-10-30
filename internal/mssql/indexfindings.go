package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/schema"
)

const (
	// avg_user_impact is the percentage by which the optimizer estimates the
	// suggested index would cut the cost of the queries that wanted it, and
	// user_seeks + user_scans how often such queries ran
	// (learn.microsoft.com/sql/relational-databases/system-dynamic-management-views/sys-dm-db-missing-index-group-stats-transact-sql).
	// A suggestion is a warning from both cutoffs on; they are dbscope's choice.
	missingWarnImpact = 80
	missingWarnUses   = 100
	// maxMissing bounds the suggestions read, most estimated benefit first.
	maxMissing = 100
	// indexStatsTimeout bounds each DMV read; one that runs out is skipped.
	indexStatsTimeout = 10 * time.Second
)

// unusedMinUptime is how long the server must have run before an index no
// query read since it started is called unused: sys.dm_db_index_usage_stats
// starts empty with the server
// (learn.microsoft.com/sql/relational-databases/system-dynamic-management-views/sys-dm-db-index-usage-stats-transact-sql).
// A variable so tests can lower it.
var unusedMinUptime = 7 * 24 * time.Hour

// indexFindings adds the duplicate, missing and unused index findings. in
// and args select the read schemas as in Introspect. None of them fails
// introspection: a read that errs or times out adds nothing.
func indexFindings(ctx context.Context, db *sql.DB, srv Server, in string, args []any, rel func(s, t string) *schema.Relation) {
	duplicateIndexes(ctx, db, in, args, rel)
	// The missing index and usage DMVs need VIEW SERVER PERFORMANCE STATE
	// (VIEW SERVER STATE before 2022) and fail without it, so those findings
	// are left out rather than guessed. Azure SQL Database checks other
	// permissions and was not tested.
	if srv.Azure {
		return
	}
	var seesAll int
	if err := db.QueryRowContext(ctx, `SELECT coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE'), 0)
		| coalesce(HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE'), 0)`).Scan(&seesAll); err != nil || seesAll == 0 {
		return
	}
	missingIndexes(ctx, db, in, args, rel)
	unusedIndexes(ctx, db, in, args, rel)
}

// missingIndexes reads the optimizer's missing index suggestions for this
// database. The optimizer makes them while compiling (never for trivial
// plans), without weighing similar existing indexes or the cost of keeping
// the index up to date on writes
// (learn.microsoft.com/sql/relational-databases/indexes/tune-nonclustered-missing-index-suggestions),
// so the finding words them as a suggestion.
func missingIndexes(ctx context.Context, db *sql.DB, in string, args []any, rel func(s, t string) *schema.Relation) {
	ctx, cancel := context.WithTimeout(ctx, indexStatsTimeout)
	defer cancel()
	type suggestion struct {
		x                  *schema.Relation
		impact             float64
		uses               int64
		eq, ineq, included []string
	}
	var order []int64
	byHandle := map[int64]*suggestion{}
	err := each(ctx, db, fmt.Sprintf(`WITH best AS (
			SELECT TOP (%d) d.index_handle, d.object_id, gs.avg_user_impact, gs.user_seeks + gs.user_scans AS uses,
				gs.avg_total_user_cost * gs.avg_user_impact * (gs.user_seeks + gs.user_scans) AS benefit
			FROM sys.dm_db_missing_index_details d
			JOIN sys.dm_db_missing_index_groups g ON g.index_handle = d.index_handle
			JOIN sys.dm_db_missing_index_group_stats gs ON gs.group_handle = g.index_group_handle
			JOIN sys.objects o ON o.object_id = d.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
			WHERE d.database_id = DB_ID() AND %s ORDER BY benefit DESC)
		SELECT s.name, o.name, b.index_handle, b.avg_user_impact, b.uses, c.column_name, c.column_usage
		FROM best b JOIN sys.objects o ON o.object_id = b.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		CROSS APPLY sys.dm_db_missing_index_columns(b.index_handle) c
		ORDER BY b.benefit DESC, b.index_handle, c.column_id`, maxMissing, in), args,
		func(r *sql.Rows) error {
			var s, t, col, usage string
			var handle, uses int64
			var impact float64
			if err := r.Scan(&s, &t, &handle, &impact, &uses, &col, &usage); err != nil {
				return err
			}
			g := byHandle[handle]
			if g == nil {
				x := rel(s, t)
				if x == nil {
					return nil
				}
				g = &suggestion{x: x, impact: impact, uses: uses}
				byHandle[handle] = g
				order = append(order, handle)
			}
			switch usage {
			case "EQUALITY":
				g.eq = append(g.eq, col)
			case "INEQUALITY":
				g.ineq = append(g.ineq, col)
			case "INCLUDE":
				g.included = append(g.included, col)
			}
			return nil
		})
	if err != nil {
		return
	}
	for _, h := range order {
		g := byHandle[h]
		keys := append(slices.Clone(g.eq), g.ineq...)
		if len(keys) == 0 {
			continue
		}
		what := "(" + strings.Join(keys, ", ") + ")"
		if len(g.included) > 0 {
			what += " INCLUDE (" + strings.Join(g.included, ", ") + ")"
		}
		var how []string
		if len(g.eq) > 0 {
			how = append(how, strings.Join(g.eq, ", ")+" with =")
		}
		if len(g.ineq) > 0 {
			how = append(how, strings.Join(g.ineq, ", ")+" with another comparison")
		}
		level, col := "info", ""
		if g.impact >= missingWarnImpact && g.uses >= missingWarnUses {
			level = "warn"
		}
		if len(g.eq) > 0 {
			col = g.eq[0]
		}
		g.x.AddFinding("DBS-MSSQL-MISSING-INDEX", level, fmt.Sprintf("The optimizer suggests an index on %s for queries that compare %s. "+
			"It estimates they would cost %.0f%% less; they could have used it %d times since the server started. "+
			"The suggestion ignores similar existing indexes and the cost to writes, so compare it with the table's indexes first",
			what, strings.Join(how, " and "), g.impact, g.uses), col)
	}
}

// unusedIndexes flags nonclustered indexes no user query read since the
// server started but that writes kept up to date. Unique indexes and
// constraints are left out: they enforce a rule even when never read.
func unusedIndexes(ctx context.Context, db *sql.DB, in string, args []any, rel func(s, t string) *schema.Relation) {
	ctx, cancel := context.WithTimeout(ctx, indexStatsTimeout)
	defer cancel()
	var upSecs int64
	if err := db.QueryRowContext(ctx, `SELECT DATEDIFF_BIG(SECOND, sqlserver_start_time, SYSDATETIME()) FROM sys.dm_os_sys_info`).Scan(&upSecs); err != nil {
		return
	}
	up := time.Duration(upSecs) * time.Second
	if up < unusedMinUptime {
		return
	}
	each(ctx, db, `SELECT s.name, o.name, i.name, u.user_updates, coalesce(c.name, '')
		FROM sys.indexes i JOIN sys.objects o ON o.object_id = i.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		JOIN sys.dm_db_index_usage_stats u ON u.database_id = DB_ID() AND u.object_id = i.object_id AND u.index_id = i.index_id
		LEFT JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.key_ordinal = 1
		LEFT JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.type = 2 AND i.is_unique = 0 AND i.is_primary_key = 0 AND i.is_unique_constraint = 0
			AND i.is_disabled = 0 AND i.is_hypothetical = 0 AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0
			AND u.user_seeks = 0 AND u.user_scans = 0 AND u.user_lookups = 0 AND u.user_updates > 0 AND `+in+`
		ORDER BY s.name, o.name, i.name`, args,
		func(r *sql.Rows) error {
			var s, t, name, col string
			var updates int64
			if err := r.Scan(&s, &t, &name, &updates, &col); err != nil {
				return err
			}
			if x := rel(s, t); x != nil {
				x.AddFinding("DBS-MSSQL-UNUSED-INDEX", "warn", fmt.Sprintf("No query read the index %s since the server started %s ago, "+
					"but %d write statements paid to keep it up to date. If no rare job (a monthly report) needs it, dropping it saves that work",
					name, health.Duration(up), updates), col)
			}
			return nil
		})
}

type keyCol struct {
	name string
	desc bool
}

// rowIndex is a clustered or nonclustered rowstore index of one table.
type rowIndex struct {
	id                    int
	name, filter          string
	clustered, unique, pk bool
	keys                  []keyCol
	included              []string
}

// duplicateIndexes flags a nonclustered index another index of the table
// makes redundant (see redundant). Read from the catalog alone.
func duplicateIndexes(ctx context.Context, db *sql.DB, in string, args []any, rel func(s, t string) *schema.Relation) {
	tables := map[*schema.Relation][]*rowIndex{}
	var order []*schema.Relation
	// Without VIEW DEFINITION filter_definition is NULL, so a filter is only known through has_filter.
	err := each(ctx, db, `SELECT s.name, o.name, i.index_id, i.name, i.type, i.is_unique, i.is_primary_key,
			CASE WHEN i.has_filter = 0 THEN '' ELSE coalesce(i.filter_definition, '?') END, c.name, ic.is_included_column, ic.is_descending_key
		FROM sys.indexes i JOIN sys.objects o ON o.object_id = i.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
		JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.type IN (1, 2) AND i.is_disabled = 0 AND i.is_hypothetical = 0 AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0
			AND (ic.key_ordinal > 0 OR ic.is_included_column = 1) AND `+in+`
		ORDER BY s.name, o.name, i.index_id, ic.is_included_column, ic.key_ordinal, ic.index_column_id`, args,
		func(r *sql.Rows) error {
			var s, t, name, filter, col string
			var id, typ int
			var unique, pk, included, desc bool
			if err := r.Scan(&s, &t, &id, &name, &typ, &unique, &pk, &filter, &col, &included, &desc); err != nil {
				return err
			}
			x := rel(s, t)
			if x == nil {
				return nil
			}
			ixs := tables[x]
			if len(ixs) == 0 {
				order = append(order, x)
			}
			if n := len(ixs); n == 0 || ixs[n-1].id != id {
				ixs = append(ixs, &rowIndex{id: id, name: name, filter: filter, clustered: typ == 1, unique: unique, pk: pk})
				tables[x] = ixs
			}
			ix := ixs[len(ixs)-1]
			if included {
				ix.included = append(ix.included, col)
			} else {
				ix.keys = append(ix.keys, keyCol{col, desc})
			}
			return nil
		})
	if err != nil {
		return
	}
	for _, x := range order {
		ixs := tables[x]
		by := map[*rowIndex][]*rowIndex{}
		for _, r := range ixs {
			for _, k := range ixs {
				if r != k && redundant(r, k) {
					by[r] = append(by[r], k)
				}
			}
		}
		for _, r := range ixs {
			ks := by[r]
			if len(ks) == 0 {
				continue
			}
			// Name an index that stays: one that is not redundant itself.
			k := ks[0]
			for _, c := range ks {
				if len(by[c]) == 0 {
					k = c
					break
				}
			}
			msg := fmt.Sprintf("The index %s has the same key columns (%s) as %s", r.name, keyText(r.keys), k.name)
			if len(r.keys) < len(k.keys) {
				msg = fmt.Sprintf("The key columns of the index %s (%s) lead those of %s (%s), which serves the same seeks",
					r.name, keyText(r.keys), k.name, keyText(k.keys))
			}
			x.AddFinding("DBS-MSSQL-DUPLICATE-INDEX", "warn", msg+", so it looks redundant: every write maintains both, and dropping it loses no rule", r.keys[0].name)
		}
	}
}

// redundant reports whether k makes the nonclustered index r redundant: both
// have the same known filter ("?" is one the login may not read), k holds every column r includes, dropping r loses no
// uniqueness rule, and either their keys are the same columns in the same
// order and direction, or r's are a leading prefix of k's and neither is
// unique (a clustered k is left out: a narrow index on its leading key reads
// fewer pages for some queries).
func redundant(r, k *rowIndex) bool {
	if r.clustered || r.filter != k.filter || r.filter == "?" || len(r.keys) == 0 || len(r.keys) > len(k.keys) || len(notIn(r.included, k)) > 0 {
		return false
	}
	for i, c := range r.keys {
		if k.keys[i] != c {
			return false
		}
	}
	if len(r.keys) < len(k.keys) {
		return !k.clustered && !r.unique && !r.pk && !k.unique && !k.pk
	}
	if (r.unique || r.pk) && !k.unique && !k.pk {
		return false
	}
	if k.clustered || len(notIn(k.included, r)) > 0 {
		return true
	}
	// Each covers the other: keep the primary key, then a unique one, then the older.
	rank := func(i *rowIndex) []int { return []int{b2i(i.pk), b2i(i.unique), -i.id} }
	return slices.Compare(rank(k), rank(r)) > 0
}

// notIn returns the columns of cols that index k holds neither as a key nor
// included; a clustered index holds every column.
func notIn(cols []string, k *rowIndex) []string {
	if k.clustered {
		return nil
	}
	var out []string
	for _, c := range cols {
		if !slices.Contains(k.included, c) && !slices.ContainsFunc(k.keys, func(kc keyCol) bool { return kc.name == c }) {
			out = append(out, c)
		}
	}
	return out
}

func keyText(keys []keyCol) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k.name
		if k.desc {
			parts[i] += " DESC"
		}
	}
	return strings.Join(parts, ", ")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

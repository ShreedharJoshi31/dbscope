package cassandra

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// Base is the cluster and keyspace schema changes are checked against.
type Base struct {
	Server Server
	// Keyspace is what unqualified names resolve to.
	Keyspace string
	// Tables is keyed by lower-case "keyspace.table".
	Tables map[string]*TableInfo
	// Tablets are ScyllaDB keyspaces that use tablets, which refuse secondary indexes.
	Tablets map[string]bool
}

// TableInfo is what the rules need to know about one table.
type TableInfo struct {
	// KeyColumns are the partition and clustering columns.
	KeyColumns map[string]bool
	// Dropped maps a dropped column to its type at the time.
	Dropped map[string]string
	// Bytes estimates the table's size from this node's size_estimates; 0 when unknown.
	Bytes int64
}

// ReadBase reads key columns, dropped columns and size estimates of the
// keyspace's tables; every role can read system_schema and size_estimates.
func ReadBase(ctx context.Context, s *gocql.Session, keyspace string) (*Base, error) {
	srv, err := describe(ctx, s)
	if err != nil {
		return nil, err
	}
	b := &Base{Server: srv, Keyspace: keyspace, Tables: map[string]*TableInfo{}, Tablets: map[string]bool{}}
	table := func(ks, t string) *TableInfo {
		k := strings.ToLower(ks + "." + t)
		if b.Tables[k] == nil {
			b.Tables[k] = &TableInfo{KeyColumns: map[string]bool{}, Dropped: map[string]string{}}
		}
		return b.Tables[k]
	}
	iter := s.Query(`SELECT keyspace_name, table_name, column_name, kind FROM system_schema.columns`).WithContext(ctx).Iter()
	var ks, t, col, kind string
	for iter.Scan(&ks, &t, &col, &kind) {
		ti := table(ks, t)
		if kind == "partition_key" || kind == "clustering" {
			ti.KeyColumns[strings.ToLower(col)] = true
		}
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	iter = s.Query(`SELECT keyspace_name, table_name, column_name, type FROM system_schema.dropped_columns`).WithContext(ctx).Iter()
	var typ string
	for iter.Scan(&ks, &t, &col, &typ) {
		table(ks, t).Dropped[strings.ToLower(col)] = typ
	}
	iter.Close()
	iter = s.Query(`SELECT keyspace_name, table_name, mean_partition_size, partitions_count FROM system.size_estimates`).WithContext(ctx).Iter()
	var mean, count int64
	for iter.Scan(&ks, &t, &mean, &count) {
		if ti := b.Tables[strings.ToLower(ks+"."+t)]; ti != nil {
			ti.Bytes += mean * count
		}
	}
	iter.Close()
	if srv.Flavor == "scylla" {
		iter = s.Query(`SELECT keyspace_name, initial_tablets FROM system_schema.scylla_keyspaces`).WithContext(ctx).Iter()
		var n *int
		for iter.Scan(&ks, &n) {
			b.Tablets[strings.ToLower(ks)] = n != nil
		}
		iter.Close()
	}
	return b, nil
}

// Analyze rates CQL schema changes against base. Cassandra and ScyllaDB
// apply DDL without locking tables, so the risks are statements the server
// refuses (checked on Cassandra 5.0 and ScyllaDB 2025.1), builds that read
// the whole table on every node, lost data, and replication changes that
// move no data until a repair (docs/ENGINES.md, "Cassandra and ScyllaDB").
func Analyze(base *Base, files []ingest.Input) *migrate.Report {
	var major, minor, patch int
	fmt.Sscanf(base.Server.Version, "%d.%d.%d", &major, &minor, &patch)
	rep := &migrate.Report{Engine: base.Server.Flavor, PGVersion: major*10000 + minor*100 + patch, Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: []string{}, Base: "live",
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	if len(base.Tables) == 0 {
		rep.Base = "none"
	}
	a := &cqlAnalysis{base: base, rep: rep, altered: map[string]bool{}}
	for _, f := range files {
		for _, p := range splitCQL(f.SQL) {
			st := a.statement(f.Name, p)
			rep.Statements = append(rep.Statements, st)
			rep.Rating = score.Max(rep.Rating, st.Rating)
		}
	}
	for k := range a.altered {
		rep.Diff.Altered = append(rep.Diff.Altered, k)
	}
	sort.Strings(rep.Diff.Added)
	sort.Strings(rep.Diff.Dropped)
	sort.Strings(rep.Diff.Altered)
	return rep
}

type cqlAnalysis struct {
	base    *Base
	rep     *migrate.Report
	altered map[string]bool
}

type cqlPiece struct {
	SQL           string
	Line, EndLine int
}

// splitCQL divides a script at semicolons outside strings, quoted names,
// $$ bodies and comments.
func splitCQL(s string) []cqlPiece {
	var out []cqlPiece
	start, line, startLine := 0, 1, 1
	begun := false
	flush := func(end int) {
		if text := strings.TrimSpace(s[start:end]); begun && len(tokens(text)) > 0 {
			out = append(out, cqlPiece{SQL: text, Line: startLine, EndLine: line})
		}
	}
	skipTo := func(i, end int) int {
		for ; i < end && i < len(s); i++ {
			if s[i] == '\n' {
				line++
			}
		}
		return min(end, len(s))
	}
	for i := 0; i < len(s); {
		c := s[i]
		comment := strings.HasPrefix(s[i:], "--") || strings.HasPrefix(s[i:], "//") || strings.HasPrefix(s[i:], "/*")
		if !begun && !comment && !(c == ' ' || c == '\t' || c == '\n' || c == '\r') {
			begun, start, startLine = true, i, line
		}
		switch {
		case strings.HasPrefix(s[i:], "--") || strings.HasPrefix(s[i:], "//"):
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				i = len(s)
			} else {
				i += end
			}
		case strings.HasPrefix(s[i:], "/*"), strings.HasPrefix(s[i:], "$$"):
			open, closing := s[i:i+2], "*/"
			if open == "$$" {
				closing = "$$"
			}
			end := strings.Index(s[i+2:], closing)
			stop := len(s)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			i = skipTo(i, stop)
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = skipTo(i, j+1)
		case c == ';':
			flush(i)
			i++
			start, begun = i, false
		default:
			if c == '\n' {
				line++
			}
			i++
		}
	}
	flush(len(s))
	return out
}

func (a *cqlAnalysis) statement(file string, p cqlPiece) (out migrate.Statement) {
	out = migrate.Statement{File: file, Line: p.Line, Column: 1, EndLine: p.EndLine, SQL: p.SQL,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	add := func(id string, r score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
		out.Rating = score.Max(out.Rating, r)
	}
	defer func() {
		out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
	}()
	toks := tokens(p.SQL)
	at := func(i int) string {
		if i < len(toks) {
			return toks[i].word
		}
		return ""
	}
	scylla := a.base.Server.Flavor == "scylla"
	fails := func(msg, alt string) { add("DBS-CQL-FAILS", score.High, msg, alt) }
	out.Kind = strings.ToUpper(strings.TrimSpace(at(0) + " " + at(1)))

	switch {
	case at(0) == "alter" && at(1) == "table":
		ks, t, next := a.qualified(toks, skipIf(toks, 2))
		display, ti := ks+"."+t, a.base.Tables[strings.ToLower(ks+"."+t)]
		a.altered[display] = true
		switch at(next) {
		case "alter":
			fails("Changing a column's type is refused (Cassandra no longer supports it; ScyllaDB only between compatible types)",
				"Add a new column of the new type, write both, backfill, and move readers over")
		case "rename":
			for i := next + 1; i+2 < len(toks); i += 4 {
				col := toks[i].ident
				if ti != nil && !ti.KeyColumns[strings.ToLower(col)] {
					fails("RENAME "+col+" is refused: only primary key columns can be renamed", "")
					break
				}
				a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: col, Change: "renamed", To: toks[i+2].ident})
			}
			if len(out.Findings) == 0 {
				add("DBS-CQL-RENAME", score.Medium, "Renaming a primary key column breaks every query and prepared statement that names it", "")
			}
		case "drop":
			for _, col := range columnList(toks[next+1:]) {
				if ti != nil && ti.KeyColumns[strings.ToLower(col)] {
					fails("DROP "+col+" is refused: it is a primary key column", "")
					continue
				}
				a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: col, Change: "dropped"})
				add("DBS-CQL-DROPS-DATA", score.High, "DROP "+col+" makes the column's data unreadable at once; the space is freed only by compaction",
					"Stop writing and reading the column first; Cassandra will not let it come back with another type")
			}
		case "add":
			for _, nt := range columnTypes(toks[next+1:]) {
				a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: display, Column: nt[0], Change: "added"})
				if ti == nil {
					continue
				}
				old, dropped := ti.Dropped[strings.ToLower(nt[0])]
				if dropped && !scylla && !strings.EqualFold(normType(old), normType(nt[1])) {
					fails(fmt.Sprintf("ADD %s %s is refused: a column of that name was dropped with type %s, and Cassandra keeps it to read old data", nt[0], nt[1], old),
						"Use another column name")
				}
			}
		case "with":
			a.tableOptions(toks[next+1:], display, ti, add)
		}
	case at(0) == "create" && (at(1) == "index" || at(1) == "custom" && at(2) == "index"):
		on := indexWord(toks, "on")
		if on < 0 {
			break
		}
		ks, t, _ := a.qualified(toks, on+1)
		display, ti := ks+"."+t, a.base.Tables[strings.ToLower(ks+"."+t)]
		out.Kind, out.Work = "CREATE INDEX", "index_build"
		custom := at(1) == "custom"
		class := strings.ToLower(lastString(toks))
		switch {
		case scylla && custom:
			fails("ScyllaDB supports no CUSTOM INDEX class (SAI and SASI are Cassandra's)", "Use a plain CREATE INDEX or a materialized view")
		case scylla && a.base.Tablets[strings.ToLower(ks)]:
			fails("Secondary indexes are refused in keyspaces that use tablets", "Create the keyspace with tablets = {'enabled': false}, or keep a lookup table")
		case !scylla && custom && strings.Contains(class, "sasi"):
			fails("SASI indexes are disabled by default (sasi_indexes_enabled) and deprecated", "Use StorageAttachedIndex (SAI)")
		default:
			add("DBS-CQL-INDEX-BUILD", a.tier(ti), fmt.Sprintf("Building the index reads all of %s (%s) on every node in the background; queries that use it before the build ends return incomplete results",
				display, sizeText(ti)), "Create it at a quiet time and wait for the build in system.\"IndexInfo\" (Cassandra) or system.built_views before using it")
			if !custom && !scylla {
				add("DBS-CQL-LEGACY-INDEX", score.Low, "A legacy secondary index queries every node for each read without the partition key", "On Cassandra 5, prefer USING 'StorageAttachedIndex' (SAI)")
			}
		}
	case at(0) == "create" && at(1) == "materialized":
		out.Kind, out.Work = "CREATE MATERIALIZED VIEW", "index_build"
		from := indexWord(toks, "from")
		display, ti, ks := "", (*TableInfo)(nil), a.base.Keyspace
		if from >= 0 {
			var t string
			ks, t, _ = a.qualified(toks, from+1)
			display, ti = ks+"."+t, a.base.Tables[strings.ToLower(ks+"."+t)]
		}
		switch {
		case !scylla:
			fails("Materialized views are disabled by default (materialized_views_enabled) and experimental in Cassandra", "Maintain a second table from the application")
			return out
		case a.base.Tablets[strings.ToLower(ks)]:
			fails("Materialized views are refused on tables in keyspaces that use tablets", "Create the keyspace with tablets = {'enabled': false}, or maintain a second table")
			return out
		}
		add("DBS-CQL-INDEX-BUILD", a.tier(ti), fmt.Sprintf("Building the view reads all of %s (%s) on every node in the background", display, sizeText(ti)), "")
	case at(0) == "alter" && at(1) == "keyspace":
		if indexWord(toks, "replication") >= 0 {
			add("DBS-CQL-REPLICATION", score.High, "Changing replication moves no data: until a full repair, replicas added by the change answer reads without it",
				"Run nodetool repair -full on every node right after, and read at QUORUM or above until it finishes")
		}
	case at(0) == "drop" && (at(1) == "table" || at(1) == "keyspace" || at(1) == "materialized"):
		i := 2
		if at(1) == "materialized" {
			i = 3
		}
		ks, t, _ := a.qualified(toks, skipIf(toks, i))
		name := ks + "." + t
		if at(1) == "keyspace" {
			name = toks[skipIf(toks, i)].ident
		}
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, name)
		add("DBS-CQL-DROPS-DATA", score.High, fmt.Sprintf("DROP %s %s removes it and its data (with auto_snapshot on, each node keeps a snapshot, using disk until cleared)", strings.ToUpper(at(1)), name), "")
	case at(0) == "truncate":
		i := 1
		if at(1) == "table" {
			i = 2
		}
		ks, t, _ := a.qualified(toks, i)
		add("DBS-CQL-DROPS-DATA", score.High, "TRUNCATE deletes every row of "+ks+"."+t+"; it fails unless every node is up", "")
	case at(0) == "create" && at(1) == "table":
		ks, t, _ := a.qualified(toks, skipIf(toks, 2))
		a.rep.Diff.Added = append(a.rep.Diff.Added, ks+"."+t)
		a.tableOptions(toks, ks+"."+t, nil, add)
	}
	return out
}

// tableOptions rates WITH options: a compaction change rewrites every
// SSTable, gc_grace_seconds 0 can bring deleted data back, and a default
// TTL applies only to new writes.
func (a *cqlAnalysis) tableOptions(toks []token, display string, ti *TableInfo, add func(string, score.Rating, string, string)) {
	for i, t := range toks {
		switch t.word {
		case "compaction":
			if ti != nil {
				add("DBS-CQL-RECOMPACT", a.tier(ti), fmt.Sprintf("Changing compaction rewrites every SSTable of %s (%s) on each node, using disk and I/O while it runs", display, sizeText(ti)),
					"Change it at a quiet time with disk headroom for a second copy")
			}
		case "gc_grace_seconds":
			if i+2 < len(toks) && toks[i+2].text == "0" {
				add("DBS-CQL-GC-GRACE-ZERO", score.High, "gc_grace_seconds = 0 drops tombstones at the next compaction, so a replica that missed a delete brings the data back",
					"Keep it above the repair interval; ScyllaDB refuses 0 on tables with views or indexes")
			}
		case "default_time_to_live":
			if ti != nil {
				add("DBS-CQL-TTL", score.Low, "default_time_to_live applies only to rows written from now on; existing rows keep their TTL", "")
			}
		}
	}
}

// qualified reads [keyspace.]name at toks[i].
func (a *cqlAnalysis) qualified(toks []token, i int) (ks, name string, next int) {
	if i >= len(toks) {
		return a.base.Keyspace, "", i
	}
	if i+2 < len(toks) && toks[i+1].text == "." {
		return toks[i].ident, toks[i+2].ident, i + 3
	}
	return a.base.Keyspace, toks[i].ident, i + 1
}

func skipIf(toks []token, i int) int {
	for i < len(toks) && (toks[i].word == "if" || toks[i].word == "exists" || toks[i].word == "not") {
		i++
	}
	return i
}

func indexWord(toks []token, w string) int {
	for i, t := range toks {
		if t.word == w {
			return i
		}
	}
	return -1
}

// lastString returns the last string literal, the class of a CUSTOM INDEX.
func lastString(toks []token) string {
	for i := len(toks) - 1; i >= 0; i-- {
		if strings.HasPrefix(toks[i].text, "'") {
			return strings.Trim(toks[i].text, "'")
		}
	}
	return ""
}

// columnList reads "a" or "(a, b)" after DROP.
func columnList(toks []token) []string {
	var out []string
	for _, t := range toks {
		if t.ident != "" {
			out = append(out, t.ident)
		}
		if t.word == "using" {
			break
		}
	}
	if len(toks) > 0 && toks[0].text != "(" && len(out) > 1 {
		out = out[:1]
	}
	return out
}

// columnTypes reads "a int" or "(a int, b frozen<list<text>>)" after ADD.
func columnTypes(toks []token) [][2]string {
	if len(toks) > 0 && toks[0].text == "(" {
		toks = toks[1:]
	}
	var out [][2]string
	depth, cur := 0, []string{}
	flush := func() {
		if len(cur) >= 2 {
			out = append(out, [2]string{cur[0], strings.Join(cur[1:], "")})
		}
		cur = nil
	}
	for _, t := range toks {
		switch {
		case t.text == "<":
			depth++
		case t.text == ">":
			depth--
		case t.text == "," && depth == 0:
			flush()
			continue
		case t.text == ")" && depth == 0:
			flush()
			return out
		}
		name := t.text
		if len(cur) == 0 && t.ident != "" {
			name = t.ident
		}
		cur = append(cur, name)
	}
	flush()
	return out
}

func normType(t string) string { return strings.ToLower(strings.ReplaceAll(t, " ", "")) }

// tier rates a build or rewrite by the table's estimated size: under 1 GiB low, under 100 GiB medium.
func (a *cqlAnalysis) tier(ti *TableInfo) score.Rating {
	switch {
	case ti == nil || ti.Bytes == 0:
		return score.Medium
	case ti.Bytes >= 100<<30:
		return score.High
	case ti.Bytes >= 1<<30:
		return score.Medium
	}
	return score.Low
}

func sizeText(ti *TableInfo) string {
	switch {
	case ti == nil || ti.Bytes == 0:
		return "size unknown"
	case ti.Bytes >= 1<<30:
		return fmt.Sprintf("about %.1f GiB on this node", float64(ti.Bytes)/(1<<30))
	}
	return fmt.Sprintf("about %.1f MiB on this node", float64(ti.Bytes)/(1<<20))
}

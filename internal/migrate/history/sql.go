package history

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// Dialect is the SQL server a Querier talks to.
type Dialect string

const (
	Postgres   Dialect = "postgres"
	MySQL      Dialect = "mysql"
	SQLite     Dialect = "sqlite"
	ClickHouse Dialect = "clickhouse"
)

// Querier runs one read-only query and returns its rows as text (NULL is nil).
type Querier interface {
	Query(ctx context.Context, q string) ([][]*string, error)
}

// PgxQuerier is a pgx connection or pool.
type PgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Pgx reads PostgreSQL through pgx.
func Pgx(q PgxQuerier) Querier { return pgxQuerier{q} }

type pgxQuerier struct{ q PgxQuerier }

func (p pgxQuerier) Query(ctx context.Context, q string) ([][]*string, error) {
	rows, err := p.q.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]*string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make([]*string, len(vals))
		for i, v := range vals {
			row[i] = text(v)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func text(v any) *string {
	var s string
	switch v := v.(type) {
	case nil:
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	case time.Time:
		s = v.Format(time.RFC3339Nano)
	default:
		s = fmt.Sprint(v)
	}
	return &s
}

// DB reads MySQL or SQLite through database/sql.
func DB(db *sql.DB) Querier { return sqlQuerier{db} }

type sqlQuerier struct{ db *sql.DB }

func (s sqlQuerier) Query(ctx context.Context, q string) ([][]*string, error) {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]*string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]*string, len(cols))
		for i, v := range vals {
			if v.Valid {
				row[i] = &v.String
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ClickHouseConn reads ClickHouse; the queries here select only String columns.
func ClickHouseConn(c driver.Conn) Querier { return chQuerier{c} }

type chQuerier struct{ c driver.Conn }

func (c chQuerier) Query(ctx context.Context, q string) ([][]*string, error) {
	rows, err := c.c.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	n := len(rows.Columns())
	var out [][]*string
	for rows.Next() {
		row := make([]*string, n)
		dest := make([]any, n)
		for i := range row {
			row[i] = new(string)
			dest[i] = row[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// The history tables, each created exactly as its tool does in the tests:
//   - Flyway flyway_schema_history (createMetaDataTable.sql of each database plugin);
//   - golang-migrate schema_migrations (version, dirty): one row, the current version
//     (database/{postgres,mysql,sqlite3}); on ClickHouse one row per change, the
//     latest by sequence (database/clickhouse);
//   - goose goose_db_version (version_id, is_applied): the latest row per version wins
//     (internal/dialects);
//   - dbmate and Rails schema_migrations (version): one row per applied migration;
//   - Prisma _prisma_migrations (migration_name, checksum, finished_at, rolled_back_at).
const (
	flywayTable  = "flyway_schema_history"
	smTable      = "schema_migrations"
	gooseTable   = "goose_db_version"
	prismaTable  = "_prisma_migrations"
	sqlLookedFor = "flyway_schema_history, schema_migrations, goose_db_version and _prisma_migrations"
	chLookedFor  = "schema_migrations and goose_db_version"
)

// columns lists a table's columns in the connection's current schema or database; none when it is missing.
func columns(ctx context.Context, q Querier, d Dialect, table string) (map[string]bool, error) {
	var stmt string
	switch d {
	case Postgres:
		stmt = `SELECT attname::text FROM pg_attribute WHERE attrelid = to_regclass('` + table + `') AND attnum > 0 AND NOT attisdropped`
	case MySQL:
		stmt = `SELECT column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '` + table + `'`
	case SQLite:
		stmt = `SELECT name FROM pragma_table_info('` + table + `')`
	case ClickHouse:
		stmt = `SELECT name FROM system.columns WHERE database = currentDatabase() AND table = '` + table + `'`
	}
	rows, err := q.Query(ctx, stmt)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		if r[0] != nil {
			out[strings.ToLower(*r[0])] = true
		}
	}
	return out, nil
}

// SQL reads the history of the tool that migrates a PostgreSQL, MySQL, SQLite or ClickHouse database.
func SQL(ctx context.Context, q Querier, d Dialect, files []ingest.Input) Result {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	tables := []string{flywayTable, smTable, gooseTable, prismaTable}
	looked := sqlLookedFor
	if d == ClickHouse {
		tables, looked = []string{smTable, gooseTable}, chLookedFor
	}
	found := map[string]map[string]bool{}
	for _, t := range tables {
		cols, err := columns(ctx, q, d, t)
		if err != nil {
			return readFailed("the migration history tables", err)
		}
		if len(cols) > 0 {
			found[t] = cols
		}
	}
	type candidate struct {
		tool, table string
		read        func() Result
	}
	var cands []candidate
	if found[flywayTable] != nil {
		cands = append(cands, candidate{"flyway", flywayTable, func() Result { return flyway(ctx, q, files) }})
	}
	if cols := found[smTable]; cols != nil {
		switch {
		case cols["dirty"]:
			cands = append(cands, candidate{"golang-migrate", smTable, func() Result { return golangMigrateHistory(ctx, q, d, files) }})
		case d == ClickHouse:
			return Result{Warnings: []string{"schema_migrations has no dirty column, so it is not golang-migrate's; dbscope reads only golang-migrate and goose histories on ClickHouse; every file is checked"}}
		default:
			tool := "rails"
			if slices.ContainsFunc(files, func(f ingest.Input) bool { return dbmateMarker.MatchString(f.SQL) }) {
				tool = "dbmate"
			}
			cands = append(cands, candidate{tool, smTable, func() Result { return versionTable(ctx, q, tool, files) }})
		}
	}
	if found[gooseTable] != nil {
		cands = append(cands, candidate{"goose", gooseTable, func() Result { return goose(ctx, q, d, files) }})
	}
	if found[prismaTable] != nil {
		cands = append(cands, candidate{"prisma", prismaTable, func() Result { return prisma(ctx, q, files) }})
	}
	if len(cands) == 0 {
		return notFound(looked)
	}
	want := tool(files)
	for _, c := range cands {
		if c.tool == want {
			return c.read()
		}
	}
	if len(cands) == 1 {
		return cands[0].read()
	}
	// Several histories and files that do not say which tool: the first that names any of them.
	var names []string
	for _, c := range cands {
		names = append(names, c.table)
	}
	for _, c := range cands {
		if r := c.read(); r.Pending != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("found %s; the files' names match %s's", strings.Join(names, ", "), c.tool))
			return r
		}
	}
	return Result{Warnings: []string{fmt.Sprintf("found %s, but no file is named the way their tools name migrations; every file is checked", strings.Join(names, ", "))}}
}

// tool is the migration tool the files' names and contents point to ("" when unclear).
func tool(files []ingest.Input) string {
	names := make([]ingest.Input, 0, len(files))
	for _, f := range files {
		names = append(names, ingest.Input{Name: f.Name}) // detection reads names only
	}
	switch ingest.Load(names, ingest.Options{}).Framework {
	case ingest.Flyway:
		return "flyway"
	case ingest.GolangMigrate:
		return "golang-migrate"
	case ingest.Prisma:
		return "prisma"
	}
	switch {
	case slices.ContainsFunc(files, func(f ingest.Input) bool { return gooseMarker.MatchString(f.SQL) }):
		return "goose"
	case slices.ContainsFunc(files, func(f ingest.Input) bool { return dbmateMarker.MatchString(f.SQL) }):
		return "dbmate"
	}
	return ""
}

func truthy(s *string) bool {
	if s == nil {
		return false
	}
	switch strings.ToLower(*s) {
	case "1", "t", "true":
		return true
	}
	return false
}

func flyway(ctx context.Context, q Querier, files []ingest.Input) Result {
	rows, err := q.Query(ctx, `SELECT version, type, script, checksum, success FROM flyway_schema_history ORDER BY installed_rank LIMIT `+strconv.Itoa(maxRows))
	if err != nil {
		return readFailed(flywayTable, err)
	}
	versions := map[string]*string{} // version -> checksum of its applied run
	repeatables := map[string]*string{}
	baseline := ""
	for _, r := range rows {
		ver, typ, script, sum, ok := r[0], "", "", r[3], truthy(r[4])
		if r[1] != nil {
			typ = strings.ToUpper(*r[1])
		}
		if r[2] != nil {
			script = base(*r[2])
		}
		v := ""
		if ver != nil {
			v = version(*ver)
		}
		switch {
		case typ == "SCHEMA":
		case typ == "BASELINE" && ok:
			baseline = v
		case typ == "DELETE" || strings.HasPrefix(typ, "UNDO"):
			// repair's DELETE and an undo both mean the migration is no longer applied.
			if ok {
				delete(versions, v)
				delete(repeatables, script)
			}
		case !ok:
			// A failed run leaves the migration pending (PostgreSQL rolls it back; Flyway reruns it after repair).
			if ver != nil {
				delete(versions, v)
			}
		case ver != nil:
			versions[v] = sum
		default:
			repeatables[script] = sum
		}
	}
	res := classify("flyway", flywayTable, files, func(f ingest.Input) state {
		b := base(f.Name)
		m := flywayV.FindStringSubmatch(b)
		var sum *string
		var ok bool
		switch {
		case m != nil:
			v := version(m[1])
			if sum, ok = versions[v]; !ok {
				if baseline != "" && cmpVersion(v, baseline) <= 0 {
					return applied
				}
				return pending
			}
		case flywayR.MatchString(b):
			if sum, ok = repeatables[b]; !ok {
				return pending
			}
		default:
			return other
		}
		if sum != nil && *sum != strconv.Itoa(int(FlywayChecksum(f.SQL))) {
			return changed
		}
		return applied
	})
	return res
}

func golangMigrateHistory(ctx context.Context, q Querier, d Dialect, files []ingest.Input) Result {
	stmt := `SELECT version, dirty FROM schema_migrations LIMIT 1`
	if d == ClickHouse {
		stmt = `SELECT toString(version), toString(dirty) FROM schema_migrations ORDER BY sequence DESC LIMIT 1`
	}
	rows, err := q.Query(ctx, stmt)
	if err != nil {
		return readFailed(smTable, err)
	}
	current, dirty := int64(-1), false
	if len(rows) > 0 && rows[0][0] != nil {
		if current, err = strconv.ParseInt(*rows[0][0], 10, 64); err != nil {
			return readFailed(smTable, fmt.Errorf("version %q is not a number", *rows[0][0]))
		}
		dirty = truthy(rows[0][1])
	}
	res := classify("golang-migrate", smTable, files, func(f ingest.Input) state {
		m := golangMigrate.FindStringSubmatch(base(f.Name))
		if m == nil {
			return other
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return other
		}
		if v < current || v == current && !dirty {
			return applied
		}
		return pending
	})
	if dirty {
		res.Warnings = append(res.Warnings, fmt.Sprintf("golang-migrate marks version %d dirty: it failed part way, so its file is checked again", current))
	}
	return res
}

func goose(ctx context.Context, q Querier, d Dialect, files []ingest.Input) Result {
	stmt := `SELECT version_id, is_applied FROM goose_db_version ORDER BY id DESC LIMIT ` + strconv.Itoa(maxRows)
	if d == ClickHouse {
		// goose's ClickHouse table has no id; down migrations delete the version's rows.
		stmt = `SELECT toString(version_id), toString(is_applied) FROM goose_db_version ORDER BY tstamp DESC LIMIT ` + strconv.Itoa(maxRows)
	}
	rows, err := q.Query(ctx, stmt)
	if err != nil {
		return readFailed(gooseTable, err)
	}
	latest := map[int64]bool{}
	for _, r := range rows {
		if r[0] == nil {
			continue
		}
		v, err := strconv.ParseInt(*r[0], 10, 64)
		if err != nil {
			continue
		}
		if _, seen := latest[v]; !seen {
			latest[v] = truthy(r[1])
		}
	}
	return classify("goose", gooseTable, files, func(f ingest.Input) state {
		m := numbered.FindStringSubmatch(base(f.Name))
		if m == nil || strings.HasSuffix(f.Name, ".down.sql") {
			return other
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || v == 0 {
			return other
		}
		if latest[v] {
			return applied
		}
		return pending
	})
}

// versionTable reads dbmate's or Rails' schema_migrations: the version is the file name's leading digits.
func versionTable(ctx context.Context, q Querier, tool string, files []ingest.Input) Result {
	rows, err := q.Query(ctx, `SELECT version FROM schema_migrations LIMIT `+strconv.Itoa(maxRows))
	if err != nil {
		return readFailed(smTable, err)
	}
	have := map[string]bool{}
	for _, r := range rows {
		if r[0] != nil {
			have[*r[0]] = true
		}
	}
	return classify(tool, smTable, files, func(f ingest.Input) state {
		m := numbered.FindStringSubmatch(base(f.Name))
		if m == nil {
			return other
		}
		if have[m[1]] {
			return applied
		}
		return pending
	})
}

func prisma(ctx context.Context, q Querier, files []ingest.Input) Result {
	rows, err := q.Query(ctx, `SELECT migration_name, checksum, finished_at, rolled_back_at FROM _prisma_migrations LIMIT `+strconv.Itoa(maxRows))
	if err != nil {
		return readFailed(prismaTable, err)
	}
	sums := map[string][]string{} // migration -> checksums of its finished, not rolled back runs
	for _, r := range rows {
		if r[0] == nil || r[2] == nil || r[3] != nil {
			continue
		}
		sum := ""
		if r[1] != nil {
			sum = strings.ToLower(*r[1])
		}
		sums[*r[0]] = append(sums[*r[0]], sum)
	}
	return classify("prisma", prismaTable, files, func(f ingest.Input) state {
		m := prismaPath.FindStringSubmatch(f.Name)
		if m == nil {
			return other
		}
		got, ok := sums[m[1]]
		if !ok {
			return pending
		}
		if slices.ContainsFunc(got, func(sum string) bool { return PrismaChecksumMatches(f.SQL, sum) }) {
			return applied
		}
		return changed
	})
}

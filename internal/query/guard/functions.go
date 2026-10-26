package guard

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Querier is satisfied by pgx.Tx, *pgx.Conn and *pgxpool.Pool.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// denied are STABLE built-ins that must still not run: the txid functions
// assign a transaction id, and the *_to_xml functions return a whole table,
// schema or database as one value, which defeats the row limit.
var denied = map[string]string{
	"txid_current":                   "it assigns a transaction ID",
	"txid_current_if_assigned":       "it reads transaction ID assignment",
	"txid_current_snapshot":          "it reads the transaction snapshot",
	"pg_current_xact_id":             "it assigns a transaction ID",
	"pg_current_xact_id_if_assigned": "it reads transaction ID assignment",
	"table_to_xml":                   "it returns a whole table as one value",
	"table_to_xmlschema":             "it is part of the table export functions",
	"table_to_xml_and_xmlschema":     "it returns a whole table as one value",
	"schema_to_xml":                  "it returns a whole schema as one value",
	"schema_to_xmlschema":            "it is part of the schema export functions",
	"schema_to_xml_and_xmlschema":    "it returns a whole schema as one value",
	"database_to_xml":                "it returns a whole database as one value",
	"database_to_xmlschema":          "it is part of the database export functions",
	"database_to_xml_and_xmlschema":  "it returns a whole database as one value",
}

// volatileAllowed are VOLATILE functions that only produce values.
var volatileAllowed = map[string]bool{
	"random": true, "random_normal": true, "clock_timestamp": true, "timeofday": true,
	"gen_random_uuid": true, "uuid_generate_v4": true,
}

// overloadsQuery lists every function with a called name: in the given
// schema for qualified calls, else in any schema on the search path.
const overloadsQuery = `SELECT f.ord::int, n.nspname::text, p.oid::regprocedure::text, p.provolatile::text,
       EXISTS (SELECT 1 FROM pg_depend d
               WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
FROM unnest($1::text[], $2::text[]) WITH ORDINALITY AS f(schema, name, ord)
JOIN pg_namespace n ON CASE WHEN f.schema = '' THEN n.nspname = ANY(coalesce($3::text[], current_schemas(true)))
                            ELSE n.nspname = f.schema END
JOIN pg_proc p ON p.pronamespace = n.oid AND p.proname = f.name
ORDER BY 1, 2, 3`

// AllowFunctions rejects the call unless every overload of every named
// function is not denylisted, is IMMUTABLE or STABLE or on the small
// VOLATILE allowlist, and lives in pg_catalog or an installed extension.
// Unqualified names are resolved against searchPath plus pg_catalog, or,
// when searchPath is nil, against the session's effective search path
// (current_schemas(true)), so q should be the transaction that will run
// the statement. Names with no match are left for PostgreSQL to report.
func AllowFunctions(ctx context.Context, q Querier, names []FuncName, searchPath []string) error {
	if len(names) == 0 {
		return nil
	}
	var path []string
	if searchPath != nil {
		path = append([]string{"pg_catalog"}, searchPath...)
	}
	schemas := make([]string, len(names))
	fnames := make([]string, len(names))
	for i, f := range names {
		if strings.HasPrefix(f.Schema, "pg_temp") {
			return reject(FunctionNotAllowed, "%s is not allowed: functions in temporary schemas cannot run", f)
		}
		schemas[i], fnames[i] = f.Schema, f.Name
	}
	rows, err := q.Query(ctx, overloadsQuery, pgx.QueryExecModeExec, schemas, fnames, path)
	if err != nil {
		return fmt.Errorf("check functions: %w", err)
	}
	why := map[int]string{}
	var ord int
	var nsp, sig, volatile string
	var ext bool
	if _, err := pgx.ForEachRow(rows, []any{&ord, &nsp, &sig, &volatile, &ext}, func() error {
		if _, done := why[ord]; done {
			return nil
		}
		if r := overloadProblem(names[ord-1].Name, nsp, sig, volatile, ext); r != "" {
			why[ord] = r
		}
		return nil
	}); err != nil {
		return fmt.Errorf("check functions: %w", err)
	}
	if len(why) == 0 {
		return nil
	}
	ords := make([]int, 0, len(why))
	for o := range why {
		ords = append(ords, o)
	}
	slices.Sort(ords)
	msgs := make([]string, len(ords))
	for i, o := range ords {
		msgs[i] = names[o-1].String() + " is not allowed: " + why[o]
	}
	return reject(FunctionNotAllowed, "%s", strings.Join(msgs, "; "))
}

// overloadProblem returns why one overload fails the rule, or "".
func overloadProblem(name, nsp, sig, volatile string, ext bool) string {
	if r, ok := denied[name]; ok {
		return r
	}
	if volatile == "v" && !volatileAllowed[name] {
		return fmt.Sprintf("%s.%s is VOLATILE, so it may change data or wait", nsp, sig)
	}
	if nsp != "pg_catalog" && !ext {
		return fmt.Sprintf("%s.%s is a user-defined function; only built-in and extension functions can run", nsp, sig)
	}
	return ""
}

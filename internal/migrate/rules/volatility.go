package rules

import (
	"slices"
	"sort"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/dbscope/dbscope/internal/migrate/apply"
)

// Volatility of a DEFAULT expression decides whether ADD COLUMN rewrites the
// table (docs/MIGRATIONS.md; ALTER TABLE Notes): PostgreSQL 11+ evaluates a
// non-volatile default once and stores it in the catalog, while a volatile
// one is evaluated per row, which rewrites the table.
//
// The rule dbscope applies, from the function names in the expression:
//   - a call to a built-in VOLATILE function (list below, from pg_proc
//     provolatile = 'v') makes the default volatile;
//   - now(), CURRENT_TIMESTAMP, CURRENT_DATE, LOCALTIMESTAMP, CURRENT_USER
//     and the other SQL value functions are STABLE: constant within the
//     statement, so no rewrite;
//   - calls to a known IMMUTABLE or STABLE built-in (list below) are fine;
//   - any other function (user-defined or not listed) is assumed VOLATILE,
//     because dbscope cannot see its declaration; the finding says so.
//
// Constants, casts and operators on constants are not volatile.

// volatileBuiltins are the VOLATILE functions migrations use in defaults.
var volatileBuiltins = map[string]bool{
	"clock_timestamp": true, "random": true, "random_normal": true, "gen_random_uuid": true,
	"uuid_generate_v1": true, "uuid_generate_v1mc": true, "uuid_generate_v4": true, "uuidv4": true, "uuidv7": true,
	"timeofday": true, "nextval": true, "setval": true, "currval": true, "lastval": true, "txid_current": true,
	"pg_current_xact_id": true, "setseed": true, "gen_random_bytes": true,
}

// stableOrImmutable are common built-ins known not to be VOLATILE.
var stableOrImmutable = map[string]bool{
	"now": true, "statement_timestamp": true, "transaction_timestamp": true, "current_date": true,
	"lower": true, "upper": true, "coalesce": true, "concat": true, "concat_ws": true, "length": true,
	"md5": true, "to_char": true, "to_date": true, "to_timestamp": true, "make_date": true, "make_timestamp": true,
	"make_timestamptz": true, "make_interval": true, "date_trunc": true, "jsonb_build_object": true,
	"json_build_object": true, "jsonb_build_array": true, "json_build_array": true, "array_fill": true,
	"timezone": true, "current_setting": true, "current_schema": true, "current_user": true, "session_user": true,
	"trim": true, "btrim": true, "ltrim": true, "rtrim": true, "substr": true, "substring": true, "replace": true,
	"greatest": true, "least": true, "abs": true, "round": true, "floor": true, "ceil": true, "nullif": true,
	"pg_catalog.now": true, "uuid_nil": true, "inet_client_addr": true,
}

// Volatility is the outcome for one expression.
type Volatility struct {
	Volatile bool
	// Functions are the function names that made it volatile; Assumed are
	// the ones dbscope does not know and treated as volatile.
	Functions, Assumed []string
}

// ExprVolatility inspects every function call in expr.
func ExprVolatility(expr *pg_query.Node) Volatility {
	var v Volatility
	apply.Walk(expr, func(m protoreflect.Message) {
		fc, ok := m.Interface().(*pg_query.FuncCall)
		if !ok {
			return
		}
		nsp, name := apply.QualifiedFromList(fc.GetFuncname())
		builtin := nsp == "" || nsp == "pg_catalog"
		switch {
		case builtin && volatileBuiltins[name]:
			v.Functions = append(v.Functions, name+"()")
		case builtin && stableOrImmutable[name]:
		default:
			full := name
			if nsp != "" {
				full = nsp + "." + name
			}
			v.Assumed = append(v.Assumed, full+"()")
		}
	})
	v.Functions, v.Assumed = uniq(v.Functions), uniq(v.Assumed)
	v.Volatile = len(v.Functions) > 0 || len(v.Assumed) > 0
	return v
}

func uniq(s []string) []string {
	sort.Strings(s)
	return slices.Compact(s)
}

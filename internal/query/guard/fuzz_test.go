package guard

import (
	"slices"
	"testing"
)

// FuzzGuard asserts that anything Check accepts re-parses to exactly one
// SELECT with no data-modifying statement, INTO or locking clause, and
// that the reported functions match the text that runs.
func FuzzGuard(f *testing.F) {
	for _, c := range checkCases {
		f.Add(c.sql)
	}
	for _, s := range []string{
		"SELECT * FROM t WHERE a = $1",
		"SELECT ARRAY(SELECT 1)::int[]",
		"SELECT xmlelement(name a, 'b')",
		"SELECT * FROM xmltable('/a' PASSING '<a/>' COLUMNS x int)",
		"SELECT * FROM t TABLESAMPLE system (10)",
		"SELECT json_object('a': 1)",
		"SELECT 1 FROM t GROUP BY GROUPING SETS ((a), (b))",
		"SELECT x COLLATE \"C\" FROM t",
		"SELECT (SELECT 1 FROM t LIMIT 1 OFFSET 2)",
		"SELECT CASE WHEN a THEN b END, COALESCE(a, b), NULLIF(a, b), GREATEST(a, b)",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		p, err := Check(sql)
		if err != nil {
			return
		}
		assertSafe(t, p)
		fns, err := checkText(p.Deparsed)
		if err != nil {
			t.Fatalf("deparsed %q of accepted %q is rejected: %v", p.Deparsed, sql, err)
		}
		if !slices.Equal(fns, p.Functions) {
			t.Fatalf("functions %v, deparsed has %v", p.Functions, fns)
		}
	})
}

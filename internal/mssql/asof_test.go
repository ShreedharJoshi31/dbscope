package mssql

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

var asOfAt = time.Date(2026, 11, 14, 19, 39, 30, 450000099, time.UTC)

// fakeCatalog: dbo.acct and dbo.acct2 are temporal, dbo.plain is not,
// dbo.v is a view, dbo.acct_history a history table, dbo.locked lacks SELECT on its history.
func fakeCatalog(refs []timetravel.Ref) ([]timetravel.Table, error) {
	temporal := &schema.TimeTravel{Kind: schema.TimeTravelTemporal, Available: true}
	rels := map[string]*schema.Relation{
		"dbo.acct":         {Schema: "dbo", Name: "acct", Kind: schema.KindTable, TimeTravel: temporal},
		"dbo.acct2":        {Schema: "dbo", Name: "acct2", Kind: schema.KindTable, TimeTravel: temporal},
		"dbo.plain":        {Schema: "dbo", Name: "plain", Kind: schema.KindTable},
		"dbo.v":            {Schema: "dbo", Name: "v", Kind: schema.KindView},
		"dbo.acct_history": {Schema: "dbo", Name: "acct_history", Kind: schema.KindTable, TimeTravel: &schema.TimeTravel{Kind: schema.TimeTravelHistory, Available: true, HistoryOf: "dbo.acct"}},
		"dbo.locked": {Schema: "dbo", Name: "locked", Kind: schema.KindTable,
			TimeTravel: &schema.TimeTravel{Kind: schema.TimeTravelTemporal, Note: "Needs SELECT on the history table dbo.locked_history"}},
	}
	out := make([]timetravel.Table, len(refs))
	for i, r := range refs {
		out[i].Ref = r
		parts := []string{"dbo"}
		for _, p := range r.Parts {
			parts = append(parts, strings.ToLower(p.Name))
		}
		if len(r.Parts) > 2 {
			out[i].Why = "is in another database or server, which cannot be checked"
			continue
		}
		out[i].Rel = rels[strings.Join(parts[len(parts)-2:], ".")]
	}
	return out, nil
}

func TestAsOfRewrite(t *testing.T) {
	c := AsOfClause(asOfAt)
	if c != "FOR SYSTEM_TIME AS OF '2026-11-14T19:39:30.4500000'" {
		t.Fatalf("clause %s", c)
	}
	if got := AsOfClause(asOfAt.In(time.FixedZone("IST", 5*3600+1800))); got != c {
		t.Fatalf("clause in another zone %s", got)
	}
	for _, tc := range []struct{ name, in, want string }{
		{"alias with AS", "SELECT a.bal FROM dbo.acct AS a WHERE a.id = 1", "SELECT a.bal FROM dbo.acct " + c + " AS a WHERE a.id = 1"},
		{"unqualified", "SELECT * FROM acct", "SELECT * FROM acct " + c},
		{"bracketed", "SELECT * FROM [dbo].[acct] x", "SELECT * FROM [dbo].[acct] " + c + " x"},
		{"double-quoted", `SELECT * FROM "dbo"."acct"`, `SELECT * FROM "dbo"."acct" ` + c},
		{"hint after alias", "SELECT * FROM dbo.acct a WITH (NOLOCK)", "SELECT * FROM dbo.acct " + c + " a WITH (NOLOCK)"},
		{"joins", "SELECT * FROM dbo.acct a INNER JOIN dbo.acct2 b ON a.id = b.id LEFT JOIN acct c ON c.id = a.id CROSS JOIN acct2",
			"SELECT * FROM dbo.acct " + c + " a INNER JOIN dbo.acct2 " + c + " b ON a.id = b.id LEFT JOIN acct " + c +
				" c ON c.id = a.id CROSS JOIN acct2 " + c},
		{"comma join", "SELECT * FROM dbo.acct a, dbo.acct2 b", "SELECT * FROM dbo.acct " + c + " a, dbo.acct2 " + c + " b"},
		{"derived table and apply", "SELECT * FROM (SELECT TOP 5 id FROM dbo.acct) x CROSS APPLY (SELECT bal FROM acct2 y WHERE y.id = x.id) z",
			"SELECT * FROM (SELECT TOP 5 id FROM dbo.acct " + c + ") x CROSS APPLY (SELECT bal FROM acct2 " + c + " y WHERE y.id = x.id) z"},
		{"subquery in WHERE", "SELECT * FROM acct WHERE EXISTS (SELECT 1 FROM acct2 WHERE acct2.id = acct.id)",
			"SELECT * FROM acct " + c + " WHERE EXISTS (SELECT 1 FROM acct2 " + c + " WHERE acct2.id = acct.id)"},
		{"union", "SELECT id FROM acct UNION SELECT id FROM acct2", "SELECT id FROM acct " + c + " UNION SELECT id FROM acct2 " + c},
		{"cte", "WITH c AS (SELECT id FROM dbo.acct) SELECT * FROM c JOIN acct2 b ON b.id = c.id",
			"WITH c AS (SELECT id FROM dbo.acct " + c + ") SELECT * FROM c JOIN acct2 " + c + " b ON b.id = c.id"},
		{"strings and comments", "SELECT 'FROM dbo.plain' /* FROM dbo.plain /* nested */ */ FROM acct -- FROM plain",
			"SELECT 'FROM dbo.plain' /* FROM dbo.plain /* nested */ */ FROM acct " + c + " -- FROM plain"},
		{"table function", "SELECT * FROM OPENJSON(N'[1]') j, acct", "SELECT * FROM OPENJSON(N'[1]') j, acct " + c},
		{"option", "SELECT * FROM acct OPTION (MAXDOP 1, RECOMPILE)", "SELECT * FROM acct " + c + " OPTION (MAXDOP 1, RECOMPILE)"},
		{"values", "SELECT * FROM (VALUES (1), (2)) v(x)", "SELECT * FROM (VALUES (1), (2)) v(x)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AsOf(tc.in, asOfAt, fakeCatalog)
			if err != nil {
				t.Fatalf("%s: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
			if err := Check(got); err != nil {
				t.Fatalf("Check refused the rewrite: %v", err)
			}
		})
	}
}

func TestAsOfRefuses(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"existing FOR SYSTEM_TIME", "SELECT * FROM acct FOR SYSTEM_TIME ALL", "already reads history (FOR SYSTEM_TIME)"},
		{"unknown table", "SELECT * FROM dbo.nope", "dbo.nope is not a table or view this user can see"},
		{"non-temporal table", "SELECT * FROM acct JOIN plain p ON 1 = 1", "plain is not a system-versioned temporal table"},
		{"view", "SELECT * FROM dbo.v", "dbo.v is a view, which can mix temporal and current tables"},
		{"history table", "SELECT * FROM acct_history", "is the history table of dbo.acct"},
		{"history not readable", "SELECT * FROM locked", "Needs SELECT on the history table dbo.locked_history"},
		{"other database", "SELECT * FROM other.dbo.acct", "is in another database"},
		{"old-style hint", "SELECT * FROM acct (NOLOCK)", "WITH (NOLOCK)"},
		{"containstable", "SELECT * FROM CONTAINSTABLE(acct, *, 'x')", "CONTAINSTABLE reads a table"},
		{"tablesample", "SELECT * FROM acct TABLESAMPLE (10 PERCENT)", "TABLESAMPLE"},
		{"parenthesized join", "SELECT * FROM (acct a JOIN acct2 b ON a.id = b.id)", "parenthesized join"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AsOf(tc.in, asOfAt, fakeCatalog)
			var te *timetravel.Error
			if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.As(err, &te) {
				t.Fatalf("%s: err %v, want a refusal containing %q", tc.in, err, tc.want)
			}
		})
	}
}

// TestAsOfKeepsGuard: a statement Check refuses is refused by the rewriter or,
// once rewritten, by Check with the same error.
func TestAsOfKeepsGuard(t *testing.T) {
	for _, q := range []string{
		"DELETE FROM acct", "UPDATE acct SET bal = 0", "INSERT INTO acct SELECT * FROM acct2", "SELECT * INTO x FROM acct",
		"SELECT * FROM acct WITH (UPDLOCK)", "SELECT * FROM acct; DROP TABLE acct", "SELECT 1 FROM acct DELETE FROM acct",
		"SELECT * FROM OPENROWSET('SQLNCLI', 'x', 'SELECT 1')",
	} {
		before := Check(q)
		if before == nil {
			t.Fatalf("%s: Check passed", q)
		}
		got, err := AsOf(q, asOfAt, fakeCatalog)
		if err != nil {
			continue
		}
		if after := Check(got); after == nil || after.Error() != before.Error() {
			t.Errorf("%s: rewritten to %s, Check %v, want %v", q, got, after, before)
		}
	}
}

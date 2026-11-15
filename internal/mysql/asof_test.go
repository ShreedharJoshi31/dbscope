package mysql

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

var asOfAt = time.Date(2026, 11, 14, 19, 39, 30, 450001999, time.UTC)

// fakeCatalog: app.acct and app.acct2 are system-versioned, app.plain is not, app.v is a view.
func fakeCatalog(refs []timetravel.Ref) ([]timetravel.Table, error) {
	versioned := &schema.TimeTravel{Kind: schema.TimeTravelSystemVersioned, Available: true}
	rels := map[string]*schema.Relation{
		"app.acct":  {Schema: "app", Name: "acct", Kind: schema.KindTable, TimeTravel: versioned},
		"app.acct2": {Schema: "app", Name: "acct2", Kind: schema.KindTable, TimeTravel: versioned},
		"app.plain": {Schema: "app", Name: "plain", Kind: schema.KindTable},
		"app.v":     {Schema: "app", Name: "v", Kind: schema.KindView},
	}
	out := make([]timetravel.Table, len(refs))
	for i, r := range refs {
		out[i].Ref = r
		parts := []string{"app"}
		for _, p := range r.Parts {
			parts = append(parts, p.Name)
		}
		out[i].Rel = rels[strings.Join(parts[len(parts)-2:], ".")]
	}
	return out, nil
}

func TestAsOfRewrite(t *testing.T) {
	c := AsOfClause(asOfAt)
	if c != "FOR SYSTEM_TIME AS OF FROM_UNIXTIME(1794685170.450001)" {
		t.Fatalf("clause %s", c)
	}
	if got := AsOfClause(time.Date(2026, 1, 1, 0, 0, 0, 5000, time.UTC)); got != "FOR SYSTEM_TIME AS OF FROM_UNIXTIME(1767225600.000005)" {
		t.Fatalf("small fraction %s", got)
	}
	for _, tc := range []struct{ name, in, want string }{
		{"alias", "SELECT a.bal FROM acct AS a WHERE a.id = 1", "SELECT a.bal FROM acct " + c + " AS a WHERE a.id = 1"},
		{"backquoted and qualified", "SELECT * FROM `app`.`acct` x", "SELECT * FROM `app`.`acct` " + c + " x"},
		{"joins", "SELECT * FROM app.acct a INNER JOIN acct2 b ON a.id = b.id LEFT JOIN acct c USING (id) CROSS JOIN acct2 STRAIGHT_JOIN acct",
			"SELECT * FROM app.acct " + c + " a INNER JOIN acct2 " + c + " b ON a.id = b.id LEFT JOIN acct " + c +
				" c USING (id) CROSS JOIN acct2 " + c + " STRAIGHT_JOIN acct " + c},
		{"comma join", "SELECT * FROM acct a, acct2 b", "SELECT * FROM acct " + c + " a, acct2 " + c + " b"},
		{"subqueries", "SELECT * FROM (SELECT id FROM acct) x WHERE x.id IN (SELECT id FROM acct2)",
			"SELECT * FROM (SELECT id FROM acct " + c + ") x WHERE x.id IN (SELECT id FROM acct2 " + c + ")"},
		{"union", "SELECT id FROM acct UNION SELECT id FROM acct2", "SELECT id FROM acct " + c + " UNION SELECT id FROM acct2 " + c},
		{"recursive cte", "WITH RECURSIVE r AS (SELECT id FROM acct UNION ALL SELECT id + 1 FROM r WHERE id < 3) SELECT * FROM r",
			"WITH RECURSIVE r AS (SELECT id FROM acct " + c + " UNION ALL SELECT id + 1 FROM r WHERE id < 3) SELECT * FROM r"},
		{"strings and comments", `SELECT 'FROM plain', "FROM plain" /* FROM plain */ FROM acct # FROM plain`,
			`SELECT 'FROM plain', "FROM plain" /* FROM plain */ FROM acct ` + c + ` # FROM plain`},
		{"limit with a comma", "SELECT * FROM acct ORDER BY id LIMIT 10, 20", "SELECT * FROM acct " + c + " ORDER BY id LIMIT 10, 20"},
		{"limit without order", "SELECT * FROM acct LIMIT 10, 20", "SELECT * FROM acct " + c + " LIMIT 10, 20"},
		{"index hint", "SELECT * FROM acct a USE INDEX (PRIMARY)", "SELECT * FROM acct " + c + " a USE INDEX (PRIMARY)"},
		{"index hint for join", "SELECT * FROM acct a USE INDEX FOR JOIN (PRIMARY) JOIN acct2 b ON a.id = b.id",
			"SELECT * FROM acct " + c + " a USE INDEX FOR JOIN (PRIMARY) JOIN acct2 " + c + " b ON a.id = b.id"},
		{"index hint for order by", "SELECT * FROM acct a FORCE KEY FOR ORDER BY (PRIMARY), acct2 b",
			"SELECT * FROM acct " + c + " a FORCE KEY FOR ORDER BY (PRIMARY), acct2 " + c + " b"},
		{"own name in a non-recursive body is the table", "WITH acct AS (SELECT * FROM acct WHERE id > 0), b AS (SELECT * FROM acct) SELECT * FROM acct, b",
			"WITH acct AS (SELECT * FROM acct " + c + " WHERE id > 0), b AS (SELECT * FROM acct) SELECT * FROM acct, b"},
		{"dual and substring", "SELECT SUBSTRING('abc' FROM 2) FROM DUAL", "SELECT SUBSTRING('abc' FROM 2) FROM DUAL"},
		{"json_table", "SELECT * FROM acct, JSON_TABLE('[1]', '$[*]' COLUMNS (x INT PATH '$')) j",
			"SELECT * FROM acct " + c + ", JSON_TABLE('[1]', '$[*]' COLUMNS (x INT PATH '$')) j"},
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
		{"existing FOR SYSTEM_TIME", "SELECT * FROM acct FOR SYSTEM_TIME ALL", "already reads history"},
		{"unknown table", "SELECT * FROM nope", "nope is not a table or view this user can see"},
		{"non-versioned table", "SELECT * FROM acct, plain", "plain is not a system-versioned table"},
		{"view", "SELECT * FROM app.v", "app.v is a view"},
		{"partition", "SELECT * FROM acct PARTITION (p0)", "PARTITION"},
		{"executable comment", "SELECT * FROM acct /*!50000 , plain */", "/*! ... */"},
		{"mariadb comment", "SELECT * FROM acct /*M! , plain */", "/*! ... */"},
		{"double-quoted name", `SELECT * FROM "acct"`, "Cannot find the table"},
		{"table statement", "TABLE acct", "Only a SELECT"},
		{"before 1970", "SELECT * FROM acct", "before 1970"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := asOfAt
			if tc.name == "before 1970" {
				at = time.Unix(0, 0)
			}
			_, err := AsOf(tc.in, at, fakeCatalog)
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
		"DELETE FROM acct", "UPDATE acct SET bal = 0", "INSERT INTO acct SELECT * FROM acct2", "SELECT * FROM acct FOR UPDATE",
		"SELECT * FROM acct LOCK IN SHARE MODE", "SELECT * INTO OUTFILE '/tmp/x' FROM acct", "SELECT @a := 1 FROM acct",
		"SELECT SLEEP(1) FROM acct", "SELECT * FROM acct; DELETE FROM acct",
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

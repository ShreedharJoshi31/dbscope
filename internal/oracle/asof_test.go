package oracle

import (
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

var asOfAt = time.Date(2026, 11, 14, 19, 29, 3, 123456789, time.UTC)

// fakeCatalog resolves names the way resolve does, from a fixed list: TT.ACCT and TT.ACCT2
// can be read with flashback, TT.LOCKED cannot, TT.SYN is a synonym.
func fakeCatalog(refs []timetravel.Ref) ([]timetravel.Table, error) {
	yes := &schema.TimeTravel{Kind: schema.TimeTravelFlashback, Available: true}
	rels := map[string]*schema.Relation{
		"TT.ACCT":   {Schema: "TT", Name: "ACCT", Kind: schema.KindTable, TimeTravel: yes},
		"TT.ACCT2":  {Schema: "TT", Name: "ACCT2", Kind: schema.KindTable, TimeTravel: yes},
		"TT.V_ACCT": {Schema: "TT", Name: "V_ACCT", Kind: schema.KindView, TimeTravel: yes},
		"TT.LOCKED": {Schema: "TT", Name: "LOCKED", Kind: schema.KindTable, TimeTravel: &schema.TimeTravel{Kind: schema.TimeTravelFlashback}},
	}
	out := make([]timetravel.Table, len(refs))
	for i, r := range refs {
		out[i].Ref = r
		parts := []string{"TT"}
		for _, p := range r.Parts {
			n := p.Name
			if !p.Quoted {
				n = strings.ToUpper(n)
			}
			parts = append(parts, n)
		}
		key := strings.Join(parts[len(parts)-2:], ".")
		switch {
		case key == "TT.SYN":
			out[i].Why = "is a synonym; name the table it points to"
		default:
			out[i].Rel = rels[key]
		}
	}
	return out, nil
}

func TestAsOfRewrite(t *testing.T) {
	c := AsOfClause(asOfAt)
	if c != "AS OF TIMESTAMP CAST(FROM_TZ(TIMESTAMP '2026-11-14 19:29:03.123456', 'UTC') AT TIME ZONE TO_CHAR(SYSTIMESTAMP, 'TZH:TZM') AS TIMESTAMP)" {
		t.Fatalf("clause %s", c)
	}
	for _, tc := range []struct{ name, in, want string }{
		{"alias", "SELECT a.bal FROM tt.acct a WHERE a.id = 1", "SELECT a.bal FROM tt.acct " + c + " a WHERE a.id = 1"},
		{"unqualified, no alias", "select * from acct", "select * from acct " + c},
		{"quoted and qualified", `SELECT * FROM "TT"."ACCT" x`, `SELECT * FROM "TT"."ACCT" ` + c + ` x`},
		{"mixed quoting", `SELECT * FROM tt."ACCT"`, `SELECT * FROM tt."ACCT" ` + c},
		{"view", "SELECT * FROM tt.v_acct", "SELECT * FROM tt.v_acct " + c},
		{"joins", "SELECT * FROM tt.acct a INNER JOIN tt.acct2 b ON a.id = b.id LEFT OUTER JOIN acct c ON c.id = a.id CROSS JOIN tt.acct2",
			"SELECT * FROM tt.acct " + c + " a INNER JOIN tt.acct2 " + c + " b ON a.id = b.id LEFT OUTER JOIN acct " + c +
				" c ON c.id = a.id CROSS JOIN tt.acct2 " + c},
		{"comma join", "SELECT * FROM tt.acct a, tt.acct2 b,acct", "SELECT * FROM tt.acct " + c + " a, tt.acct2 " + c + " b,acct " + c},
		{"comma after a join condition", "SELECT * FROM tt.acct a JOIN acct2 b ON a.id = b.id, tt.acct c",
			"SELECT * FROM tt.acct " + c + " a JOIN acct2 " + c + " b ON a.id = b.id, tt.acct " + c + " c"},
		{"derived table", "SELECT * FROM (SELECT id FROM tt.acct) x, tt.acct2 y", "SELECT * FROM (SELECT id FROM tt.acct " + c + ") x, tt.acct2 " + c + " y"},
		{"subquery in WHERE", "SELECT * FROM tt.acct WHERE id IN (SELECT id FROM tt.acct2 WHERE tag IN ('a', 'b'))",
			"SELECT * FROM tt.acct " + c + " WHERE id IN (SELECT id FROM tt.acct2 " + c + " WHERE tag IN ('a', 'b'))"},
		{"union", "SELECT id FROM tt.acct UNION ALL SELECT id FROM tt.acct2 ORDER BY 1",
			"SELECT id FROM tt.acct " + c + " UNION ALL SELECT id FROM tt.acct2 " + c + " ORDER BY 1"},
		{"select list after union", "SELECT id FROM tt.acct UNION SELECT id, 2 FROM tt.acct2",
			"SELECT id FROM tt.acct " + c + " UNION SELECT id, 2 FROM tt.acct2 " + c},
		{"cte names kept, bodies rewritten", "WITH c1 AS (SELECT id FROM tt.acct), c2 (i) AS (SELECT id FROM c1) SELECT * FROM c2 JOIN tt.acct2 b ON b.id = c2.i, c1",
			"WITH c1 AS (SELECT id FROM tt.acct " + c + "), c2 (i) AS (SELECT id FROM c1) SELECT * FROM c2 JOIN tt.acct2 " + c + " b ON b.id = c2.i, c1"},
		{"strings and comments", "SELECT 'x FROM tt.nope' s, q'[FROM tt.nope]' /* FROM tt.nope */ FROM tt.acct -- FROM tt.nope\n",
			"SELECT 'x FROM tt.nope' s, q'[FROM tt.nope]' /* FROM tt.nope */ FROM tt.acct " + c + " -- FROM tt.nope\n"},
		{"dual", "SELECT 1 FROM dual", "SELECT 1 FROM dual"},
		{"from inside a function call", "SELECT EXTRACT(YEAR FROM SYSDATE), TRIM(LEADING 'x' FROM 'xa') FROM tt.acct",
			"SELECT EXTRACT(YEAR FROM SYSDATE), TRIM(LEADING 'x' FROM 'xa') FROM tt.acct " + c},
		{"table function", "SELECT * FROM TABLE(sys.odcinumberlist(1, 2)) t, tt.acct", "SELECT * FROM TABLE(sys.odcinumberlist(1, 2)) t, tt.acct " + c},
		{"lateral", "SELECT * FROM tt.acct a, LATERAL (SELECT tag FROM tt.acct2 b WHERE b.id = a.id)",
			"SELECT * FROM tt.acct " + c + " a, LATERAL (SELECT tag FROM tt.acct2 " + c + " b WHERE b.id = a.id)"},
		{"semicolon", "SELECT * FROM tt.acct;", "SELECT * FROM tt.acct " + c + ";"},
		{"cte names fold like Oracle names", `WITH "acct" AS (SELECT 1 x FROM dual), ACCT2 AS (SELECT 1 x FROM dual) SELECT * FROM acct, "ACCT2", "acct"`,
			`WITH "acct" AS (SELECT 1 x FROM dual), ACCT2 AS (SELECT 1 x FROM dual) SELECT * FROM acct ` + c + `, "ACCT2", "acct"`},
		{"self join", "SELECT * FROM tt.acct o JOIN tt.acct n ON n.id = o.id", "SELECT * FROM tt.acct " + c + " o JOIN tt.acct " + c + " n ON n.id = o.id"},
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
		{"existing AS OF", "SELECT * FROM tt.acct AS OF SCN 1", "already reads history (AS OF)"},
		{"existing VERSIONS", "SELECT * FROM tt.acct VERSIONS BETWEEN SCN 1 AND MAXVALUE", "already reads history (VERSIONS BETWEEN)"},
		{"unknown table", "SELECT * FROM tt.nope", "tt.nope is not a table or view this user can see"},
		{"unknown and synonym named", "SELECT * FROM tt.nope, syn", "tt.nope is not a table or view this user can see; syn is a synonym"},
		{"no FLASHBACK", "SELECT * FROM tt.acct JOIN tt.locked l ON 1 = 1", "tt.locked needs the FLASHBACK privilege on it, or FLASHBACK ANY TABLE"},
		{"parenthesized join", "SELECT * FROM (tt.acct a JOIN tt.acct2 b ON a.id = b.id)", "parenthesized join"},
		{"database link", "SELECT * FROM tt.acct@remote", "with @ after its name"},
		{"partition", "SELECT * FROM tt.acct PARTITION (p1)", "with PARTITION after its name"},
		{"sample", "SELECT * FROM tt.acct SAMPLE (10)", "with SAMPLE after its name"},
		{"only", "SELECT * FROM ONLY (tt.acct)", "ONLY reads a table"},
		{"not a select", "DELETE FROM tt.acct", "Only a SELECT"},
		{"unbalanced", "SELECT * FROM (SELECT 1 FROM tt.acct", "do not balance"},
		{"three parts", "SELECT * FROM a.b.c", "a.b.c"},
		{"nq string", "SELECT nq'[a' x ]' , e.* FROM tt.acct e", "NQ'...'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AsOf(tc.in, asOfAt, func(refs []timetravel.Ref) ([]timetravel.Table, error) {
				tables, err := fakeCatalog(refs)
				for i, r := range refs {
					if len(r.Parts) > 2 {
						tables[i].Rel, tables[i].Why = nil, "has more than two name parts"
					}
				}
				return tables, err
			})
			var te *timetravel.Error
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: err %v, want it to contain %q", tc.in, err, tc.want)
			}
			if !asError(err, &te) {
				t.Fatalf("%T is not a *timetravel.Error", err)
			}
		})
	}
}

// TestAsOfKeepsGuard: a statement Check refuses is refused by the rewriter or,
// once rewritten, by Check with the same error.
func TestAsOfKeepsGuard(t *testing.T) {
	for _, q := range []string{
		"DELETE FROM tt.acct", "UPDATE tt.acct SET bal = 0", "INSERT INTO tt.acct SELECT * FROM tt.acct2",
		"SELECT * FROM tt.acct FOR UPDATE", "SELECT bal INTO :x FROM tt.acct", "SELECT * FROM tt.acct; DROP TABLE tt.acct",
		"SELECT dbms_flashback.get_system_change_number FROM tt.acct",
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

func asError(err error, target **timetravel.Error) bool {
	te, ok := err.(*timetravel.Error)
	*target = te
	return ok
}

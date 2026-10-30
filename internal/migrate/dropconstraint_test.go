package migrate

import (
	"strings"
	"testing"
)

func TestDropConstraintFinding(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"ALTER TABLE app.teams DROP CONSTRAINT teams_owner_id_fkey", "(owner_id) is no longer checked against app.users"},
		{"ALTER TABLE app.teams DROP CONSTRAINT teams_slug_check", "rows breaking"},
		{"ALTER TABLE app.teams DROP CONSTRAINT teams_slug_key", "duplicate (slug) values can be written"},
		{"ALTER TABLE app.teams DROP CONSTRAINT IF EXISTS no_such_constraint", ""},
	} {
		s := analyzeOne(t, golden{sql: c.sql})
		msg := ""
		for _, f := range s.Findings {
			if f.RuleID == "DBS-DROP-CONSTRAINT" {
				msg = f.Message
				if f.Rating != "medium" {
					t.Errorf("%s: rated %s", c.sql, f.Rating)
				}
			}
		}
		if (c.want == "") != (msg == "") || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %q, want %q", c.sql, msg, c.want)
		}
	}
}

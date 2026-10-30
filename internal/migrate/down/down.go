// Package down checks each up migration's rollback: that one exists when
// the tool supports rollbacks, that applying it after the up returns the
// schema to where it was, and whether the up drops data no rollback brings
// back. The engines compute the schema changes; this package rates them.
package down

import (
	"fmt"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// Rule ids.
const (
	Missing    = "DBS-DOWN-MISSING"
	Incomplete = "DBS-DOWN-INCOMPLETE"
	LosesData  = "DBS-DOWN-LOSES-DATA"
)

// Finding is one rollback problem, reported on the up file's first statement.
type Finding struct {
	RuleID  string
	Rating  score.Rating
	Message string
	Safe    string
}

// Result is what an engine learned by replaying an up file and its down.
type Result struct {
	// Drops is the data the up removes, such as "table app.users" or "column app.users.email".
	Drops []string
	// Left is what still differs from the schema before the up once the
	// down has run; empty when the down restores it.
	Left []string
	// Failed describes a down statement the engine rejected, which stops a
	// real rollback part way ("line 3: no such column: x").
	Failed string
	// Unchecked says why the round trip could not be checked; Left and
	// Failed are then not meaningful.
	Unchecked string
}

// Findings rates the rollback of one up file.
func Findings(d *ingest.Down, r Result) []Finding {
	if d == nil {
		return nil
	}
	if d.Missing {
		return []Finding{{RuleID: Missing, Rating: score.Medium,
			Message: fmt.Sprintf("this %s migration has no down migration, so it cannot be rolled back with the tool; a rollback under pressure means writing one by hand", d.Tool),
			Safe:    missingSafe[d.Tool]}}
	}
	var out []Finding
	switch {
	case r.Unchecked != "":
	case r.Failed != "":
		out = append(out, Finding{RuleID: Incomplete, Rating: score.High,
			Message: fmt.Sprintf("the down migration in %s fails after this migration (%s), so a rollback stops part way", d.Name, r.Failed),
			Safe:    "Fix the down migration and test it: apply the up, then the down, on a copy of the database"})
	case len(r.Left) > 0:
		out = append(out, Finding{RuleID: Incomplete, Rating: score.High,
			Message: fmt.Sprintf("applying the down migration in %s after this migration does not return the schema to where it was: %s", d.Name, list(r.Left)),
			Safe:    "Make the down undo every change of the up, in reverse order, and test it: apply the up, then the down, on a copy of the database"})
	}
	if len(r.Drops) > 0 {
		out = append(out, Finding{RuleID: LosesData, Rating: score.Medium,
			Message: fmt.Sprintf("this migration drops %s; the down can recreate the structure but not the data, so a rollback leaves it empty", list(r.Drops)),
			Safe:    "Back the data up before running it, or keep it for the rollback window: rename instead of drop now, and drop in a later migration"})
	}
	return out
}

var missingSafe = map[string]string{
	ingest.ToolGolangMigrate: "Add a matching .down.sql file that undoes the up",
	ingest.ToolFlyway:        "Add a U<version>__ undo migration that undoes it",
	ingest.ToolGoose:         "Add a -- +goose Down section that undoes the up",
	ingest.ToolDbmate:        "Add a -- migrate:down section that undoes the up",
	ingest.ToolMigrateMongo:  "Add a down(db, client) function that undoes the up",
}

// list joins items, naming at most five.
func list(items []string) string {
	const show = 5
	if len(items) <= show {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:show], ", "), len(items)-show)
}

// Describe lists what a diff from the schema before the up still changes.
func Describe(d apply.Diff) []string {
	var out []string
	for _, t := range d.Added {
		out = append(out, t+" is still there")
	}
	for _, t := range d.Dropped {
		out = append(out, t+" is not recreated")
	}
	cols := map[string][]string{}
	for _, c := range d.Columns {
		what := "column " + c.Column + " " + c.Change
		switch c.Change {
		case "added":
			what = "column " + c.Column + " is still there"
		case "dropped":
			what = "column " + c.Column + " is not recreated"
		case "renamed":
			what = "column " + c.Column + " is still named " + c.To
		case "altered":
			what = "column " + c.Column + " is defined differently"
		}
		cols[c.Relation] = append(cols[c.Relation], what)
	}
	for _, t := range d.Altered {
		if cs := cols[t]; len(cs) > 0 {
			out = append(out, t+" "+strings.Join(cs, ", "))
		} else {
			out = append(out, t+" has different indexes, constraints or properties")
		}
	}
	return out
}

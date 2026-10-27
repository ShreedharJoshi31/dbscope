package summarize

import (
	"fmt"
	"strings"
)

// maxDescribed caps how many columns Describe mentions.
const maxDescribed = 4

// Describe states what a profile shows in plain sentences: the row count,
// ranges of numeric and date/time columns, distinct counts of other
// columns and how often they are empty. It only repeats facts from the
// profile, so unlike a model it cannot invent row values.
func Describe(p Profile) string {
	var b strings.Builder
	switch {
	case p.RowCount == 0:
		return "The query returned no rows."
	case p.RowCount == 1:
		b.WriteString("1 row")
	default:
		fmt.Fprintf(&b, "%d rows", p.RowCount)
	}
	if p.Truncated {
		b.WriteString(" (more exist than were fetched)")
	}
	b.WriteString(".")
	for i, c := range p.Columns {
		if i == maxDescribed {
			fmt.Fprintf(&b, " %d more columns.", len(p.Columns)-maxDescribed)
			break
		}
		switch {
		case c.Min != nil && c.Max != nil && fmt.Sprint(c.Min) == fmt.Sprint(c.Max):
			fmt.Fprintf(&b, " %s is %v", c.Name, c.Min)
		case c.Min != nil && c.Max != nil:
			fmt.Fprintf(&b, " %s ranges from %v to %v", c.Name, c.Min, c.Max)
		case p.RowCount > 1:
			fmt.Fprintf(&b, " %s has %d distinct values", c.Name, c.Distinct)
		default:
			continue
		}
		if c.NullPct > 0 {
			fmt.Fprintf(&b, ", empty in %g%% of rows", c.NullPct)
		}
		b.WriteString(".")
	}
	return b.String()
}

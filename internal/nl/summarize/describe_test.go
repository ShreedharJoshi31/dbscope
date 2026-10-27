package summarize

import "testing"

func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		p    Profile
		want string
	}{
		{Profile{}, "The query returned no rows."},
		{Profile{RowCount: 5, Columns: []ColumnProfile{
			{Name: "customer_id", Distinct: 5, Min: 380.0, Max: 553.0},
			{Name: "first_name", Distinct: 5},
			{Name: "rental_count", Distinct: 5, Min: 90.0, Max: 97.0},
		}}, "5 rows. customer_id ranges from 380 to 553. first_name has 5 distinct values. rental_count ranges from 90 to 97."},
		{Profile{RowCount: 1, Columns: []ColumnProfile{{Name: "count", Min: 42.0, Max: 42.0}, {Name: "name", Distinct: 1}}},
			"1 row. count is 42."},
		{Profile{RowCount: 500, Truncated: true, Columns: []ColumnProfile{{Name: "email", Distinct: 480, NullPct: 4}}},
			"500 rows (more exist than were fetched). email has 480 distinct values, empty in 4% of rows."},
		{Profile{RowCount: 2, Columns: []ColumnProfile{{Name: "a", Distinct: 2}, {Name: "b", Distinct: 2}, {Name: "c", Distinct: 2},
			{Name: "d", Distinct: 2}, {Name: "e", Distinct: 2}, {Name: "f", Distinct: 2}}},
			"2 rows. a has 2 distinct values. b has 2 distinct values. c has 2 distinct values. d has 2 distinct values. 2 more columns."},
	} {
		if got := Describe(c.p); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

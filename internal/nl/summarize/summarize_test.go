package summarize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/query/exec"
)

func raw(vals ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(vals))
	for i, v := range vals {
		out[i] = json.RawMessage(v)
	}
	return out
}

func TestCompute(t *testing.T) {
	res := &exec.Result{
		Columns: []exec.Column{
			{Name: "id", Type: "integer", TypeOID: 23},
			{Name: "total", Type: "numeric(10,2)", TypeOID: 1700},
			{Name: "email", Type: "text", TypeOID: 25},
			{Name: "placed_at", Type: "timestamp with time zone", TypeOID: 1184},
			{Name: "ok", Type: "boolean", TypeOID: 16},
			{Name: "day", Type: "date", TypeOID: 1082},
		},
		Rows: [][]json.RawMessage{
			raw(`1`, `"10.50"`, `"alice@example.com"`, `"2026-10-01 10:00:00+00"`, `true`, `"2026-01-01"`),
			raw(`2`, `"NaN"`, `"bob@x.io"`, `"2026-10-03 10:00:00+00"`, `false`, `"infinity"`),
			raw(`3`, `null`, `null`, `null`, `true`, `"2026-01-03"`),
			raw(`3`, `"4.50"`, `"bob@x.io"`, `"2026-10-02 10:00:00.5+00"`, `null`, `null`),
		},
		RowCount: 4, Truncated: true,
	}
	p := Compute(res)
	if p.RowCount != 4 || !p.Truncated || len(p.Columns) != 6 {
		t.Fatalf("%+v", p)
	}
	id, total, email, at, ok, day := p.Columns[0], p.Columns[1], p.Columns[2], p.Columns[3], p.Columns[4], p.Columns[5]
	if id.Distinct != 3 || id.Min != 1.0 || id.Max != 3.0 || id.Avg != 2.25 || id.NullPct != 0 {
		t.Errorf("id %+v", id)
	}
	if total.NullPct != 25 || total.Min != 4.5 || total.Max != 10.5 || total.Avg != 7.5 || total.Distinct != 3 {
		t.Errorf("total %+v", total)
	}
	if email.Min != nil || email.Max != nil || *email.MinLen != 8 || *email.MaxLen != 17 || email.Distinct != 2 {
		t.Errorf("email %+v", email)
	}
	if at.Min != "2026-10-01 10:00:00+00" || at.Max != "2026-10-03 10:00:00+00" || at.Avg == nil {
		t.Errorf("placed_at %+v", at)
	}
	if ok.Min != nil || ok.MinLen != nil || ok.Distinct != 2 || ok.NullPct != 25 {
		t.Errorf("ok %+v", ok)
	}
	if day.Min != "2026-01-01" || day.Max != "2026-01-03" || day.Avg != "2026-01-02T00:00:00Z" {
		t.Errorf("day %+v", day)
	}
	// Text values never appear in the profile.
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "alice") || strings.Contains(string(b), "bob@") {
		t.Fatalf("text values leaked: %s", b)
	}
}

func TestFirstRows(t *testing.T) {
	res := &exec.Result{Columns: []exec.Column{{Name: "a"}}, Rows: [][]json.RawMessage{raw(`1`), raw(`2`), raw(`3`)}, RowCount: 3}
	r := FirstRows(res, 2)
	if len(r.Rows) != 2 || !r.Truncated || r.RowCount != 3 || r.Columns[0] != "a" {
		t.Fatalf("%+v", r)
	}
	if r := FirstRows(res, 5); len(r.Rows) != 3 || r.Truncated {
		t.Fatalf("%+v", r)
	}
	if p := Compute(&exec.Result{Columns: []exec.Column{{Name: "x", TypeOID: 23}}}); p.Columns[0].NullPct != 0 || p.Columns[0].Min != nil {
		t.Fatalf("empty %+v", p)
	}
}

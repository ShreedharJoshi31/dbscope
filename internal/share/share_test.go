package share

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/store"
)

func sample() store.Shared {
	return store.Shared{
		Layouts: []store.Layout{{
			Key:       "billing,public",
			Positions: map[string]store.Position{"public.users": {X: 0.4, Y: -0.6}, "billing.invoices": {X: 420.5, Y: 80}},
			Collapsed: []string{"public.audit", "billing.invoices", "public.audit"},
		}, {Key: "public"}},
		Decisions: map[string]string{"public.posts(user_id)->public.users(id)": "accepted"},
		Glossary: []store.GlossaryEntry{
			{Term: "mrr", Definition: "Monthly recurring revenue.\nExcludes refunds.", SQL: "sum(amount) filter (where plan <> 'free')"},
			{Term: "active user", Definition: "Logged in during the last 30 days."},
		},
	}
}

const golden = `{
  "kind": "dbscope-shared-state",
  "formatVersion": 1,
  "foreignKeys": {
    "public.posts(user_id)->public.users(id)": "accepted"
  },
  "glossary": {
    "mrr": {
      "definition": [
        "Monthly recurring revenue.",
        "Excludes refunds."
      ],
      "sql": "sum(amount) filter (where plan <> 'free')"
    },
    "active user": {
      "definition": "Logged in during the last 30 days."
    }
  },
  "layouts": {
    "billing,public": {
      "collapsed": [
        "billing.invoices",
        "public.audit"
      ],
      "positions": {
        "billing.invoices": [421, 80],
        "public.users": [0, -1]
      }
    },
    "public": {
      "collapsed": [],
      "positions": {}
    }
  }
}
`

func TestMarshalIsStableAndRoundTrips(t *testing.T) {
	got := string(FromStore(sample()).Marshal())
	if got != golden {
		t.Fatalf("Marshal:\n%s\nwant:\n%s", got, golden)
	}
	f, err := Parse([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	if again := string(f.Marshal()); again != golden {
		t.Fatalf("round trip changed the file:\n%s", again)
	}
	if g := f.Glossary["mrr"]; g.Definition != "Monthly recurring revenue.\nExcludes refunds." || !strings.Contains(g.SQL, "<>") {
		t.Fatalf("glossary entry %+v", g)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		`{"kind": "something-else", "formatVersion": 1}`:                                      "not a dbscope shared-state file",
		`{"kind": "dbscope-shared-state"}`:                                                    "formatVersion is missing",
		`{"kind": "dbscope-shared-state", "formatVersion": 2}`:                                "needs a newer dbscope",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "foreignKeys": {"a": "maybe"}}`: "want accepted or rejected",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "glossary": {" ": {}}}`:         "1 to 100 characters",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "glossary": {"t": {"sql": 5}}}`: "string or an array of lines",
		`not json`: "not a dbscope shared-state file",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "glossary": {"MRR": {}, "mrr ": {}}}`:         "are the same term",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "glossary": {"a": {}, "a": {}}}`:              "appears twice",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "foreignKeys": {"foo": "accepted"}}`:          "must look like",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "layouts": {"p": {"positions": {"t": [5]}}}}`: "must be [x, y]",
		`{"kind": "dbscope-shared-state", "formatVersion": 1, "layouts": {"": {}}}`:                         "layout key",
	}
	for in, want := range cases {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%s) = %v, want %q", in, err, want)
		}
	}
	// $schema and unknown fields are fine.
	if _, err := Parse([]byte(`{"$schema": "./dbscope.schema.json", "kind": "dbscope-shared-state", "formatVersion": 1, "future": true}`)); err != nil {
		t.Fatal(err)
	}
}

func TestPlanMergeAndReplace(t *testing.T) {
	local := FromStore(store.Shared{
		Layouts: []store.Layout{{Key: "public", Positions: map[string]store.Position{
			"public.users": {X: 0, Y: 0}, "public.orders": {X: 10, Y: 10}, "public.mine": {X: 5, Y: 5},
		}, Collapsed: []string{"public.mine"}}},
		Decisions: map[string]string{"public.a(b_id)->public.b(id)": "accepted", "public.c(d_id)->public.d(id)": "rejected"},
		Glossary:  []store.GlossaryEntry{{Term: "zeta", Definition: "z"}, {Term: "Active User", Definition: "old"}},
	})
	file := FromStore(store.Shared{
		Layouts: []store.Layout{{Key: "public", Positions: map[string]store.Position{
			"public.users": {X: 0, Y: 0}, "public.orders": {X: 99, Y: 10}, "public.gone": {X: 1, Y: 1},
		}}},
		Decisions: map[string]string{"public.a(b_id)->public.b(id)": "rejected", "public.e(f_id)->public.f(id)": "accepted"},
		Glossary:  []store.GlossaryEntry{{Term: "active user", Definition: "new"}, {Term: "beta", Definition: "b"}},
	})
	ex := &Existing{
		Schemas:     map[string]bool{"public": true},
		Tables:      map[string]bool{"public.users": true, "public.orders": true, "public.mine": true},
		InferredFKs: map[string]bool{"public.a(b_id)->public.b(id)": true, "public.c(d_id)->public.d(id)": true},
	}

	p, out := Plan(local, file, Merge, ex)
	if !p.SchemaChecked || p.ForeignKeys.Counts[Changed] != 1 || p.ForeignKeys.Counts[Added] != 1 || p.ForeignKeys.Counts[Kept] != 1 || p.ForeignKeys.Orphans != 1 {
		t.Fatalf("foreign keys %+v", p.ForeignKeys)
	}
	if !reflect.DeepEqual(out.Decisions, map[string]string{"public.a(b_id)->public.b(id)": "rejected", "public.c(d_id)->public.d(id)": "rejected", "public.e(f_id)->public.f(id)": "accepted"}) {
		t.Fatalf("merged decisions %v", out.Decisions)
	}
	if c := p.Tables.Counts; c[Changed] != 1 || c[Unchanged] != 1 || c[Added] != 1 || c[Kept] != 1 || p.Tables.Orphans != 1 {
		t.Fatalf("tables %+v", p.Tables)
	}
	l := out.Layouts[0]
	if len(l.Positions) != 4 || l.Positions["public.orders"] != (store.Position{X: 99, Y: 10}) || !slices.Equal(l.Collapsed, []string{"public.mine"}) {
		t.Fatalf("merged layout %+v", l)
	}
	// Local order kept, the changed term updated in place (case-insensitively), new terms appended.
	if got := out.Glossary; len(got) != 3 || got[0].Term != "zeta" || got[1] != (store.GlossaryEntry{Term: "active user", Definition: "new"}) || got[2].Term != "beta" {
		t.Fatalf("merged glossary %+v", got)
	}

	p, out = Plan(local, file, Replace, nil)
	if p.SchemaChecked || p.ForeignKeys.Counts[Removed] != 1 || p.Tables.Counts[Removed] != 1 || p.Glossary.Counts[Removed] != 1 || p.Tables.Orphans != 0 {
		t.Fatalf("replace preview %+v", p)
	}
	if !reflect.DeepEqual(out.Decisions, file.ForeignKeys) || len(out.Layouts[0].Positions) != 3 || len(out.Glossary) != 2 || out.Glossary[0].Term != "active user" {
		t.Fatalf("replace result %+v", out)
	}
	if !reflect.DeepEqual(FromStore(out).Marshal(), file.Marshal()) {
		t.Fatal("after replace the connection's state should export as the file")
	}
}

func TestPlanEdgeCases(t *testing.T) {
	// The file only collapses a table this connection has placed: a merge keeps the position.
	local := FromStore(store.Shared{Layouts: []store.Layout{{Key: "public", Positions: map[string]store.Position{"public.audit": {X: 10, Y: 20}}}}})
	file := File{Layouts: map[string]Layout{"public": {Positions: map[string][2]int{}, Collapsed: []string{"public.audit"}}}}
	p, out := Plan(local, file, Merge, nil)
	if got := out.Layouts[0]; got.Positions["public.audit"] != (store.Position{X: 10, Y: 20}) || !slices.Equal(got.Collapsed, []string{"public.audit"}) {
		t.Fatalf("merged %+v", got)
	}
	if it := p.Tables.Items[0]; it.Change != Changed || it.Detail != "at 10, 20 → at 10, 20, collapsed" {
		t.Fatalf("item %+v", it)
	}

	// Tables and keys in schemas that were not read are not called orphans.
	ex := &Existing{Schemas: map[string]bool{"public": true}, Tables: map[string]bool{}, InferredFKs: map[string]bool{}}
	file = File{
		ForeignKeys: map[string]string{"sales.o(c_id)->public.c(id)": "accepted", "public.o(c_id)->public.c(id)": "accepted"},
		Layouts:     map[string]Layout{"public,sales": {Positions: map[string][2]int{"sales.orders": {0, 0}, "public.gone": {0, 0}}}},
	}
	p, _ = Plan(File{}, file, Merge, ex)
	if p.Tables.Orphans != 1 || p.ForeignKeys.Orphans != 1 {
		t.Fatalf("orphans: tables %d, keys %d", p.Tables.Orphans, p.ForeignKeys.Orphans)
	}

	// An unknown mode replaces; a case-only rename says so.
	local = FromStore(store.Shared{Glossary: []store.GlossaryEntry{{Term: "MRR", Definition: "x"}, {Term: "other"}}})
	file = FromStore(store.Shared{Glossary: []store.GlossaryEntry{{Term: "mrr", Definition: "x"}}})
	p, out = Plan(local, file, Mode("bogus"), nil)
	if p.Mode != Replace || len(out.Glossary) != 1 || p.Glossary.Counts[Removed] != 1 {
		t.Fatalf("bogus mode: %+v %+v", p.Mode, out.Glossary)
	}
	if it := p.Glossary.Items[0]; it.Change != Changed || it.Detail != "spelled MRR here" {
		t.Fatalf("case change %+v", it)
	}

	// Too many terms after a merge.
	var many []store.GlossaryEntry
	for i := range store.MaxGlossary {
		many = append(many, store.GlossaryEntry{Term: fmt.Sprintf("t%d", i)})
	}
	_, out = Plan(FromStore(store.Shared{Glossary: many}), FromStore(store.Shared{Glossary: []store.GlossaryEntry{{Term: "one more"}}}), Merge, nil)
	if err := Check(out); err == nil || !strings.Contains(err.Error(), "501 glossary terms") {
		t.Fatalf("Check: %v", err)
	}
}

func TestGlossaryOrderSurvivesReplace(t *testing.T) {
	order := []store.GlossaryEntry{{Term: "zeta"}, {Term: "Alpha"}, {Term: "mid"}}
	f, err := Parse(FromStore(store.Shared{Glossary: order}).Marshal())
	if err != nil {
		t.Fatal(err)
	}
	_, out := Plan(File{}, f, Replace, nil)
	if !slices.Equal(out.Glossary, order) {
		t.Fatalf("order %v", out.Glossary)
	}
	// Terms stored with stray spaces export trimmed, so the file always parses.
	if _, err := Parse(FromStore(store.Shared{Glossary: []store.GlossaryEntry{{Term: "padded "}}}).Marshal()); err != nil {
		t.Fatal(err)
	}
}

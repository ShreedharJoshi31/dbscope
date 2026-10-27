package workload

import (
	"slices"
	"testing"

	"github.com/dbscope/dbscope/internal/schema"
)

func rel(sc, name string, cols ...string) schema.Relation {
	r := schema.Relation{Schema: sc, Name: name, Kind: schema.KindTable}
	for _, c := range cols {
		r.Columns = append(r.Columns, schema.Column{Name: c})
	}
	return r
}

func TestAttribute(t *testing.T) {
	m := &schema.Model{Schemas: []string{"app", "public"}, Relations: []schema.Relation{
		rel("app", "orders", "id", "customer_id", "total", "created_at"),
		rel("public", "customers", "id", "email", "created_at"),
		rel("public", "orders", "id", "legacy"),
	}}
	s := &Snapshot{Tables: map[string]*Table{"app.orders": {SeqScans: 3}}}
	Attribute(s, m, Path{Schemas: []string{"app", "public"}}, []Statement{
		// Unqualified orders resolves through the path to app.orders, not public.orders;
		// created_at is in both tables, so the qualified use goes to customers only.
		{QueryID: "1", Text: "SELECT o.total, c.email, c.created_at FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.id = $1", Calls: 100, TotalMs: 50},
		{QueryID: "2", Text: "SELECT * FROM public.orders WHERE legacy = $1", Calls: 10, TotalMs: 900},
		// o.* stars orders only.
		{QueryID: "3", Text: "SELECT o.* FROM orders o JOIN customers c ON c.id = o.customer_id", Calls: 4, TotalMs: 1},
		// A CTE named like a table is not the table, except inside its own body.
		{QueryID: "4", Text: "WITH customers AS (SELECT 1 AS id) SELECT id FROM customers", Calls: 7, TotalMs: 1},
		{QueryID: "5", Text: "WITH customers AS (SELECT * FROM customers WHERE id > $1) SELECT count(*) FROM customers", Calls: 6, TotalMs: 1},
		// Unattributed: no such table, not SQL, utility statements, no text left.
		{QueryID: "6", Text: "SELECT * FROM nowhere", Calls: 5, TotalMs: 1},
		{QueryID: "7", Text: "not sql at all", Calls: 2, TotalMs: 1},
		{QueryID: "8", Text: "COPY public.customers (id, email) TO stdout", Calls: 1, TotalMs: 300},
		{QueryID: "9", Text: "DECLARE c CURSOR FOR SELECT * FROM orders", Calls: 8, TotalMs: 1},
		{QueryID: "10", Text: "", Calls: 9, TotalMs: 1},
		// Skipped altogether: reading pg_stat_statements is not traffic.
		{QueryID: "11", Text: "SELECT dealloc, stats_reset FROM public.pg_stat_statements_info", Calls: 3, TotalMs: 1},
		{QueryID: "12", Text: "SELECT query FROM public.pg_stat_statements($1)", Calls: 3, TotalMs: 1},
		{Hidden: true, Calls: 1000, TotalMs: 5000},
	})
	o := s.Tables["app.orders"]
	if o.SeqScans != 3 || o.QueryCalls != 104 || o.WildcardCalls != 4 {
		t.Fatalf("app.orders %+v", o)
	}
	if o.Columns["total"] != (ColumnUse{Queries: 1, Calls: 100}) || o.Columns["customer_id"].Calls != 104 {
		t.Fatalf("app.orders columns %+v", o.Columns)
	}
	if _, ok := o.Columns["created_at"]; ok {
		t.Fatal("c.created_at was attributed to orders")
	}
	c := s.Tables["public.customers"]
	if c == nil || c.QueryCalls != 110 || c.WildcardCalls != 6 || c.Columns["created_at"].Calls != 100 || c.Columns["email"].Calls != 100 {
		t.Fatalf("public.customers %+v", c)
	}
	po := s.Tables["public.orders"]
	if po == nil || po.WildcardCalls != 10 || po.Columns["legacy"].Calls != 10 || po.TopQueries[0].MeanMs != 90 {
		t.Fatalf("public.orders %+v", po)
	}
	if got := s.Statements.UnattributedCalls; got != 7+5+2+1+8+9 {
		t.Fatalf("unattributed %d", got)
	}
}

func TestResolveFollowsTheDatabasePath(t *testing.T) {
	m := &schema.Model{Schemas: []string{"public"}, Relations: []schema.Relation{rel("public", "orders", "id")}}
	path := Path{Schemas: []string{"sales", "public"}, Names: map[string]map[string]bool{
		"sales":  {"orders": true}, // exists in the database, but the profile does not show sales
		"public": {"orders": true, "users": true},
	}}
	s := &Snapshot{Tables: map[string]*Table{}}
	Attribute(s, m, path, []Statement{{Text: "SELECT id FROM orders", Calls: 5}})
	if s.Tables["public.orders"] != nil || s.Statements.UnattributedCalls != 5 {
		t.Fatalf("sales.orders comes first on the path, so public.orders must not get the call: %+v", s.Tables["public.orders"])
	}
}

func TestTopQueriesKeepsTheMostTime(t *testing.T) {
	tb := &Table{}
	for _, ms := range []float64{5, 50, 1, 500, 20, 7, 300} {
		addTopQuery(tb, Query{TotalMs: ms})
	}
	var got []float64
	for _, q := range tb.TopQueries {
		got = append(got, q.TotalMs)
	}
	if !slices.Equal(got, []float64{500, 300, 50, 20, 7}) {
		t.Fatalf("top %v", got)
	}
}

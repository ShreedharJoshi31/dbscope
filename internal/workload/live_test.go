package workload

import (
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func TestReadLive(t *testing.T) {
	m := &schema.Model{Schemas: []string{"public"}, Relations: []schema.Relation{
		rel("public", "customers", "id", "email"),
		rel("public", "orders", "id", "customer_id", "total"),
	}}
	for _, srv := range pgtest.Servers(t, "14", "17", "17+"+pgtest.PGStatStatements, "18") {
		t.Run(srv.String(), func(t *testing.T) {
			pgss := strings.HasSuffix(srv.Name, pgtest.PGStatStatements)
			d := srv.NewDB(t)
			d.Exec(t, `CREATE TABLE customers (id int PRIMARY KEY, email text);
				CREATE TABLE orders (id int PRIMARY KEY, customer_id int, total numeric);
				INSERT INTO orders SELECT g, g % 10, g FROM generate_series(1, 100) g`)
			ctx := t.Context()

			s, err := Read(ctx, d.Admin, m, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if s.Statements.Status != StatusNotInstalled || s.InRecovery {
				t.Fatalf("before CREATE EXTENSION: %+v", s.Statements)
			}
			if s.Tables["public.orders"] == nil {
				t.Fatalf("table statistics missing: %v", s.Tables)
			}

			d.Exec(t, "CREATE EXTENSION pg_stat_statements")
			if !pgss {
				s, err = Read(ctx, d.Admin, m, 10*time.Second)
				if err != nil || s.Statements.Status != StatusNotLoaded {
					t.Fatalf("installed but not preloaded: %+v %v", s.Statements, err)
				}
				return
			}
			d.Exec(t, "SELECT pg_stat_statements_reset()")
			for i := range 25 {
				if _, err := d.Admin.Exec(ctx, "SELECT total FROM orders WHERE customer_id = $1", i%10); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := d.Admin.Exec(ctx, "SELECT o.id, c.email FROM orders o JOIN customers c ON c.id = o.customer_id"); err != nil {
				t.Fatal(err)
			}
			s, err = Read(ctx, d.Admin, m, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if s.Statements.Status != StatusOK || s.Statements.Since == nil || s.Statements.TotalCalls < 26 {
				t.Fatalf("statements %+v", s.Statements)
			}
			o := s.Tables["public.orders"]
			if o.QueryCalls != 26 || o.Columns["customer_id"].Calls != 26 || o.Columns["total"].Calls != 25 || len(o.TopQueries) != 2 {
				t.Fatalf("orders %+v", o)
			}
			if !strings.Contains(o.TopQueries[0].Text+o.TopQueries[1].Text, "customer_id = $1") {
				t.Fatalf("top queries %+v", o.TopQueries)
			}
			if c := s.Tables["public.customers"]; c.QueryCalls != 1 || c.Columns["email"].Calls != 1 {
				t.Fatalf("customers %+v", c)
			}
		})
	}
}

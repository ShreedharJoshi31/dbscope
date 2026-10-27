package introspect

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

const readerPassword = "r"

// fixtureDB loads testdata/fixture.sql (and fixture18.sql on 18+) into a
// fresh database and returns it with the name of its SELECT-only role.
func fixtureDB(t *testing.T, srv *pgtest.Server) (*pgtest.DB, string) {
	t.Helper()
	d := srv.NewDB(t)
	reader := d.Role("reader")
	sql := readFile(t, "testdata/fixture.sql")
	// Roles are cluster-wide, so the fixture's role gets a unique name.
	sql = regexp.MustCompile(`\breader\b`).ReplaceAllString(sql, reader)
	d.Exec(t, sql)
	if srv.Major >= 18 {
		d.Exec(t, readFile(t, "testdata/fixture18.sql"))
	}
	return d, reader
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func q(s, n string) schema.QualifiedName { return schema.QualifiedName{Schema: s, Name: n} }

func names(m *schema.Model) []string {
	var out []string
	for _, r := range m.Relations {
		out = append(out, r.QName().String())
	}
	return out
}

func mustRel(t *testing.T, m *schema.Model, s, n string) *schema.Relation {
	t.Helper()
	r := m.Relation(q(s, n))
	if r == nil {
		t.Fatalf("relation %s.%s missing; have %v", s, n, names(m))
	}
	return r
}

func equalJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	g, _ := json.MarshalIndent(got, "", "  ")
	w, _ := json.MarshalIndent(want, "", "  ")
	if string(g) != string(w) {
		t.Errorf("%s:\ngot  %s\nwant %s", what, g, w)
	}
}

func TestIntrospectFixture(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "14", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			d, reader := fixtureDB(t, srv)
			c := d.Connect(t, reader, readerPassword)

			m, err := Introspect(t.Context(), c, []string{"app"}, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if m.ServerVersion/10000 != srv.Major || !slices.Equal(m.Schemas, []string{"app"}) ||
				m.IntrospectedAt.IsZero() || m.DurationMs < 0 {
				t.Fatalf("model header: version %d schemas %v at %v", m.ServerVersion, m.Schemas, m.IntrospectedAt)
			}
			if got, want := names(m), []string{"app.accounts", "app.big_orders", "app.events", "app.order_totals", "app.orders"}; !slices.Equal(got, want) {
				t.Fatalf("relations %v, want %v (partitions must be collapsed)", got, want)
			}
			equalJSON(t, "enums", m.Enums, []schema.Enum{{Schema: "app", Name: "status", Values: []string{"active", "suspended", "closed"}}})

			accounts := mustRel(t, m, "app", "accounts")
			accounts.RowEstimate = nil // 0 or unknown depending on how the index builds updated stats
			equalJSON(t, "app.accounts", accounts, schema.Relation{
				Schema: "app", Name: "accounts", Kind: schema.KindTable, Comment: "customer accounts",
				Columns: []schema.Column{
					{Name: "region", Type: "text"},
					{Name: "id", Type: "bigint"},
					{Name: "email", Type: "text", Comment: "login email"},
					{Name: "status", Type: "app.status", Default: "'active'::app.status"},
				},
				PrimaryKey: []string{"region", "id"},
				Uniques:    [][]string{{"email"}},
				Indexes: []schema.Index{
					{Name: "accounts_email_key", Columns: []string{"email"}, Unique: true, Method: "btree", Valid: true},
					{Name: "accounts_pkey", Columns: []string{"region", "id"}, Unique: true, Primary: true, Method: "btree", Valid: true},
				},
			})

			orderCols := []schema.Column{
				{Name: "id", Type: "bigint", Identity: "always"},
				{Name: "acct_id", Type: "bigint"},
				{Name: "acct_region", Type: "text"},
				{Name: "total", Type: "numeric(12,2)"},
				{Name: "created_at", Type: "timestamp with time zone", Default: "now()"},
				{Name: "total_cents", Type: "bigint", Nullable: true, Generated: "((total * (100)::numeric))::bigint", GeneratedKind: "stored"},
			}
			if srv.Major >= 18 {
				orderCols = append(orderCols, schema.Column{Name: "total_virtual", Type: "bigint", Nullable: true,
					Generated: "((total * (10)::numeric))::bigint", GeneratedKind: "virtual"})
			}
			equalJSON(t, "app.orders", mustRel(t, m, "app", "orders"), schema.Relation{
				Schema: "app", Name: "orders", Kind: schema.KindTable,
				Columns:    orderCols,
				PrimaryKey: []string{"id"},
				ForeignKeys: []schema.ForeignKey{{
					Name: "orders_acct_region_acct_id_fkey", Columns: []string{"acct_region", "acct_id"},
					Ref: q("app", "accounts"), RefCols: []string{"region", "id"},
					OnDelete: "cascade", OnUpdate: "no action", Validated: true,
				}},
				Checks: []schema.Check{{Name: "orders_total_check", Expr: "total >= 0::numeric", Validated: true}},
				Indexes: []schema.Index{
					{Name: "orders_lower", Columns: []string{"lower(acct_region)", "acct_id"}, Method: "btree", Valid: true},
					{Name: "orders_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Method: "btree", Valid: true},
					{Name: "orders_recent", Columns: []string{"created_at"}, Include: []string{"total"}, Method: "btree",
						Partial: "total > 0::numeric", Valid: true},
				},
			})

			equalJSON(t, "app.events", mustRel(t, m, "app", "events"), schema.Relation{
				Schema: "app", Name: "events", Kind: schema.KindPartitionedTable,
				Columns: []schema.Column{
					{Name: "id", Type: "bigint"},
					{Name: "at", Type: "date"},
					{Name: "order_id", Type: "bigint", Nullable: true},
				},
				PrimaryKey: []string{"id", "at"},
				ForeignKeys: []schema.ForeignKey{{
					Name: "events_order_id_fkey", Columns: []string{"order_id"}, Ref: q("app", "orders"), RefCols: []string{"id"},
					OnDelete: "no action", OnUpdate: "no action", Validated: true,
				}},
				Indexes:      []schema.Index{{Name: "events_pkey", Columns: []string{"id", "at"}, Unique: true, Primary: true, Method: "btree", Valid: true}},
				PartitionKey: "RANGE (at)",
				Partitions:   []schema.QualifiedName{q("app", "events_2026"), q("app", "events_2027")},
			})

			bo := mustRel(t, m, "app", "big_orders")
			if bo.Kind != schema.KindView || !slices.Equal(bo.DependsOn, []schema.QualifiedName{q("app", "accounts"), q("app", "orders")}) ||
				len(bo.Columns) != 2 || bo.Columns[1].Name != "email" || !bo.Columns[1].Nullable {
				t.Errorf("big_orders: %+v", bo)
			}
			ot := mustRel(t, m, "app", "order_totals")
			if ot.Kind != schema.KindMatView || !slices.Equal(ot.DependsOn, []schema.QualifiedName{q("app", "orders")}) {
				t.Errorf("order_totals: %+v", ot)
			}

			// The JSON form keeps an explicit null for unknown estimates and [] for columns.
			b, _ := json.Marshal(m)
			var raw struct {
				Relations []map[string]json.RawMessage `json:"relations"`
			}
			json.Unmarshal(b, &raw)
			if string(raw.Relations[2]["rowEstimate"]) != "null" {
				t.Errorf("events rowEstimate %s, want null", raw.Relations[2]["rowEstimate"])
			}
		})
	}
}

func TestIntrospectPartitionEstimate(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "14", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			d, _ := fixtureDB(t, srv)
			d.Exec(t, `INSERT INTO app.events (id, at) SELECT g, DATE '2026-03-01' FROM generate_series(1, 3) g;
				INSERT INTO app.events (id, at) SELECT g, DATE '2027-03-01' FROM generate_series(1, 2) g;
				ANALYZE app.events_2026`)
			m, err := Introspect(t.Context(), d.Admin, []string{"app"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			// events_2027 was never analyzed, so it counts as 0.
			if e := mustRel(t, m, "app", "events"); e.RowEstimate == nil || *e.RowEstimate != 3 {
				t.Fatalf("events estimate %v, want 3", e.RowEstimate)
			}
			d.Exec(t, `ANALYZE app.events_2027`)
			m, _ = Introspect(t.Context(), d.Admin, []string{"app"}, 0)
			if e := mustRel(t, m, "app", "events"); e.RowEstimate == nil || *e.RowEstimate != 5 || e.Pages < 2 {
				t.Fatalf("events estimate %v pages %d, want 5 and >= 2", e.RowEstimate, e.Pages)
			}

			// Sub-partitions and partitions in unselected schemas collapse into the root too.
			d.Exec(t, `CREATE SCHEMA archive;
				CREATE TABLE archive.events_2025 PARTITION OF app.events FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
				INSERT INTO app.events (id, at) VALUES (1, '2025-05-05'), (2, '2025-05-06');
				ANALYZE archive.events_2025;
				CREATE TABLE app.events_2028 PARTITION OF app.events FOR VALUES FROM ('2028-01-01') TO ('2029-01-01') PARTITION BY RANGE (at);
				CREATE TABLE app.events_2028_h1 PARTITION OF app.events_2028 FOR VALUES FROM ('2028-01-01') TO ('2028-07-01');
				INSERT INTO app.events (id, at) VALUES (1, '2028-02-02');
				ANALYZE app.events_2028_h1`)
			m, err = Introspect(t.Context(), d.Admin, []string{"app"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			e := mustRel(t, m, "app", "events")
			wantParts := []schema.QualifiedName{q("app", "events_2026"), q("app", "events_2027"), q("app", "events_2028"),
				q("app", "events_2028_h1"), q("archive", "events_2025")}
			if !slices.Equal(e.Partitions, wantParts) || e.RowEstimate == nil || *e.RowEstimate != 8 {
				t.Fatalf("events partitions %v estimate %v, want %v and 8", e.Partitions, e.RowEstimate, wantParts)
			}
			if got := names(m); len(got) != 5 {
				t.Fatalf("partitions leaked into relations: %v", got)
			}
			// A partition whose root is not selected is not shown on its own.
			m, err = Introspect(t.Context(), d.Admin, []string{"archive"}, 0)
			if err != nil || len(m.Relations) != 0 {
				t.Fatalf("archive alone: %v %v", err, names(m))
			}
		})
	}
}

// Older schemas (e.g. Pagila) declare foreign keys on each partition instead
// of on the partitioned table.
func TestIntrospectPartitionLocalForeignKeys(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "14", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			d := srv.NewDB(t)
			d.Exec(t, `CREATE TABLE customers (id int PRIMARY KEY);
				CREATE TABLE staff (id int PRIMARY KEY);
				CREATE TABLE pay (id int, customer_id int, staff_id int, at date) PARTITION BY RANGE (at);
				CREATE TABLE pay_a PARTITION OF pay FOR VALUES FROM ('2026-01-01') TO ('2026-07-01');
				CREATE TABLE pay_b PARTITION OF pay FOR VALUES FROM ('2026-07-01') TO ('2027-01-01');
				ALTER TABLE pay_a ADD FOREIGN KEY (customer_id) REFERENCES customers(id);
				ALTER TABLE pay_b ADD FOREIGN KEY (customer_id) REFERENCES customers(id);
				ALTER TABLE pay_a ADD FOREIGN KEY (staff_id) REFERENCES staff(id)`)
			m, err := Introspect(t.Context(), d.Admin, []string{"public"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			fks := mustRel(t, m, "public", "pay").ForeignKeys
			if len(fks) != 1 {
				t.Fatalf("want only the key declared on every partition, got %+v", fks)
			}
			fk := fks[0]
			if !slices.Equal(fk.Columns, []string{"customer_id"}) || fk.Ref != q("public", "customers") ||
				!slices.Equal(fk.RefCols, []string{"id"}) || fk.Inferred || !fk.Validated {
				t.Fatalf("promoted key %+v", fk)
			}
			if !strings.Contains(fk.Name, "declared on each of 2 partitions") {
				t.Fatalf("name %q", fk.Name)
			}
		})
	}
}

func TestIntrospectStubsAndSchemas(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "14", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			d, reader := fixtureDB(t, srv)
			d.Exec(t, `CREATE TABLE public.legacy_invoices (id int PRIMARY KEY, acct_region text, acct_id bigint,
				order_id bigint REFERENCES app.orders (id) ON DELETE SET NULL ON UPDATE RESTRICT,
				FOREIGN KEY (acct_region, acct_id) REFERENCES app.accounts (region, id) ON DELETE SET DEFAULT);
				CREATE VIEW public.recent AS SELECT id FROM app.orders;
				GRANT SELECT ON public.legacy_invoices, public.recent TO `+reader)
			c := d.Connect(t, reader, readerPassword)

			m, err := Introspect(t.Context(), c, []string{"public"}, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"app.accounts", "app.orders", "public.legacy_invoices", "public.legacy_posts", "public.legacy_users", "public.recent"}
			if got := names(m); !slices.Equal(got, want) {
				t.Fatalf("relations %v, want %v", got, want)
			}
			equalJSON(t, "stub accounts", mustRel(t, m, "app", "accounts"), schema.Relation{
				Schema: "app", Name: "accounts", Kind: schema.KindTable, Columns: []schema.Column{},
				PrimaryKey: []string{"region", "id"}, Stub: true,
			})
			if o := mustRel(t, m, "app", "orders"); !o.Stub || !slices.Equal(o.PrimaryKey, []string{"id"}) {
				t.Errorf("stub orders %+v", o)
			}
			li := mustRel(t, m, "public", "legacy_invoices")
			equalJSON(t, "legacy_invoices FKs", li.ForeignKeys, []schema.ForeignKey{
				{Name: "legacy_invoices_acct_region_acct_id_fkey", Columns: []string{"acct_region", "acct_id"},
					Ref: q("app", "accounts"), RefCols: []string{"region", "id"}, OnDelete: "set default", OnUpdate: "no action", Validated: true},
				{Name: "legacy_invoices_order_id_fkey", Columns: []string{"order_id"},
					Ref: q("app", "orders"), RefCols: []string{"id"}, OnDelete: "set null", OnUpdate: "restrict", Validated: true},
			})
			// View dependencies outside the selection are named but not stubbed.
			if r := mustRel(t, m, "public", "recent"); !slices.Equal(r.DependsOn, []schema.QualifiedName{q("app", "orders")}) {
				t.Errorf("recent depends on %v", r.DependsOn)
			}

			// Default selection: schemas the role has USAGE on. The reader lacks
			// USAGE on hidden, so it is left out.
			m, err = Introspect(t.Context(), c, nil, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(m.Schemas, []string{"app", "public"}) || m.Relation(q("hidden", "secret")) != nil {
				t.Fatalf("reader default schemas %v relations %v", m.Schemas, names(m))
			}
			for _, r := range m.Relations {
				if r.Stub {
					t.Errorf("unexpected stub %s with every referenced schema selected", r.QName())
				}
			}
			ns, err := Namespaces(t.Context(), c, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			equalJSON(t, "reader namespaces", ns, []Namespace{{"app", 5}, {"public", 4}})

			// Named explicitly, a schema without USAGE is still read from the catalogs.
			m, err = Introspect(t.Context(), c, []string{"hidden"}, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if got := names(m); !slices.Equal(got, []string{"hidden.secret"}) || len(m.Relations[0].Columns) != 1 {
				t.Fatalf("explicit hidden: %v", got)
			}

			// The superuser has USAGE everywhere.
			m, err = Introspect(t.Context(), d.Admin, []string{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(m.Schemas, []string{"app", "hidden", "public"}) {
				t.Fatalf("admin default schemas %v", m.Schemas)
			}
			ns, _ = Namespaces(t.Context(), d.Admin, 0)
			equalJSON(t, "admin namespaces", ns, []Namespace{{"app", 5}, {"hidden", 1}, {"public", 4}})

			// Unknown schemas yield an empty model, not an error.
			m, err = Introspect(t.Context(), c, []string{"nope"}, 10*time.Second)
			if err != nil || len(m.Relations) != 0 || m.Enums == nil {
				t.Fatalf("unknown schema: %v %+v", err, m)
			}
		})
	}
}

func TestIntrospectStatementTimeout(t *testing.T) {
	for _, srv := range pgtest.Servers(t, "14", "18") {
		t.Run(srv.String(), func(t *testing.T) {
			d, reader := fixtureDB(t, srv)
			c := d.Connect(t, reader, readerPassword)
			// Holding an exclusive lock on a catalog the relations query reads
			// makes introspection wait until statement_timeout fires.
			blocker, err := d.Admin.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(t.Context(), "LOCK TABLE pg_catalog.pg_description IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err = Introspect(t.Context(), c, []string{"app"}, 200*time.Millisecond)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
				t.Fatalf("want SQLSTATE 57014, got %v", err)
			}
			if el := time.Since(start); el > 5*time.Second {
				t.Fatalf("timeout took %v", el)
			}
			blocker.Rollback(t.Context())
			// SET LOCAL ended with the transaction, so the connection is reusable.
			if _, err := Introspect(t.Context(), c, []string{"app"}, 10*time.Second); err != nil {
				t.Fatalf("after timeout: %v", err)
			}
			var setting string
			c.QueryRow(t.Context(), "SHOW statement_timeout").Scan(&setting)
			if setting != "0" {
				t.Fatalf("statement_timeout leaked out of the transaction: %s", setting)
			}
		})
	}
}

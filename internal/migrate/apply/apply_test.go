package apply

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/parse"
	"github.com/dbscope/dbscope/internal/sample"
	"github.com/dbscope/dbscope/internal/schema"
)

func applyAll(t *testing.T, st *State, sql string) int {
	t.Helper()
	f := parse.Split(sql)
	for _, s := range f.Statements {
		if s.Err != nil {
			t.Fatalf("line %d: %v", s.Line, s.Err)
		}
		st.Apply(s.Node)
	}
	return len(f.Statements)
}

func q(s, n string) QName { return QName{Schema: s, Name: n} }

// series turns the sample schema into a modified one in 30 statements.
const series = `
CREATE TYPE shop.shipping_speed AS ENUM ('standard', 'express');
ALTER TYPE shop.order_status ADD VALUE 'on_hold' AFTER 'paid';
ALTER TYPE shop.shipping_speed RENAME VALUE 'express' TO 'priority';
CREATE TABLE shop.gift_cards (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code text NOT NULL UNIQUE,
    customer_id bigint REFERENCES shop.customers (id) ON DELETE SET NULL,
    balance_cents bigint NOT NULL DEFAULT 0 CHECK (balance_cents >= 0),
    speed shop.shipping_speed
);
CREATE INDEX gift_cards_customer_idx ON shop.gift_cards (customer_id);
ALTER TABLE shop.orders ADD COLUMN gift_card_id bigint;
ALTER TABLE shop.orders ADD CONSTRAINT orders_gift_card_fk FOREIGN KEY (gift_card_id) REFERENCES shop.gift_cards (id) NOT VALID;
ALTER TABLE shop.orders VALIDATE CONSTRAINT orders_gift_card_fk;
ALTER TABLE shop.orders ADD COLUMN notes varchar(200);
ALTER TABLE shop.orders ALTER COLUMN notes TYPE varchar(500);
ALTER TABLE shop.orders ALTER COLUMN total_cents TYPE numeric(14,2);
ALTER TABLE shop.orders ALTER COLUMN currency SET DEFAULT 'EUR';
ALTER TABLE shop.customers RENAME COLUMN phone TO phone_number;
ALTER TABLE shop.customers ALTER COLUMN phone_number SET NOT NULL;
ALTER TABLE shop.customers ADD CONSTRAINT customers_phone_check CHECK (phone_number <> '') NOT VALID;
ALTER TABLE shop.customers DROP COLUMN marketing_opt_in;
CREATE UNIQUE INDEX CONCURRENTLY customers_phone_key ON shop.customers (phone_number);
ALTER TABLE shop.customers ADD CONSTRAINT customers_phone_unique UNIQUE USING INDEX customers_phone_key;
ALTER TABLE shop.reviews RENAME TO product_reviews;
ALTER TABLE shop.product_reviews ALTER COLUMN body DROP NOT NULL;
DROP INDEX shop.refunds_payment_idx;
DROP TABLE shop.refunds;
ALTER TABLE shop.warehouses ADD COLUMN region text NOT NULL DEFAULT 'eu';
ALTER TABLE shop.warehouses DROP COLUMN region;
CREATE VIEW shop.gift_card_balances AS SELECT g.code, g.balance_cents, c.email FROM shop.gift_cards g JOIN shop.customers c ON c.id = g.customer_id;
CREATE MATERIALIZED VIEW shop.daily_orders AS SELECT date_trunc('day', placed_at) AS day, count(*) AS orders FROM shop.orders GROUP BY 1;
CREATE OR REPLACE VIEW shop.order_summaries AS SELECT o.id, o.placed_at, o.status FROM shop.orders o;
COMMENT ON COLUMN shop.orders.notes IS 'free text from support';
CREATE TABLE app.audit_log_2026_11 PARTITION OF app.audit_log FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
ALTER TABLE app.audit_log DETACH PARTITION app.audit_log_2026_08;
`

func TestSeriesOnSample(t *testing.T) {
	base, err := sample.Model()
	if err != nil {
		t.Fatal(err)
	}
	st := New(base, nil)
	if n := applyAll(t, st, series); n != 30 {
		t.Fatalf("%d statements, want 30", n)
	}
	if len(st.Warnings) > 0 {
		t.Fatalf("warnings: %v", st.Warnings)
	}
	m := st.Model

	gc := m.Relation(q("shop", "gift_cards"))
	if gc == nil || gc.Kind != schema.KindTable || len(gc.Columns) != 5 {
		t.Fatalf("gift_cards %+v", gc)
	}
	if id := gc.Column("id"); id.Identity != "always" || id.Nullable || id.Type != "bigint" {
		t.Errorf("id %+v", id)
	}
	if bal := gc.Column("balance_cents"); bal.Default != "0" || bal.Nullable {
		t.Errorf("balance %+v", bal)
	}
	if sp := gc.Column("speed"); sp.Type != "shop.shipping_speed" || !sp.Nullable {
		t.Errorf("speed %+v", sp)
	}
	if !slices.Equal(gc.PrimaryKey, []string{"id"}) || len(gc.Uniques) != 1 || gc.Uniques[0][0] != "code" {
		t.Errorf("keys %v %v", gc.PrimaryKey, gc.Uniques)
	}
	if len(gc.ForeignKeys) != 1 || gc.ForeignKeys[0].Name != "gift_cards_customer_id_fkey" ||
		gc.ForeignKeys[0].Ref != q("shop", "customers") || gc.ForeignKeys[0].OnDelete != "set null" ||
		!slices.Equal(gc.ForeignKeys[0].RefCols, []string{"id"}) {
		t.Errorf("fk %+v", gc.ForeignKeys)
	}
	if len(gc.Checks) != 1 || gc.Checks[0].Name != "gift_cards_balance_cents_check" || !gc.Checks[0].Validated {
		t.Errorf("checks %+v", gc.Checks)
	}
	names := func(r *schema.Relation) []string {
		var out []string
		for _, ix := range r.Indexes {
			out = append(out, ix.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := names(gc); !slices.Equal(got, []string{"gift_cards_code_key", "gift_cards_customer_idx", "gift_cards_pkey"}) {
		t.Errorf("gift_cards indexes %v", got)
	}

	o := m.Relation(q("shop", "orders"))
	if c := o.Column("notes"); c == nil || c.Type != "character varying(500)" || c.Comment != "free text from support" {
		t.Errorf("notes %+v", c)
	}
	if c := o.Column("total_cents"); c.Type != "numeric(14,2)" {
		t.Errorf("total_cents %+v", c)
	}
	if c := o.Column("currency"); c.Default != "'EUR'" {
		t.Errorf("currency default %q", c.Default)
	}
	fk := o.ForeignKeys[len(o.ForeignKeys)-1]
	if fk.Name != "orders_gift_card_fk" || !fk.Validated || fk.Ref != q("shop", "gift_cards") {
		t.Errorf("orders fk %+v", fk)
	}

	c := m.Relation(q("shop", "customers"))
	if c.Column("phone") != nil || c.Column("marketing_opt_in") != nil {
		t.Error("renamed or dropped column still present")
	}
	if p := c.Column("phone_number"); p == nil || p.Nullable {
		t.Errorf("phone_number %+v", p)
	}
	if len(c.Checks) != 1 || c.Checks[0].Validated {
		t.Errorf("NOT VALID check %+v", c.Checks)
	}
	if !slices.ContainsFunc(c.Indexes, func(ix schema.Index) bool { return ix.Name == "customers_phone_unique" && ix.Unique }) ||
		!slices.ContainsFunc(c.Uniques, func(u []string) bool { return slices.Equal(u, []string{"phone_number"}) }) {
		t.Errorf("USING INDEX: indexes %v uniques %v", names(c), c.Uniques)
	}

	if m.Relation(q("shop", "reviews")) != nil || m.Relation(q("shop", "product_reviews")) == nil {
		t.Error("rename table")
	}
	if b := m.Relation(q("shop", "product_reviews")).Column("body"); b == nil || !b.Nullable {
		t.Errorf("body %+v", b)
	}
	if m.Relation(q("shop", "refunds")) != nil {
		t.Error("refunds not dropped")
	}
	if w := m.Relation(q("shop", "warehouses")); w.Column("region") != nil {
		t.Error("region not dropped")
	}

	v := m.Relation(q("shop", "gift_card_balances"))
	if v == nil || v.Kind != schema.KindView || !slices.Equal(v.DependsOn, []QName{q("shop", "customers"), q("shop", "gift_cards")}) {
		t.Fatalf("view %+v", v)
	}
	if col := v.Columns[0]; col.Name != "code" || col.Type != "text" {
		t.Errorf("view column %+v", col)
	}
	mv := m.Relation(q("shop", "daily_orders"))
	if mv == nil || mv.Kind != schema.KindMatView || !slices.Equal(mv.DependsOn, []QName{q("shop", "orders")}) ||
		mv.Columns[0].Name != "day" || mv.Columns[1].Name != "orders" {
		t.Fatalf("matview %+v", mv)
	}
	if os := m.Relation(q("shop", "order_summaries")); len(os.Columns) != 3 || !slices.Equal(os.DependsOn, []QName{q("shop", "orders")}) {
		t.Errorf("replaced view %+v", os)
	}

	al := m.Relation(q("app", "audit_log"))
	wantParts := []QName{q("app", "audit_log_2026_09"), q("app", "audit_log_2026_10"), q("app", "audit_log_2026_11")}
	if !slices.Equal(al.Partitions, wantParts) {
		t.Errorf("partitions %v", al.Partitions)
	}
	if d := m.Relation(q("app", "audit_log_2026_08")); d == nil || d.Kind != schema.KindTable {
		t.Errorf("detached partition %+v", d)
	}

	var enum *schema.Enum
	for i := range m.Enums {
		if m.Enums[i].Name == "order_status" {
			enum = &m.Enums[i]
		}
	}
	if !slices.Equal(enum.Values, []string{"pending", "paid", "on_hold", "fulfilled", "cancelled", "refunded"}) {
		t.Errorf("order_status %v", enum.Values)
	}
	if e := st.Enum("shop", "shipping_speed"); e == nil || !slices.Equal(e.Values, []string{"standard", "priority"}) {
		t.Errorf("shipping_speed %+v", e)
	}

	// The base is untouched.
	if base.Relation(q("shop", "refunds")) == nil || base.Relation(q("shop", "customers")).Column("phone") == nil {
		t.Fatal("base model was mutated")
	}

	d := st.Diff()
	want := Diff{
		Added:   []string{"app.audit_log_2026_08", "shop.daily_orders", "shop.gift_card_balances", "shop.gift_cards", "shop.product_reviews"},
		Dropped: []string{"shop.refunds", "shop.reviews"},
		Altered: []string{"app.audit_log", "shop.customers", "shop.order_summaries", "shop.orders"},
		Columns: []ColumnChange{
			{Relation: "shop.customers", Column: "marketing_opt_in", Change: "dropped"},
			{Relation: "shop.customers", Column: "phone", Change: "renamed", To: "phone_number"},
			{Relation: "shop.customers", Column: "phone_number", Change: "altered"},
			{Relation: "shop.order_summaries", Column: "customer_email", Change: "dropped"},
			{Relation: "shop.order_summaries", Column: "item_count", Change: "dropped"},
			{Relation: "shop.order_summaries", Column: "total_cents", Change: "dropped"},
			{Relation: "shop.orders", Column: "currency", Change: "altered"},
			{Relation: "shop.orders", Column: "gift_card_id", Change: "added"},
			{Relation: "shop.orders", Column: "notes", Change: "added"},
			{Relation: "shop.orders", Column: "total_cents", Change: "altered"},
		},
	}
	got, _ := json.MarshalIndent(d, "", " ")
	exp, _ := json.MarshalIndent(want, "", " ")
	if string(got) != string(exp) {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, exp)
	}
}

func TestTypeChange(t *testing.T) {
	for _, c := range []struct {
		old, new          string
		rewrite, narrowed bool
	}{
		{"character varying(20)", "character varying(40)", false, false},
		{"character varying(20)", "text", false, false},
		{"text", "character varying", false, false},
		{"text", "character varying(100)", true, true},
		{"character varying(40)", "character varying(20)", true, true},
		{"numeric(10,2)", "numeric(12,2)", false, false},
		{"numeric(10,2)", "numeric(12,3)", true, false},
		{"numeric(10,2)", "numeric", false, false},
		{"numeric(10,2)", "numeric(8,2)", true, true},
		{"integer", "bigint", true, false},
		{"bigint", "integer", true, true},
		{"timestamp(3) with time zone", "timestamp(6) with time zone", false, false},
		{"timestamp without time zone", "timestamp with time zone", true, false},
		{"cidr", "inet", false, false},
		{"text", "text", false, false},
		{"", "text", true, false},
	} {
		got := TypeChange(ParseType(c.old), ParseType(c.new))
		if c.old == "" {
			got = TypeChange(Type{}, ParseType(c.new))
		}
		if got.Rewrite != c.rewrite || got.Narrowing != c.narrowed {
			t.Errorf("%s -> %s: rewrite %v narrowing %v (%s)", c.old, c.new, got.Rewrite, got.Narrowing, got.Reason)
		}
	}
	for _, s := range []string{"character varying(255)", "numeric(10,2)", "timestamp(3) with time zone", "app.user_status", "integer[]", "text"} {
		if got := ParseType(s).String(); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}

func TestLoadDump(t *testing.T) {
	dump := `--
-- PostgreSQL database dump
--
\restrict k3yK3y

-- Dumped from database version 16.4
SET statement_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);
CREATE SCHEMA app;
CREATE TYPE app.kind AS ENUM ('a', 'b');
CREATE DOMAIN app.pos AS integer CONSTRAINT pos_check CHECK (VALUE > 0);
CREATE FUNCTION app.f() RETURNS integer LANGUAGE sql AS $$ SELECT 1 $$;
CREATE TABLE app.users (id bigint NOT NULL, email text NOT NULL, kind app.kind);
CREATE TABLE app.posts (id bigint NOT NULL, user_id bigint);
CREATE SEQUENCE app.posts_id_seq;
ALTER TABLE ONLY app.users ADD CONSTRAINT users_pkey PRIMARY KEY (id);
ALTER TABLE ONLY app.posts ADD CONSTRAINT posts_pkey PRIMARY KEY (id);
ALTER TABLE ONLY app.posts ADD CONSTRAINT posts_user_id_fkey FOREIGN KEY (user_id) REFERENCES app.users(id);
CREATE INDEX posts_user_idx ON app.posts USING btree (user_id);
CREATE VIEW app.v AS SELECT u.id, p.id AS post_id FROM (app.users u JOIN app.posts p ON ((p.user_id = u.id)));
CREATE POLICY p ON app.posts USING (true);
CREATE STATISTICS app.s ON id, user_id FROM app.posts;
ALTER TABLE app.posts OWNER TO someone;
CREATE EVENT TRIGGER et ON ddl_command_start EXECUTE FUNCTION app.f();
CREATE AGGREGATE app.agg(integer) (SFUNC = int4pl, STYPE = integer);
\unrestrict k3yK3y
`
	d := LoadDump(dump)
	m := d.Model
	if m.ServerVersion != 160004 || !slices.Equal(m.Schemas, []string{"app"}) || len(m.Relations) != 3 {
		t.Fatalf("model %d %v %d", m.ServerVersion, m.Schemas, len(m.Relations))
	}
	posts := m.Relation(q("app", "posts"))
	if len(posts.ForeignKeys) != 1 || posts.ForeignKeys[0].Ref != q("app", "users") || len(posts.Indexes) != 2 || posts.RowEstimate != nil {
		t.Fatalf("posts %+v", posts)
	}
	if v := m.Relation(q("app", "v")); !slices.Equal(v.DependsOn, []QName{q("app", "posts"), q("app", "users")}) {
		t.Fatalf("view deps %v", v.DependsOn)
	}
	if !d.Catalog.ConstrainedDomains["app.pos"] || len(m.Enums) != 1 {
		t.Fatalf("catalog %+v enums %v", d.Catalog, m.Enums)
	}
	want := "schema dump: 6 statements were not modeled (CreateEventTrigStmt x1, CreateFunctionStmt x1, CreatePolicyStmt x1, CreateSeqStmt x1, CreateStatsStmt x1, DefineStmt x1)"
	if len(d.Warnings) != 1 || d.Warnings[0] != want {
		t.Fatalf("warnings %v", d.Warnings)
	}
}

func TestSearchPath(t *testing.T) {
	st := New(nil, nil)
	applyAll(t, st, `CREATE SCHEMA a; SET search_path = a, public; CREATE TABLE t (id int); RESET search_path; CREATE TABLE t (id int);`)
	if st.Lookup(q("a", "t")) == nil || st.Lookup(q("public", "t")) == nil {
		t.Fatalf("relations %+v", st.Model.Relations)
	}
}

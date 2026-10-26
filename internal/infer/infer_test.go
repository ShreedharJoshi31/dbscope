package infer

import (
	"cmp"
	"encoding/json"
	"slices"
	"testing"

	"github.com/dbscope/dbscope/internal/schema"
)

func col(name, typ string) schema.Column { return schema.Column{Name: name, Type: typ} }

func table(s, n string, pk []string, cols ...schema.Column) schema.Relation {
	return schema.Relation{Schema: s, Name: n, Kind: schema.KindTable, Columns: cols, PrimaryKey: pk}
}

func withIndex(r schema.Relation, cols ...string) schema.Relation {
	r.Indexes = append(r.Indexes, schema.Index{Name: r.Name + "_ix", Columns: cols, Method: "btree", Valid: true})
	return r
}

func model(rels ...schema.Relation) *schema.Model {
	m := &schema.Model{Schemas: []string{}}
	for _, r := range rels {
		if !slices.Contains(m.Schemas, r.Schema) {
			m.Schemas = append(m.Schemas, r.Schema)
		}
	}
	slices.Sort(m.Schemas)
	m.Relations = rels
	slices.SortFunc(m.Relations, func(a, b schema.Relation) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
	})
	return m
}

var users = table("public", "users", []string{"id"}, col("id", "bigint"), col("email", "text"))

type want struct {
	key  string
	conf float64
}

func TestInfer(t *testing.T) {
	cases := []struct {
		name string
		m    *schema.Model
		want []want
	}{
		{"plural s, unindexed", model(users, table("public", "posts", []string{"id"}, col("id", "bigint"), col("user_id", "bigint"))),
			[]want{{"public.posts(user_id)->public.users(id)", 0.6}}},
		{"indexed child column", model(users, withIndex(table("public", "posts", []string{"id"}, col("id", "bigint"), col("user_id", "bigint")), "user_id", "id")),
			[]want{{"public.posts(user_id)->public.users(id)", 0.8}}},
		{"index on a later column does not count", model(users, withIndex(table("public", "posts", nil, col("id", "bigint"), col("user_id", "bigint")), "id", "user_id")),
			[]want{{"public.posts(user_id)->public.users(id)", 0.6}}},
		{"exact singular table name, indexed", model(
			table("public", "user", []string{"id"}, col("id", "integer")),
			withIndex(table("public", "post", nil, col("user_id", "integer")), "user_id")),
			[]want{{"public.post(user_id)->public.user(id)", 0.9}}},
		{"exact name preferred over plural", model(
			table("public", "user", []string{"id"}, col("id", "integer")), users,
			table("public", "post", nil, col("user_id", "integer"))),
			[]want{{"public.post(user_id)->public.user(id)", 0.7}}},
		{"es plural", model(
			table("public", "addresses", []string{"id"}, col("id", "uuid")),
			table("public", "orders", nil, col("address_id", "uuid"))),
			[]want{{"public.orders(address_id)->public.addresses(id)", 0.6}}},
		{"ies plural", model(
			table("shop", "categories", []string{"id"}, col("id", "integer")),
			table("shop", "products", nil, col("category_id", "bigint"))),
			[]want{{"shop.products(category_id)->shop.categories(id)", 0.6}}},
		{"y after vowel uses s", model(
			table("public", "keys", []string{"id"}, col("id", "integer")),
			table("public", "keies", []string{"id"}, col("id", "integer")),
			table("public", "grants", nil, col("key_id", "integer"))),
			[]want{{"public.grants(key_id)->public.keys(id)", 0.6}}},
		{"camelCase column", model(
			table("public", "order_items", []string{"id"}, col("id", "integer")),
			table("public", "shipments", nil, col("orderItemId", "integer"))),
			[]want{{"public.shipments(orderItemId)->public.order_items(id)", 0.6}}},
		{"upper-case suffix", model(users, table("public", "posts", nil, col("USER_ID", "bigint"))),
			[]want{{"public.posts(USER_ID)->public.users(id)", 0.6}}},
		{"multi-word stem", model(
			table("public", "legacy_users", []string{"id"}, col("id", "integer")),
			table("public", "legacy_posts", []string{"id"}, col("id", "integer"), col("legacy_user_id", "integer"))),
			[]want{{"public.legacy_posts(legacy_user_id)->public.legacy_users(id)", 0.6}}},
		{"same schema wins over other schemas", model(
			table("a", "users", []string{"id"}, col("id", "bigint")),
			table("b", "users", []string{"id"}, col("id", "bigint")),
			table("b", "posts", nil, col("user_id", "bigint"))),
			[]want{{"b.posts(user_id)->b.users(id)", 0.6}}},
		{"other schemas in order", model(
			table("z", "users", []string{"id"}, col("id", "bigint")),
			table("m", "users", []string{"id"}, col("id", "bigint")),
			table("b", "posts", nil, col("user_id", "bigint"))),
			[]want{{"b.posts(user_id)->m.users(id)", 0.6}}},
		{"unique column when the primary key is composite", model(
			schema.Relation{Schema: "public", Name: "accounts", Kind: schema.KindTable, PrimaryKey: []string{"region", "id"},
				Columns: []schema.Column{col("region", "text"), col("id", "bigint")}, Uniques: [][]string{{"id"}}},
			table("public", "orders", nil, col("account_id", "bigint"))),
			[]want{{"public.orders(account_id)->public.accounts(id)", 0.6}}},
		{"unique index as target", model(
			schema.Relation{Schema: "public", Name: "teams", Kind: schema.KindTable,
				Columns: []schema.Column{col("id", "integer")},
				Indexes: []schema.Index{{Name: "teams_id", Columns: []string{"id"}, Unique: true, Valid: true}}},
			table("public", "members", nil, col("team_id", "integer"))),
			[]want{{"public.members(team_id)->public.teams(id)", 0.6}}},
		{"varchar lengths may differ", model(
			table("public", "countries", []string{"code"}, col("code", "character varying(2)")),
			table("public", "cities", nil, col("country_id", "character varying(3)"))),
			[]want{{"public.cities(country_id)->public.countries(code)", 0.6}}},
		{"self reference", model(table("public", "parents", []string{"id"}, col("id", "integer"), col("parent_id", "integer"))),
			[]want{{"public.parents(parent_id)->public.parents(id)", 0.6}}},
		{"one-to-one primary key column counts as indexed", model(users, table("public", "profiles", []string{"user_id"}, col("user_id", "bigint"))),
			[]want{{"public.profiles(user_id)->public.users(id)", 0.8}}},
		{"partitioned child table", model(users, schema.Relation{Schema: "public", Name: "events", Kind: schema.KindPartitionedTable,
			Columns: []schema.Column{col("user_id", "bigint")}}),
			[]want{{"public.events(user_id)->public.users(id)", 0.6}}},
		{"deterministic order across relations and columns", model(
			table("public", "teams", []string{"id"}, col("id", "integer")), users,
			table("public", "memberships", nil, col("user_id", "bigint"), col("team_id", "integer")),
			table("public", "invites", nil, col("team_id", "integer"))),
			[]want{
				{"public.invites(team_id)->public.teams(id)", 0.6},
				{"public.memberships(user_id)->public.users(id)", 0.6},
				{"public.memberships(team_id)->public.teams(id)", 0.6},
			}},

		// False-positive guards.
		{"polymorphic pair", model(
			table("public", "comments", nil, col("commentable_id", "bigint"), col("commentable_type", "text")),
			table("public", "commentables", []string{"id"}, col("id", "bigint"))), nil},
		{"polymorphic camelCase pair", model(
			table("public", "comments", nil, col("commentableId", "bigint"), col("commentableType", "text")),
			table("public", "commentables", []string{"id"}, col("id", "bigint"))), nil},
		{"type mismatch", model(users, table("public", "posts", nil, col("user_id", "text"))), nil},
		{"uuid vs integer", model(users, table("public", "posts", nil, col("user_id", "uuid"))), nil},
		{"non-unique target column", model(
			table("public", "users", nil, col("id", "bigint")),
			withIndex(table("public", "posts", nil, col("user_id", "bigint")), "user_id")), nil},
		{"partial unique index is not a key", model(
			schema.Relation{Schema: "public", Name: "users", Kind: schema.KindTable, Columns: []schema.Column{col("id", "bigint")},
				Indexes: []schema.Index{{Name: "u", Columns: []string{"id"}, Unique: true, Valid: true, Partial: "id > 0"}}},
			table("public", "posts", nil, col("user_id", "bigint"))), nil},
		{"composite primary key only", model(
			table("public", "users", []string{"id", "tenant"}, col("id", "bigint"), col("tenant", "text")),
			table("public", "posts", nil, col("user_id", "bigint"))), nil},
		{"already declared", model(users, schema.Relation{Schema: "public", Name: "posts", Kind: schema.KindTable,
			Columns: []schema.Column{col("user_id", "bigint")},
			ForeignKeys: []schema.ForeignKey{{Name: "posts_user_fk", Columns: []string{"user_id"},
				Ref: schema.QualifiedName{Schema: "public", Name: "users"}, RefCols: []string{"id"}}}}), nil},
		{"part of a declared composite key", model(users, schema.Relation{Schema: "public", Name: "posts", Kind: schema.KindTable,
			Columns: []schema.Column{col("region", "text"), col("user_id", "bigint")},
			ForeignKeys: []schema.ForeignKey{{Name: "fk", Columns: []string{"region", "user_id"},
				Ref: schema.QualifiedName{Schema: "public", Name: "accounts"}, RefCols: []string{"region", "id"}}}}), nil},
		{"no such table", model(users, table("public", "posts", nil, col("author_id", "bigint"))), nil},
		{"view target", model(
			schema.Relation{Schema: "public", Name: "users", Kind: schema.KindView, Columns: []schema.Column{col("id", "bigint")}, PrimaryKey: []string{"id"}},
			table("public", "posts", nil, col("user_id", "bigint"))), nil},
		{"view child", model(users, schema.Relation{Schema: "public", Name: "v", Kind: schema.KindView, Columns: []schema.Column{col("user_id", "bigint")}}), nil},
		{"stub target", model(
			schema.Relation{Schema: "other", Name: "users", Kind: schema.KindTable, PrimaryKey: []string{"id"}, Columns: []schema.Column{}, Stub: true},
			table("public", "posts", nil, col("user_id", "bigint"))), nil},
		{"bare _id and id", model(table("public", "s", []string{"id"}, col("id", "bigint"), col("_id", "bigint"))), nil},
		{"lower-case suffix is not camelCase", model(
			table("public", "pa", []string{"id"}, col("id", "bigint")),
			table("public", "t", nil, col("paid", "bigint"), col("PAId", "bigint"))), nil},
		{"the target key itself", model(table("public", "users", []string{"user_id"}, col("user_id", "bigint"))), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []want
			for _, c := range Infer(tc.m) {
				fk := c.FK
				if !fk.Inferred || fk.Validated || len(fk.Columns) != 1 || len(fk.RefCols) != 1 ||
					fk.Name != Key(c.From, fk.Columns, fk.Ref, fk.RefCols) {
					t.Errorf("malformed candidate %+v", c)
				}
				got = append(got, want{fk.Name, fk.Confidence})
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestApplyDecisions(t *testing.T) {
	build := func() *schema.Model {
		return model(users,
			table("public", "teams", []string{"id"}, col("id", "integer")),
			table("public", "posts", nil, col("user_id", "bigint"), col("team_id", "integer"), col("editor_id", "bigint")))
	}
	m := build()
	before, _ := json.Marshal(build())
	Apply(m, map[string]string{
		"public.posts(user_id)->public.users(id)": Rejected,
		"public.posts(team_id)->public.teams(id)": Accepted,
		"public.gone(x_id)->public.xs(id)":        Accepted,
	})
	fks := m.Relation(schema.QualifiedName{Schema: "public", Name: "posts"}).ForeignKeys
	if len(fks) != 1 || fks[0].Name != "public.posts(team_id)->public.teams(id)" || fks[0].Confidence != 1 || !fks[0].Inferred {
		t.Fatalf("after decisions: %+v", fks)
	}
	m2 := build()
	Apply(m2, nil)
	if n := len(m2.Relation(schema.QualifiedName{Schema: "public", Name: "posts"}).ForeignKeys); n != 2 {
		t.Fatalf("no decisions: %d FKs", n)
	}
	// Apply is idempotent: the inferred keys now cover their columns.
	Apply(m2, nil)
	if n := len(m2.Relation(schema.QualifiedName{Schema: "public", Name: "posts"}).ForeignKeys); n != 2 {
		t.Fatalf("second Apply: %d FKs", n)
	}
	if again, _ := json.Marshal(build()); string(again) != string(before) {
		t.Fatal("Apply mutated a model it was not given")
	}
}

func TestKey(t *testing.T) {
	got := Key(schema.QualifiedName{Schema: "app", Name: "orders"}, []string{"acct_region", "acct_id"},
		schema.QualifiedName{Schema: "app", Name: "accounts"}, []string{"region", "id"})
	if want := "app.orders(acct_region,acct_id)->app.accounts(region,id)"; got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}

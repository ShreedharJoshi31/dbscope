package mysql

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

const testPassword = mytest.Password

func server(t *testing.T, flavor string) conn.Profile { return mytest.Server(t, flavor) }

const fixtureDDL = `
CREATE TABLE users (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  email VARCHAR(200) NOT NULL UNIQUE,
  name VARCHAR(100) DEFAULT 'anon' COMMENT 'display name',
  email_domain VARCHAR(200) AS (substring_index(email, '@', -1)) STORED,
  avatar BLOB
) COMMENT = 'people';
CREATE TABLE orders (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  total DECIMAL(10,2) NOT NULL CHECK (total >= 0),
  big BIGINT UNSIGNED,
  CONSTRAINT orders_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  INDEX orders_user_total (user_id, total)
);
CREATE VIEW big_orders AS SELECT id, total FROM orders WHERE total > 100;
INSERT INTO users (email, avatar) VALUES ('a@x.io', x'00ff'), ('b@y.io', NULL);
INSERT INTO orders (user_id, total, big) VALUES (1, 150.50, 18446744073709551615), (2, 9.99, 1);
CREATE USER 'reader'@'%' IDENTIFIED BY 'reader-test';
GRANT SELECT ON app.* TO 'reader'@'%'
`

func setup(t *testing.T, flavor string) (conn.Profile, *Server) {
	p := server(t, flavor)
	db, err := Open(p, testPassword, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, stmt := range strings.Split(fixtureDDL, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	srv, err := Describe(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return p, &srv
}

func TestEngines(t *testing.T) {
	for _, flavor := range []string{"mysql", "mariadb"} {
		t.Run(flavor, func(t *testing.T) {
			t.Parallel()
			p, srv := setup(t, flavor)
			if srv.Flavor != flavor || srv.Num < 80000 {
				t.Fatalf("server %+v", srv)
			}
			db, err := Open(p, testPassword, 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()

			t.Run("introspect", func(t *testing.T) {
				m, err := Introspect(ctx, db, nil, "app")
				if err != nil {
					t.Fatal(err)
				}
				if m.Engine != flavor || len(m.Relations) != 3 {
					t.Fatalf("model %s %d relations", m.Engine, len(m.Relations))
				}
				users := m.Relation(schema.QualifiedName{Schema: "app", Name: "users"})
				if users.Comment != "people" || users.Column("id").Identity != "by_default" || !slices.Equal(users.PrimaryKey, []string{"id"}) {
					t.Errorf("users %+v", users)
				}
				if c := users.Column("email_domain"); c.GeneratedKind != "stored" || !strings.Contains(c.Generated, "substring_index") {
					t.Errorf("generated %+v", c)
				}
				if c := users.Column("name"); c.Comment != "display name" || !strings.Contains(c.Default, "anon") || !c.Nullable {
					t.Errorf("name %+v", c)
				}
				if c := users.Column("avatar"); c.Default != "" {
					t.Errorf("no default read as %q", c.Default)
				}
				if !slices.EqualFunc(users.Uniques, [][]string{{"email"}}, slices.Equal) {
					t.Errorf("uniques %v", users.Uniques)
				}
				orders := m.Relation(schema.QualifiedName{Schema: "app", Name: "orders"})
				if len(orders.ForeignKeys) != 1 || orders.ForeignKeys[0].OnDelete != "cascade" || orders.ForeignKeys[0].Ref.Name != "users" {
					t.Errorf("fks %+v", orders.ForeignKeys)
				}
				if len(orders.Checks) != 1 || !strings.Contains(orders.Checks[0].Expr, "total") {
					t.Errorf("checks %+v", orders.Checks)
				}
				if v := m.Relation(schema.QualifiedName{Schema: "app", Name: "big_orders"}); v.Kind != schema.KindView || v.RowEstimate != nil {
					t.Errorf("view %+v", v)
				}
				// A SELECT-only user, the recommended setup, sees the same keys and actions.
				rp := p
				rp.User = "reader"
				rdb, err := Open(rp, "reader-test", 30*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer rdb.Close()
				rm, err := Introspect(ctx, rdb, nil, "app")
				if err != nil {
					t.Fatal(err)
				}
				rfk := rm.Relation(schema.QualifiedName{Schema: "app", Name: "orders"}).ForeignKeys
				if len(rfk) != 1 || rfk[0].OnDelete != "cascade" || rfk[0].OnUpdate != orders.ForeignKeys[0].OnUpdate {
					t.Errorf("read-only user's fks %+v, root's %+v", rfk, orders.ForeignKeys)
				}
				ns, err := Namespaces(ctx, db)
				if err != nil || len(ns) != 1 || ns[0].Name != "app" {
					t.Errorf("namespaces %v %v", ns, err)
				}
			})

			t.Run("run", func(t *testing.T) {
				res, err := Run(ctx, db, "SELECT u.email, o.total, o.big, u.avatar FROM users u JOIN orders o ON o.user_id = u.id ORDER BY o.id;", 10, false, 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, v := range res.Rows[0] {
					got = append(got, string(v))
				}
				if want := `"a@x.io","150.50","18446744073709551615","\\x00ff"`; strings.Join(got, ",") != want {
					t.Errorf("row %s, want %s", strings.Join(got, ","), want)
				}
				if res.Plan == nil || len(res.Plan.Children) != 2 {
					t.Errorf("plan %+v", res.Plan)
				}
			})

			t.Run("read only", func(t *testing.T) {
				// Check would refuse these; the READ ONLY transaction must refuse them on its own.
				for _, q := range []string{"DELETE FROM users", "UPDATE orders SET total = 0", "INSERT INTO users (email) VALUES ('c@z')"} {
					if _, err := Run(ctx, db, q, 10, false, 5*time.Second); err == nil {
						t.Errorf("%s succeeded", q)
					}
				}
				var n int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 2 {
					t.Fatalf("users = %d, %v", n, err)
				}
			})

			t.Run("timeout", func(t *testing.T) {
				start := time.Now()
				_, err := Run(ctx, db, `SELECT count(*) FROM information_schema.COLUMNS a, information_schema.COLUMNS b, information_schema.COLUMNS c`, 10, false, time.Second)
				if err == nil || time.Since(start) > 20*time.Second {
					t.Fatalf("err %v after %s", err, time.Since(start))
				}
			})

			t.Run("test", func(t *testing.T) {
				res := Test(ctx, p, testPassword)
				if !res.OK || res.ServerVersionNum != srv.Num || !slices.ContainsFunc(res.WritePrivileges, func(s string) bool {
					// MariaDB says ALL PRIVILEGES; MySQL 8 lists each privilege.
					return strings.HasPrefix(s, "ALL PRIVILEGES") || strings.HasPrefix(s, "INSERT ")
				}) {
					t.Errorf("test %+v", res)
				}
				if flavor == "mariadb" && res.Engine != conn.EngineMariaDB {
					t.Errorf("engine %s", res.Engine)
				}
				bad := Test(ctx, p, "wrong")
				if bad.OK || bad.Error == nil || bad.Error.Kind != "auth" {
					t.Errorf("wrong password %+v %+v", bad, bad.Error)
				}
			})
		})
	}
}

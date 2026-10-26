package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// fakeSource stands in for the database.
type fakeSource struct {
	mu        sync.Mutex
	err       error
	passwords []string
	forgotten []string
	closed    bool
}

func (f *fakeSource) record(password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passwords = append(f.passwords, password)
	return f.err
}

func (f *fakeSource) Namespaces(_ context.Context, _ conn.Profile, password string) ([]introspect.Namespace, error) {
	if err := f.record(password); err != nil {
		return nil, err
	}
	return []introspect.Namespace{{Name: "public", RelationCount: 2}}, nil
}

func (f *fakeSource) Introspect(_ context.Context, p conn.Profile, password string) (*schema.Model, error) {
	if err := f.record(password); err != nil {
		return nil, err
	}
	return &schema.Model{
		ServerVersion: 170002, Schemas: []string{"public"}, Enums: []schema.Enum{},
		Relations: []schema.Relation{
			{Schema: "public", Name: "posts", Kind: schema.KindTable, PrimaryKey: []string{"id"},
				Columns: []schema.Column{{Name: "id", Type: "bigint"}, {Name: "user_id", Type: "bigint"}}},
			{Schema: "public", Name: "users", Kind: schema.KindTable, PrimaryKey: []string{"id"},
				Columns: []schema.Column{{Name: "id", Type: "bigint"}}},
		},
	}, nil
}

func (f *fakeSource) Forget(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, id)
}

func (f *fakeSource) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

const postsKey = "public.posts(user_id)->public.users(id)"

func fakeEnv(t *testing.T) (*env, *fakeSource) {
	f := &fakeSource{}
	e := keyringEnv(t)
	e.useSource(f)
	return e, f
}

// useSource rebuilds the handler around src.
func (e *env) useSource(src SchemaSource) {
	e.t.Helper()
	a, err := New(Config{Port: testPort, Token: testToken, Store: e.store, Secrets: e.vault, Schema: src})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(a.Close)
	e.h, e.agent = a, a
}

func (e *env) createProfile(body string) conn.Profile {
	e.t.Helper()
	w := e.do("POST", "/api/profiles", body)
	expect(e.t, w, 201, "")
	return decodeBody[conn.Profile](e.t, w)
}

func postsFK(t *testing.T, m schema.Model) *schema.ForeignKey {
	t.Helper()
	posts := m.Relation(schema.QualifiedName{Schema: "public", Name: "posts"})
	if posts == nil {
		t.Fatal("posts missing")
	}
	for i := range posts.ForeignKeys {
		if posts.ForeignKeys[i].Name == postsKey {
			return &posts.ForeignKeys[i]
		}
	}
	return nil
}

func TestSchemaAndInferredFKs(t *testing.T) {
	e, f := fakeEnv(t)
	p := e.createProfile(`{"name":"a","host":"h","database":"d","user":"u","env":"local","password":"` + testPass + `"}`)
	base := "/api/profiles/" + p.ID

	w := e.do("GET", base+"/namespaces", "")
	expect(t, w, 200, "")
	if got := w.Body.String(); strings.TrimSpace(got) != `[{"name":"public","relationCount":2}]` {
		t.Fatalf("namespaces %s", got)
	}

	schemaFK := func() *schema.ForeignKey {
		w := e.do("GET", base+"/schema", "")
		expect(t, w, 200, "")
		return postsFK(t, decodeBody[schema.Model](t, w))
	}
	if fk := schemaFK(); fk == nil || !fk.Inferred || fk.Confidence != 0.6 || fk.Validated {
		t.Fatalf("inferred FK %+v", fk)
	}
	if f.passwords[0] != testPass || len(f.passwords) != 2 {
		t.Fatalf("source got passwords %q", f.passwords)
	}

	put := func(status string) {
		t.Helper()
		expect(t, e.do("PUT", base+"/inferred-fks", `{"key":"`+postsKey+`","status":"`+status+`"}`), 204, "")
	}
	put("rejected")
	if fk := schemaFK(); fk != nil {
		t.Fatalf("rejected FK still returned: %+v", fk)
	}
	put("accepted")
	if fk := schemaFK(); fk == nil || !fk.Inferred || fk.Confidence != 1 {
		t.Fatalf("accepted FK %+v", fk)
	}
	put("unset")
	if fk := schemaFK(); fk == nil || fk.Confidence != 0.6 {
		t.Fatalf("unset FK %+v", fk)
	}

	for name, body := range map[string]string{
		"bad status":    `{"key":"` + postsKey + `","status":"maybe"}`,
		"no status":     `{"key":"` + postsKey + `"}`,
		"no key":        `{"status":"accepted"}`,
		"malformed key": `{"key":"public.posts","status":"accepted"}`,
		"long key":      `{"key":"` + strings.Repeat("x", maxKeyBytes) + `)->a(b)","status":"accepted"}`,
		"unknown field": `{"key":"` + postsKey + `","status":"accepted","confidence":1}`,
		"not json":      `{`,
	} {
		t.Run(name, func(t *testing.T) { expect(t, e.do("PUT", base+"/inferred-fks", body), 400, "validation") })
	}
}

func TestSchemaRoutesUnknownProfile(t *testing.T) {
	e, _ := fakeEnv(t)
	base := "/api/profiles/missing"
	expect(t, e.do("GET", base+"/namespaces", ""), 404, "not_found")
	expect(t, e.do("GET", base+"/schema", ""), 404, "not_found")
	expect(t, e.do("PUT", base+"/inferred-fks", `{"key":"`+postsKey+`","status":"accepted"}`), 404, "not_found")
	expect(t, e.do("PUT", base+"/inferred-fks", `{"key":"`+postsKey+`","status":"unset"}`), 404, "not_found")
	expect(t, e.do("GET", base+"/layout?key=x", ""), 404, "not_found")
	expect(t, e.do("PUT", base+"/layout", `{"key":"x","positions":{},"collapsed":[]}`), 404, "not_found")
	expect(t, e.do("POST", base+"/schema", ""), 404, "not_found")
}

func TestSchemaUpstreamErrors(t *testing.T) {
	e, f := fakeEnv(t)
	p := e.createProfile(validBody)
	for _, c := range []struct {
		err          error
		status       int
		code, detail string
	}{
		{&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, 504, "timeout", ""},
		{&pgconn.PgError{Code: "42501", Message: "permission denied for table pg_class"}, 502, "upstream", "database"},
		{&pgconn.PgError{Code: "28P01", Message: "password authentication failed"}, 502, "upstream", "auth"},
		{context.DeadlineExceeded, 502, "upstream", "timeout"},
		{errors.New("boom"), 502, "upstream", "other"},
		{errClosed, 503, "internal", ""},
	} {
		f.err = c.err
		for _, route := range []string{"/namespaces", "/schema"} {
			w := e.do("GET", "/api/profiles/"+p.ID+route, "")
			expect(t, w, c.status, c.code)
			if got := decodeBody[struct{ Error apiError }](t, w).Error.Detail; got != c.detail {
				t.Errorf("%v %s: detail %q, want %q", c.err, route, got, c.detail)
			}
		}
	}
}

func TestSchemaClosedPort(t *testing.T) {
	e := keyringEnv(t) // real pools
	p := e.createProfile(`{"name":"t","host":"127.0.0.1","port":` + strconv.Itoa(closedPort(t)) +
		`,"database":"d","user":"u","env":"local","sslMode":"disable","password":"` + testPass + `"}`)
	for _, route := range []string{"/namespaces", "/schema"} {
		w := e.do("GET", "/api/profiles/"+p.ID+route, "")
		expect(t, w, 502, "upstream")
		if d := decodeBody[struct{ Error apiError }](t, w).Error.Detail; d != "network" {
			t.Fatalf("%s detail %q, want network", route, d)
		}
	}
	missingCert := `{"name":"tls","host":"db.example","database":"d","user":"u","env":"local","sslMode":"verify-full","sslRootCert":"` +
		filepath.ToSlash(filepath.Join(t.TempDir(), "missing.pem")) + `"}`
	p = e.createProfile(missingCert)
	w := e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 502, "upstream")
	if d := decodeBody[struct{ Error apiError }](t, w).Error.Detail; d != "tls" {
		t.Fatalf("detail %q, want tls", d)
	}
}

func TestSchemaLockedVault(t *testing.T) {
	e := lockedEnv(t, "pass")
	f := &fakeSource{}
	e.useSource(f)
	base, _ := conn.ProfileInput{Name: "plain", Host: "h", Database: "d", User: "u", Env: conn.EnvLocal}.Profile()
	plain, _ := e.store.CreateProfile(t.Context(), base)
	base.Name, base.HasPassword = "secret", true
	withPass, _ := e.store.CreateProfile(t.Context(), base)

	expect(t, e.do("GET", "/api/profiles/"+withPass.ID+"/namespaces", ""), 423, "secrets_locked")
	expect(t, e.do("GET", "/api/profiles/"+withPass.ID+"/schema", ""), 423, "secrets_locked")
	expect(t, e.do("GET", "/api/profiles/"+plain.ID+"/namespaces", ""), 200, "")
	expect(t, e.do("GET", "/api/profiles/"+plain.ID+"/schema", ""), 200, "")
	if !slices.Equal(f.passwords, []string{"", ""}) {
		t.Fatalf("source calls %q", f.passwords)
	}
	// Decisions and layouts hold no secrets, so they work while locked.
	expect(t, e.do("PUT", "/api/profiles/"+withPass.ID+"/inferred-fks", `{"key":"`+postsKey+`","status":"rejected"}`), 204, "")
	expect(t, e.do("PUT", "/api/profiles/"+withPass.ID+"/layout", `{"key":"k","positions":{},"collapsed":[]}`), 204, "")
}

func TestLayoutRoutes(t *testing.T) {
	e, _ := fakeEnv(t)
	p := e.createProfile(validBody)
	base := "/api/profiles/" + p.ID + "/layout"

	expect(t, e.do("GET", base, ""), 400, "validation")
	expect(t, e.do("GET", base+"?key=app%2Cpublic", ""), 404, "not_found")
	expect(t, e.do("PUT", base, `{"key":"app,public","positions":{"public.users":{"x":10.5,"y":-3}},"collapsed":["public.audit"]}`), 204, "")
	w := e.do("GET", base+"?key=app%2Cpublic", "")
	expect(t, w, 200, "")
	var got map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &got)
	if string(got["key"]) != `"app,public"` || string(got["positions"]) != `{"public.users":{"x":10.5,"y":-3}}` ||
		string(got["collapsed"]) != `["public.audit"]` || len(got["updatedAt"]) < 10 || len(got) != 4 {
		t.Fatalf("layout %s", w.Body.String())
	}
	// Omitted positions and collapsed come back as empty values.
	expect(t, e.do("PUT", base, `{"key":"app,public"}`), 204, "")
	if b := e.do("GET", base+"?key=app%2Cpublic", "").Body.String(); !strings.Contains(b, `"positions":{}`) || !strings.Contains(b, `"collapsed":[]`) {
		t.Fatalf("emptied layout %s", b)
	}
	expect(t, e.do("GET", base+"?key=other", ""), 404, "not_found")

	for name, body := range map[string]string{
		"no key":        `{"positions":{}}`,
		"bad position":  `{"key":"k","positions":{"a":{"x":"1"}}}`,
		"unknown field": `{"key":"k","zoom":2}`,
		"not json":      `[`,
	} {
		t.Run(name, func(t *testing.T) { expect(t, e.do("PUT", base, body), 400, "validation") })
	}
	t.Run("too large", func(t *testing.T) {
		big := `{"key":"k","collapsed":["` + strings.Repeat("x", maxBodyBytes) + `"]}`
		expect(t, e.do("PUT", base, big), 413, "validation")
	})

	// Layouts and decisions go when the profile goes.
	expect(t, e.do("DELETE", "/api/profiles/"+p.ID, ""), 204, "")
	expect(t, e.do("GET", base+"?key=app%2Cpublic", ""), 404, "not_found")
}

func TestSampleSchema(t *testing.T) {
	e, f := fakeEnv(t)
	w := e.do("GET", "/api/sample/schema", "")
	expect(t, w, 200, "")
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	m := decodeBody[schema.Model](t, w)
	inferred := 0
	for _, r := range m.Relations {
		for _, fk := range r.ForeignKeys {
			if fk.Inferred {
				inferred++
			}
		}
	}
	if m.ServerVersion != 170000 || len(m.Relations) < 40 || inferred != 4 {
		t.Fatalf("sample: version %d, %d relations, %d inferred FKs", m.ServerVersion, len(m.Relations), inferred)
	}
	if len(f.passwords) != 0 {
		t.Fatal("sample touched the schema source")
	}
}

func TestProfileChangesForgetPools(t *testing.T) {
	e, f := fakeEnv(t)
	p := e.createProfile(validBody)
	expect(t, e.do("PUT", "/api/profiles/"+p.ID, validBody), 200, "")
	expect(t, e.do("PUT", "/api/profiles/missing", validBody), 404, "not_found")
	expect(t, e.do("DELETE", "/api/profiles/"+p.ID, ""), 204, "")
	if !slices.Equal(f.forgotten, []string{p.ID, p.ID}) {
		t.Fatalf("forgotten %v", f.forgotten)
	}
	e.agent.Close()
	if !f.closed {
		t.Fatal("Agent.Close did not close the source")
	}
}

func TestPoolCache(t *testing.T) {
	ps := newPools()
	p, _ := conn.ProfileInput{Name: "a", Host: "db.example", Database: "d", User: "u", Env: conn.EnvLocal}.Profile()
	p.ID = "one"
	a, err := ps.get(t.Context(), p, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := ps.get(t.Context(), p, "pw"); b != a {
		t.Fatal("same settings did not reuse the pool")
	}
	if a.Config().MaxConns != 2 {
		t.Fatalf("MaxConns %d", a.Config().MaxConns)
	}
	p.StatementTimeoutMs = 5 // not a connection setting
	if b, _ := ps.get(t.Context(), p, "pw"); b != a {
		t.Fatal("statement timeout change replaced the pool")
	}
	b, _ := ps.get(t.Context(), p, "other")
	if b == a || ps.open() != 1 {
		t.Fatalf("password change: new pool %v, open %d", b != a, ps.open())
	}
	p2 := p
	p2.ID = "two"
	ps.get(t.Context(), p2, "pw")
	if ps.open() != 2 {
		t.Fatalf("open %d", ps.open())
	}
	ps.Forget("one")
	ps.Forget("never")
	if ps.open() != 1 {
		t.Fatalf("after forget: open %d", ps.open())
	}
	ps.Close()
	if _, err := ps.get(t.Context(), p, "pw"); !errors.Is(err, errClosed) || ps.open() != 0 {
		t.Fatalf("after close: %v, open %d", err, ps.open())
	}
}

// TestSchemaEndToEnd runs the routes against PostgreSQL 18 with the
// introspection fixture and its SELECT-only role.
func TestSchemaEndToEnd(t *testing.T) {
	srv := pgtest.Servers(t, "18")[0]
	d := srv.NewDB(t)
	reader := d.Role("reader")
	fixture, err := os.ReadFile("../introspect/testdata/fixture.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := regexp.MustCompile(`\breader\b`).ReplaceAllString(string(fixture), reader)
	d.Exec(t, strings.Replace(sql, "PASSWORD 'r'", "PASSWORD '"+testPass+"'", 1))

	e := keyringEnv(t)
	prof := d.As(t, reader)
	body := func(timeoutMs int) string {
		b, _ := json.Marshal(map[string]any{
			"name": "e2e", "host": prof.Host, "port": prof.Port, "database": prof.Database, "user": reader,
			"sslMode": prof.SSLMode, "env": "local", "statementTimeoutMs": timeoutMs, "password": testPass,
		})
		return string(b)
	}
	p := e.createProfile(body(10000))
	base := "/api/profiles/" + p.ID
	pools := e.agent.source.(*pools)

	w := e.do("GET", base+"/namespaces", "")
	expect(t, w, 200, "")
	if got := strings.TrimSpace(w.Body.String()); got != `[{"name":"app","relationCount":5},{"name":"public","relationCount":2}]` {
		t.Fatalf("namespaces %s", got)
	}

	const legacyKey = "public.legacy_posts(legacy_user_id)->public.legacy_users(id)"
	legacyFK := func() *schema.ForeignKey {
		t.Helper()
		w := e.do("GET", base+"/schema", "")
		expect(t, w, 200, "")
		m := decodeBody[schema.Model](t, w)
		if !slices.Equal(m.Schemas, []string{"app", "public"}) || m.Relation(schema.QualifiedName{Schema: "app", Name: "events"}) == nil {
			t.Fatalf("schema %v", m.Schemas)
		}
		for _, fk := range m.Relation(schema.QualifiedName{Schema: "public", Name: "legacy_posts"}).ForeignKeys {
			if fk.Name == legacyKey {
				return &fk
			}
		}
		return nil
	}
	if fk := legacyFK(); fk == nil || !fk.Inferred || fk.Confidence != 0.6 {
		t.Fatalf("inferred legacy FK %+v", fk)
	}
	expect(t, e.do("PUT", base+"/inferred-fks", `{"key":"`+legacyKey+`","status":"rejected"}`), 204, "")
	if fk := legacyFK(); fk != nil {
		t.Fatalf("rejected FK returned %+v", fk)
	}
	if pools.open() != 1 {
		t.Fatalf("open pools %d, want 1 reused across requests", pools.open())
	}

	// A short statement timeout with a catalog locked gives 504.
	expect(t, e.do("PUT", base, body(300)), 200, "")
	if pools.open() != 0 {
		t.Fatal("profile update did not drop the pool")
	}
	blocker, err := d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(t.Context(), "LOCK TABLE pg_catalog.pg_description IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	expect(t, e.do("GET", base+"/schema", ""), 504, "timeout")
	blocker.Rollback(t.Context())
	expect(t, e.do("GET", base+"/schema", ""), 200, "")

	// A wrong password is an upstream auth failure.
	wrong := strings.Replace(body(10000), testPass, "wrong-password", 1)
	expect(t, e.do("PUT", base, wrong), 200, "")
	w = e.do("GET", base+"/schema", "")
	expect(t, w, 502, "upstream")
	if d := decodeBody[struct{ Error apiError }](t, w).Error.Detail; d != "auth" {
		t.Fatalf("detail %q, want auth", d)
	}
	e.agent.Close()
	if pools.open() != 0 {
		t.Fatal("Close left pools open")
	}
}

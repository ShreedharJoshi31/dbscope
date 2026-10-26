package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/zalando/go-keyring"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/secrets"
	"github.com/dbscope/dbscope/internal/store"
)

const (
	testPort  = 4555
	testToken = "test-token"
	testPass  = "pg-password-do-not-leak"
)

type env struct {
	t     *testing.T
	h     http.Handler
	store *store.Store
	vault *secrets.Vault
}

func newEnv(t *testing.T, vault *secrets.Vault, dir string) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "dbscope.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, err := New(Config{Port: testPort, Token: testToken, Store: st, Secrets: vault})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, h: h, store: st, vault: vault}
}

// keyringEnv has an unlocked vault backed by the mock keyring.
func keyringEnv(t *testing.T) *env {
	keyring.MockInit()
	dir := t.TempDir()
	v, err := secrets.Open(filepath.Join(dir, "secrets.age"))
	if err != nil {
		t.Fatal(err)
	}
	return newEnv(t, v, dir)
}

// lockedEnv has a passphrase vault (no keyring) whose file was written with a
// cheap scrypt work factor so unlock attempts stay fast.
func lockedEnv(t *testing.T, passphrase string) *env {
	keyring.MockInitWithError(errors.New("no keyring"))
	t.Cleanup(keyring.MockInit)
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.age")
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	r.SetWorkFactor(10)
	var buf bytes.Buffer
	w, _ := age.Encrypt(&buf, r)
	w.Write([]byte(`{}`))
	w.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := secrets.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return newEnv(t, v, dir)
}

func (e *env) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:" + strconv.Itoa(testPort)
	r.Header.Set(TokenHeader, testToken)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), testPass) {
		e.t.Fatalf("%s %s: response contains the password: %s", method, path, w.Body.String())
	}
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d; body %s", w.Code, status, w.Body.String())
	}
	if code == "" {
		return
	}
	var env struct {
		Error apiError `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Error.Code != code || env.Error.Message == "" {
		t.Fatalf("want error code %q with a message, got %s", code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

func closedPort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

const validBody = `{"name":"Orders","host":"db.example","database":"app","user":"reader","env":"staging"}`

func TestNewRejectsBadConfig(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "db"))
	defer st.Close()
	v, _ := secrets.Open(filepath.Join(dir, "s.age"))
	for _, c := range []Config{
		{Port: 4555, Token: "", Store: st, Secrets: v},
		{Port: 0, Token: "t", Store: st, Secrets: v},
		{Port: 4555, Token: "t", Secrets: v},
		{Port: 4555, Token: "t", Store: st},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) should fail", c)
		}
	}
}

func TestHealth(t *testing.T) {
	e := keyringEnv(t)
	w := e.do("GET", "/api/health", "")
	expect(t, w, 200, "")
	h := decodeBody[map[string]string](t, w)
	if h["version"] != "dev" || h["os"] == "" || h["arch"] == "" || len(h) != 3 {
		t.Fatalf("health %v", h)
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("guard headers missing")
	}
}

func TestAPIRequiresToken(t *testing.T) {
	e := keyringEnv(t)
	for _, path := range []string{"/api/profiles", "/x/../api/profiles", "//api/profiles"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:4555"
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatalf("%s served without token: %s", path, w.Body.String())
		}
	}
}

func TestUnknownAPIRoute(t *testing.T) {
	e := keyringEnv(t)
	expect(t, e.do("GET", "/api/nope", ""), 404, "not_found")
	expect(t, e.do("PATCH", "/api/profiles", "{}"), 404, "not_found")
}

func TestCreateProfileDefaults(t *testing.T) {
	e := keyringEnv(t)
	w := e.do("POST", "/api/profiles", validBody)
	expect(t, w, 201, "")
	p := decodeBody[map[string]any](t, w)
	want := map[string]any{
		"name": "Orders", "port": 5432.0, "sslMode": "prefer", "summaryMode": "profile",
		"maxRowsToLLM": 50.0, "statementTimeoutMs": 30000.0, "hasPassword": false, "env": "staging",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v, want %v", k, p[k], v)
		}
	}
	if s, ok := p["schemas"].([]any); !ok || len(s) != 0 {
		t.Errorf("schemas = %v, want []", p["schemas"])
	}
	if id, _ := p["id"].(string); len(id) != 26 {
		t.Errorf("id %v is not a ULID", p["id"])
	}
	if _, ok := p["password"]; ok {
		t.Error("password field present")
	}

	w = e.do("POST", "/api/profiles", `{"name":"Prod","host":"db.example","database":"app","user":"reader","env":"prod-replica"}`)
	expect(t, w, 201, "")
	if p := decodeBody[conn.Profile](t, w); p.StatementTimeoutMs != 10000 {
		t.Fatalf("prod-replica timeout %d", p.StatementTimeoutMs)
	}
}

func TestCreateProfileValidation(t *testing.T) {
	e := keyringEnv(t)
	for name, body := range map[string]string{
		"empty name":      `{"name":"","host":"h","database":"d","user":"u","env":"local"}`,
		"long name":       `{"name":"` + strings.Repeat("x", 81) + `","host":"h","database":"d","user":"u","env":"local"}`,
		"port zero":       `{"name":"n","host":"h","port":0,"database":"d","user":"u","env":"local"}`,
		"port too big":    `{"name":"n","host":"h","port":65536,"database":"d","user":"u","env":"local"}`,
		"missing env":     `{"name":"n","host":"h","database":"d","user":"u"}`,
		"bad env":         `{"name":"n","host":"h","database":"d","user":"u","env":"prod"}`,
		"bad sslMode":     `{"name":"n","host":"h","database":"d","user":"u","env":"local","sslMode":"allow"}`,
		"bad summary":     `{"name":"n","host":"h","database":"d","user":"u","env":"local","summaryMode":"all"}`,
		"rows zero":       `{"name":"n","host":"h","database":"d","user":"u","env":"local","maxRowsToLLM":0}`,
		"rows too many":   `{"name":"n","host":"h","database":"d","user":"u","env":"local","maxRowsToLLM":501}`,
		"bad timeout":     `{"name":"n","host":"h","database":"d","user":"u","env":"local","statementTimeoutMs":0}`,
		"missing host":    `{"name":"n","database":"d","user":"u","env":"local"}`,
		"unknown field":   `{"name":"n","host":"h","database":"d","user":"u","env":"local","hasPassword":true}`,
		"wrong type":      `{"name":"n","host":"h","port":"5432","database":"d","user":"u","env":"local"}`,
		"not json":        `{name:`,
		"empty body":      ``,
		"trailing value":  validBody + `{}`,
		"array body":      `[]`,
		"multi-host host": `{"name":"n","host":"a,b","database":"d","user":"u","env":"local"}`,
	} {
		t.Run(name, func(t *testing.T) { expect(t, e.do("POST", "/api/profiles", body), 400, "validation") })
	}
	t.Run("too large", func(t *testing.T) {
		big := `{"name":"` + strings.Repeat("x", maxBodyBytes) + `"}`
		expect(t, e.do("POST", "/api/profiles", big), 413, "validation")
	})
}

func TestProfileLifecycle(t *testing.T) {
	e := keyringEnv(t)
	withPass := `{"name":"Orders","host":"db.example","database":"app","user":"reader","env":"staging","password":"` + testPass + `"}`
	w := e.do("POST", "/api/profiles", withPass)
	expect(t, w, 201, "")
	p := decodeBody[conn.Profile](t, w)
	if !p.HasPassword {
		t.Fatal("hasPassword false after create with password")
	}
	if s, ok, _ := e.vault.Get(p.ID); !ok || s != testPass {
		t.Fatal("password not stored in vault")
	}

	expect(t, e.do("POST", "/api/profiles", withPass), 409, "conflict")
	expect(t, e.do("POST", "/api/profiles", strings.Replace(withPass, "Orders", "ORDERS", 1)), 409, "conflict")
	expect(t, e.do("POST", "/api/profiles", strings.Replace(validBody, "Orders", "Analytics", 1)), 201, "")

	list := decodeBody[[]conn.Profile](t, e.do("GET", "/api/profiles", ""))
	if len(list) != 2 || list[0].Name != "Analytics" || list[1].Name != "Orders" {
		t.Fatalf("list %+v", list)
	}

	url := "/api/profiles/" + p.ID
	// Omitting password keeps it.
	w = e.do("PUT", url, strings.Replace(validBody, `"user":"reader"`, `"user":"other"`, 1))
	expect(t, w, 200, "")
	up := decodeBody[conn.Profile](t, w)
	if up.User != "other" || !up.HasPassword || !up.CreatedAt.Equal(p.CreatedAt) || up.ID != p.ID {
		t.Fatalf("update result %+v", up)
	}
	if s, ok, _ := e.vault.Get(p.ID); !ok || s != testPass {
		t.Fatal("password lost on update without password")
	}
	// A new password replaces it.
	expect(t, e.do("PUT", url, strings.Replace(withPass, testPass, testPass+"2", 1)), 200, "")
	if s, _, _ := e.vault.Get(p.ID); s != testPass+"2" {
		t.Fatal("password not replaced")
	}
	// "" clears it.
	w = e.do("PUT", url, strings.Replace(withPass, testPass, "", 1))
	expect(t, w, 200, "")
	if decodeBody[conn.Profile](t, w).HasPassword {
		t.Fatal("hasPassword still true after clearing")
	}
	if _, ok, _ := e.vault.Get(p.ID); ok {
		t.Fatal("password still in vault after clearing")
	}

	expect(t, e.do("PUT", url, strings.Replace(validBody, "Orders", "Analytics", 1)), 409, "conflict")
	expect(t, e.do("PUT", url, `{"name":""}`), 400, "validation")
	expect(t, e.do("PUT", "/api/profiles/missing", validBody), 404, "not_found")

	expect(t, e.do("PUT", url, withPass), 200, "")
	w = e.do("DELETE", url, "")
	expect(t, w, 204, "")
	if _, ok, _ := e.vault.Get(p.ID); ok {
		t.Fatal("secret not deleted with profile")
	}
	expect(t, e.do("DELETE", url, ""), 404, "not_found")
}

func TestParseURL(t *testing.T) {
	e := keyringEnv(t)
	w := e.do("POST", "/api/connection-url/parse", `{"url":"postgres://u:p%40ss@h:6543/db?sslmode=require"}`)
	expect(t, w, 200, "")
	got := decodeBody[map[string]any](t, w)
	want := map[string]any{"host": "h", "port": 6543.0, "database": "db", "user": "u", "password": "p@ss", "sslMode": "require"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	expect(t, e.do("POST", "/api/connection-url/parse", `{"url":"mysql://u@h/db"}`), 400, "validation")
	expect(t, e.do("POST", "/api/connection-url/parse", `{"url":""}`), 400, "validation")
	expect(t, e.do("POST", "/api/connection-url/parse", `{"uri":"postgres://h/db"}`), 400, "validation")
}

func TestConnectionTestEndpoints(t *testing.T) {
	e := keyringEnv(t)
	body := `{"name":"t","host":"127.0.0.1","port":` + strconv.Itoa(closedPort(t)) +
		`,"database":"d","user":"u","env":"local","sslMode":"disable","password":"` + testPass + `"}`

	w := e.do("POST", "/api/profiles/test", body)
	expect(t, w, 200, "")
	res := decodeBody[conn.TestResult](t, w)
	if res.OK || res.Error == nil || res.Error.Kind != "network" || res.Warnings == nil {
		t.Fatalf("unsaved test result %+v", res)
	}
	expect(t, e.do("POST", "/api/profiles/test", `{"name":"t"}`), 400, "validation")

	w = e.do("POST", "/api/profiles", body)
	expect(t, w, 201, "")
	id := decodeBody[conn.Profile](t, w).ID
	w = e.do("POST", "/api/profiles/"+id+"/test", "")
	expect(t, w, 200, "")
	if res := decodeBody[conn.TestResult](t, w); res.Error == nil || res.Error.Kind != "network" {
		t.Fatalf("saved test result %+v", res)
	}
	expect(t, e.do("POST", "/api/profiles/missing/test", ""), 404, "not_found")
}

func TestSecretsKeyringStatus(t *testing.T) {
	e := keyringEnv(t)
	w := e.do("GET", "/api/secrets/status", "")
	expect(t, w, 200, "")
	if st := decodeBody[secrets.Status](t, w); st != (secrets.Status{Backend: secrets.BackendKeyring, Locked: false}) {
		t.Fatalf("status %+v", st)
	}
	expect(t, e.do("POST", "/api/secrets/unlock", `{"passphrase":"anything"}`), 204, "")
}

func TestLockedVault(t *testing.T) {
	const passphrase = "correct horse"
	e := lockedEnv(t, passphrase)

	if st := decodeBody[secrets.Status](t, e.do("GET", "/api/secrets/status", "")); st != (secrets.Status{Backend: secrets.BackendPassphrase, Locked: true}) {
		t.Fatalf("status %+v", st)
	}
	// Profiles created out of band, since the API refuses while locked.
	base, _ := conn.ProfileInput{Name: "plain", Host: "h", Database: "d", User: "u", Env: conn.EnvLocal}.Profile()
	plain, _ := e.store.CreateProfile(t.Context(), base)
	base.Name, base.HasPassword = "secret", true
	withPass, _ := e.store.CreateProfile(t.Context(), base)

	expect(t, e.do("GET", "/api/profiles", ""), 200, "")
	expect(t, e.do("POST", "/api/profiles", validBody), 423, "secrets_locked")
	expect(t, e.do("PUT", "/api/profiles/"+plain.ID, validBody), 423, "secrets_locked")
	expect(t, e.do("POST", "/api/profiles/"+plain.ID+"/test", ""), 423, "secrets_locked")
	expect(t, e.do("DELETE", "/api/profiles/"+withPass.ID, ""), 423, "secrets_locked")
	expect(t, e.do("DELETE", "/api/profiles/"+plain.ID, ""), 204, "")

	expect(t, e.do("POST", "/api/secrets/unlock", `{"passphrase":"wrong"}`), 401, "unauthorized")
	expect(t, e.do("POST", "/api/secrets/unlock", `{"passphrase":""}`), 400, "validation")
	expect(t, e.do("POST", "/api/secrets/unlock", `{}`), 400, "validation")
	expect(t, e.do("POST", "/api/secrets/unlock", `{"passphrase":"x","extra":1}`), 400, "validation")
	expect(t, e.do("POST", "/api/secrets/unlock", `{"passphrase":"`+passphrase+`"}`), 204, "")
	if decodeBody[secrets.Status](t, e.do("GET", "/api/secrets/status", "")).Locked {
		t.Fatal("still locked after unlock")
	}
	expect(t, e.do("DELETE", "/api/profiles/"+withPass.ID, ""), 204, "")
}

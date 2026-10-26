package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuard(t *testing.T) {
	const tok = "secret-token"
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := guard{port: "4555", token: []byte(tok)}.wrap(ok)

	cases := []struct {
		name   string
		path   string
		host   string
		origin string
		token  string
		want   int
	}{
		{"api with token", "/api/health", "127.0.0.1:4555", "", tok, 204},
		{"api localhost host", "/api/health", "localhost:4555", "http://localhost:4555", tok, 204},
		{"api ipv6 loopback", "/api/health", "[::1]:4555", "", tok, 204},
		{"api missing token", "/api/health", "127.0.0.1:4555", "", "", 401},
		{"api wrong token", "/api/health", "127.0.0.1:4555", "", "nope", 401},
		{"token prefix only", "/api/health", "127.0.0.1:4555", "", tok[:5], 401},
		{"dns rebinding host", "/api/health", "evil.example:4555", "", tok, 403},
		{"wrong port host", "/api/health", "127.0.0.1:9999", "", tok, 403},
		{"host without port", "/api/health", "127.0.0.1", "", tok, 403},
		{"foreign origin", "/api/health", "127.0.0.1:4555", "https://evil.example", tok, 403},
		{"https origin on loopback", "/api/health", "127.0.0.1:4555", "https://127.0.0.1:4555", tok, 403},
		{"null origin", "/api/health", "127.0.0.1:4555", "null", tok, 403},
		{"static asset needs no token", "/index.html", "127.0.0.1:4555", "", "", 204},
		{"static asset still checks host", "/index.html", "evil.example:4555", "", "", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", c.path, nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.token != "" {
				r.Header.Set(TokenHeader, c.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("got %d, want %d", w.Code, c.want)
			}
		})
	}
}

func TestGuardDevOrigin(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, c := range []struct {
		devOrigin, origin string
		want              int
	}{
		{"", "http://localhost:5173", 403},
		{"http://localhost:5173", "http://localhost:5173", 204},
		{"http://localhost:5173", "http://localhost:5174", 403},
	} {
		h := guard{port: "4555", token: []byte("t"), devOrigin: c.devOrigin}.wrap(ok)
		r := httptest.NewRequest("GET", "/api/health", nil)
		r.Host = "127.0.0.1:4555"
		r.Header.Set("Origin", c.origin)
		r.Header.Set(TokenHeader, "t")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Fatalf("devOrigin=%q origin=%q: got %d, want %d", c.devOrigin, c.origin, w.Code, c.want)
		}
	}
}

func TestNewTokenIsRandom(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if len(a) != 64 || a == b {
		t.Fatalf("bad tokens %q %q", a, b)
	}
}

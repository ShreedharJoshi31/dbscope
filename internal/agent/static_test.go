package agent

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticNotBuilt(t *testing.T) {
	h := static(nil)
	for _, path := range []string{"/", "/workbench"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "not built") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestStaticSPA(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := static(assets)
	for _, c := range []struct {
		path     string
		status   int
		contains string
	}{
		{"/", 200, "app</html>"},
		{"/assets/app.js", 200, "console.log"},
		{"/workbench/orders", 200, "app</html>"},
		{"/assets", 200, "app</html>"},
		{"/assets/missing.js", 404, ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.contains) {
			t.Errorf("%s: %d %q", c.path, w.Code, w.Body.String())
		}
	}
}

func TestStaticThroughGuard(t *testing.T) {
	e := keyringEnv(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "127.0.0.1:4555"
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "not built") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestStaticRejectsWrites(t *testing.T) {
	e := keyringEnv(t)
	r := httptest.NewRequest("POST", "/index.html", nil)
	r.Host = "127.0.0.1:4555"
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatalf("got %d", w.Code)
	}
}

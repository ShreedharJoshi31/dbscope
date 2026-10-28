package elastic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
)

// TestSigV4 connects with auth=sigv4 to a local HTTPS server and checks every
// request is signed for the region and service, without a user or API key.
func TestSigV4(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	var auth []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version": {"number": "2.19.0", "distribution": "opensearch"}}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	p := conn.Profile{Engine: conn.EngineElasticsearch, Host: u.Hostname(), Port: port, User: "ignored", SSLMode: conn.SSLRequire,
		Options: "auth=sigv4&region=eu-west-1&service=aoss"}
	c, err := Connect(p, "ignored-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.describe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") || !strings.Contains(auth[0], "/eu-west-1/aoss/aws4_request") {
		t.Errorf("authorization %q", auth)
	}
}

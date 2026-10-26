package agent

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// TokenHeader carries the per-launch secret. The browser learns the token from
// the URL fragment the CLI opens, which is never sent to the server or logged.
const TokenHeader = "X-Dbscope-Token"

func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// guard rejects requests that could come from another website: a Host other
// than the loopback address we bound (DNS rebinding), a foreign Origin (CSRF),
// or a missing/wrong token on API routes.
type guard struct {
	port  string
	token []byte
}

func (g guard) allowedHost(host string) bool {
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != g.port {
		return false
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

func (g guard) allowedOrigin(origin string) bool {
	rest, ok := strings.CutPrefix(origin, "http://")
	return ok && g.allowedHost(rest)
}

func (g guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !g.allowedOrigin(o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			got := []byte(r.Header.Get(TokenHeader))
			if subtle.ConstantTimeCompare(got, g.token) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; worker-src 'self' blob:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

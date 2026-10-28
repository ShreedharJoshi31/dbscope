package awsauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

var static = aws.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session-token")}

func TestFromOptions(t *testing.T) {
	s := FromOptions("auth=sigv4&region=eu-west-1&profile=prod")
	if s.Auth != "sigv4" || s.Region != "eu-west-1" || s.Profile != "prod" || s.Service != "es" {
		t.Errorf("settings %+v", s)
	}
	if FromOptions("").Auth != "" || FromOptions("auth=sigv4&service=aoss").Service != "aoss" {
		t.Error("defaults")
	}
}

func TestRDSToken(t *testing.T) {
	tok, err := RDSToken(context.Background(), static, "db.abc.eu-west-1.rds.amazonaws.com", 5432, "app_user")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse("https://" + tok)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "db.abc.eu-west-1.rds.amazonaws.com:5432" || q.Get("Action") != "connect" || q.Get("DBUser") != "app_user" ||
		q.Get("X-Amz-Expires") != "900" || !strings.HasPrefix(q.Get("X-Amz-Credential"), "AKIDEXAMPLE/") ||
		!strings.Contains(q.Get("X-Amz-Credential"), "/eu-west-1/rds-db/aws4_request") || q.Get("X-Amz-Security-Token") != "session-token" {
		t.Errorf("token %s", tok)
	}
}

func TestTransportSigns(t *testing.T) {
	body := `{"query":{"match_all":{}}}`
	var got *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	tr := NewTransport(http.DefaultTransport, static, "es")
	tr.now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	req, _ := http.NewRequest("POST", srv.URL+"/logs/_search?size=1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sum := sha256.Sum256([]byte(body))
	auth := got.Header.Get("Authorization")
	if gotBody != body || got.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) ||
		!strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261029/eu-west-1/es/aws4_request") ||
		!strings.Contains(auth, "SignedHeaders=") || got.Header.Get("X-Amz-Security-Token") != "session-token" ||
		got.Header.Get("X-Amz-Date") != "20261029T120000Z" {
		t.Errorf("signed request: body %q headers %v", gotBody, got.Header)
	}
}

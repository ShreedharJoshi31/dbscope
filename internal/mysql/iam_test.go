package mysql

import (
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
)

// TestRDSIAMReplacesPassword shows the token, not the stored password, is
// sent: root's right password fails once auth=rds-iam is set.
func TestRDSIAMReplacesPassword(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	p := mytest.Server(t, "mysql")
	if res := Test(t.Context(), p, mytest.Password); !res.OK {
		t.Fatalf("plain login: %+v", res.Error)
	}
	// Over plaintext caching_sha2 would RSA-encrypt the token, which is too long for that; RDS IAM needs TLS anyway.
	p.SSLMode, p.Options = conn.SSLRequire, "auth=rds-iam&region=eu-west-1"
	db, err := Open(p, mytest.Password, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(t.Context()); err == nil || !strings.Contains(err.Error(), "1045") {
		t.Fatalf("the stored password was used, or another error: %v", err)
	}
}

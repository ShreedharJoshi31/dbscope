package conn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func ptr[T any](v T) *T { return &v }

func validInput() ProfileInput {
	return ProfileInput{Name: " Orders ", Host: "db.example", Database: "app", User: "reader", Env: EnvStaging}
}

func TestProfileDefaults(t *testing.T) {
	p, err := validInput().Profile()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Orders" || p.Port != 5432 || p.SSLMode != SSLPrefer || p.SummaryMode != SummaryProfile ||
		p.MaxRowsToLLM != 50 || p.StatementTimeoutMs != 30000 || p.Schemas == nil {
		t.Fatalf("defaults %+v", p)
	}
	in := validInput()
	in.Env = EnvProdReplica
	if p, _ := in.Profile(); p.StatementTimeoutMs != 10000 {
		t.Fatalf("prod-replica timeout %d", p.StatementTimeoutMs)
	}
	in.StatementTimeoutMs = ptr(2500)
	in.Port, in.MaxRowsToLLM = ptr(6543), ptr(500)
	if p, _ := in.Profile(); p.StatementTimeoutMs != 2500 || p.Port != 6543 || p.MaxRowsToLLM != 500 {
		t.Fatalf("explicit values ignored: %+v", p)
	}
}

func TestEngines(t *testing.T) {
	in := validInput()
	p, err := in.Profile()
	if err != nil || p.Engine != EnginePostgres || !p.Capabilities.Migrations {
		t.Fatalf("default engine: %+v, %v", p, err)
	}
	dir := t.TempDir()
	sep := string(filepath.Separator)
	in.Engine, in.Path = EngineSQLite, dir+sep+"x"+sep+".."+sep+"app.db"
	p, err = in.Profile()
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != filepath.Join(dir, "app.db") || p.Host != "" || p.Port != 0 || p.User != "" || p.SSLMode != "" {
		t.Fatalf("sqlite profile kept network fields: %+v", p)
	}
	if p.Capabilities != (Capabilities{Query: true, Migrations: true}) { // SQLite
		t.Fatalf("sqlite capabilities %+v", p.Capabilities)
	}
}

func TestCostLimit(t *testing.T) {
	for env, want := range map[Env]float64{EnvLocal: 0, EnvStaging: 1e7, EnvProdReplica: 1e6} {
		in := validInput()
		in.Env = env
		p, err := in.Profile()
		if err != nil || p.MaxCost != nil || p.CostLimit() != want {
			t.Errorf("%s: limit %v (maxCost %v, err %v), want %v", env, p.CostLimit(), p.MaxCost, err, want)
		}
		for _, set := range []float64{0, 5e5} {
			in.MaxCost = ptr(set)
			if p, _ := in.Profile(); p.CostLimit() != set || *p.MaxCost != set {
				t.Errorf("%s with maxCost %v: limit %v", env, set, p.CostLimit())
			}
		}
	}
}

func TestProfileValidation(t *testing.T) {
	for name, mut := range map[string]func(*ProfileInput){
		"empty name":    func(in *ProfileInput) { in.Name = "  " },
		"long name":     func(in *ProfileInput) { in.Name = strings.Repeat("é", 81) },
		"no host":       func(in *ProfileInput) { in.Host = "" },
		"socket host":   func(in *ProfileInput) { in.Host = "/var/run/postgresql" },
		"port 0":        func(in *ProfileInput) { in.Port = ptr(0) },
		"port 65536":    func(in *ProfileInput) { in.Port = ptr(65536) },
		"no database":   func(in *ProfileInput) { in.Database = "" },
		"no user":       func(in *ProfileInput) { in.User = "" },
		"bad ssl":       func(in *ProfileInput) { in.SSLMode = "allow" },
		"no env":        func(in *ProfileInput) { in.Env = "" },
		"bad env":       func(in *ProfileInput) { in.Env = "production" },
		"empty schema":  func(in *ProfileInput) { in.Schemas = []string{"public", ""} },
		"bad summary":   func(in *ProfileInput) { in.SummaryMode = "everything" },
		"rows 0":        func(in *ProfileInput) { in.MaxRowsToLLM = ptr(0) },
		"rows 501":      func(in *ProfileInput) { in.MaxRowsToLLM = ptr(501) },
		"timeout 0":     func(in *ProfileInput) { in.StatementTimeoutMs = ptr(0) },
		"negative port": func(in *ProfileInput) { in.Port = ptr(-1) },
		"negative cost": func(in *ProfileInput) { in.MaxCost = ptr(-1.0) },
		"bad engine":    func(in *ProfileInput) { in.Engine = "oracle" },
		"postgres path": func(in *ProfileInput) { in.Path = "/tmp/app.db" },
		"sqlite relative path": func(in *ProfileInput) {
			in.Engine, in.Path = EngineSQLite, "app.db"
		},
		"sqlite no path": func(in *ProfileInput) { in.Engine = EngineSQLite },
		"sqlite password": func(in *ProfileInput) {
			in.Engine, in.Path, in.Password = EngineSQLite, filepath.Join(os.TempDir(), "app.db"), ptr("pw")
		},
	} {
		in := validInput()
		mut(&in)
		var ve *ValidationError
		if _, err := in.Profile(); !errors.As(err, &ve) {
			t.Errorf("%s: got %v, want ValidationError", name, err)
		}
	}
	in := validInput()
	in.Name = strings.Repeat("é", 80)
	if _, err := in.Profile(); err != nil {
		t.Fatalf("80 runes rejected: %v", err)
	}
}

func TestParseURL(t *testing.T) {
	for _, c := range []struct {
		in   string
		want ParsedURL
	}{
		{"postgres://u:p@h:5433/db?sslmode=require", ParsedURL{"h", 5433, "db", "u", "p", SSLRequire}},
		{"postgresql://h/db", ParsedURL{"h", 5432, "db", "", "", SSLPrefer}},
		{"postgres://us%40er:p%3Ass%2Fw%25rd@h/my%20db", ParsedURL{"h", 5432, "my db", "us@er", "p:ss/w%rd", SSLPrefer}},
		{"postgres://u@[::1]:6000/db?sslmode=verify-full&application_name=x", ParsedURL{"::1", 6000, "db", "u", "", SSLVerifyFull}},
		{"  postgres://h  ", ParsedURL{"h", 5432, "", "", "", SSLPrefer}},
	} {
		got, err := ParseURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseURL(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{
		"mysql://u@h/db", "h:5432/db", "postgres://h:0/db", "postgres://h:70000/db", "postgres://h:x/db",
		"postgres://h1,h2/db", "postgres://h/db?sslmode=allow", "postgres://u:se cret@h:bad/db",
	} {
		_, err := ParseURL(bad)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("ParseURL(%q): got %v, want ValidationError", bad, err)
		} else if strings.Contains(err.Error(), "se cret") {
			t.Errorf("error quotes input: %v", err)
		}
	}
}

func testProfile(mode SSLMode) Profile {
	p, _ := ProfileInput{Name: "n", Host: "db.example", Database: "app", User: "reader", Env: EnvLocal, SSLMode: mode}.Profile()
	return p
}

func TestConnConfigIgnoresEnvironment(t *testing.T) {
	pgpass := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(pgpass, []byte("*:*:*:*:from-pgpass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", pgpass)
	t.Setenv("PGPASSWORD", "from-env")
	t.Setenv("PGHOST", "env-host")
	t.Setenv("PGSSLMODE", "verify-full")
	t.Setenv("PGOPTIONS", "-c search_path=evil")
	t.Setenv("PGTARGETSESSIONATTRS", "read-write")

	cc, err := connConfig(testProfile(SSLDisable), "")
	if err != nil {
		t.Fatal(err)
	}
	if cc.Host != "db.example" || cc.Port != 5432 || cc.Database != "app" || cc.User != "reader" || cc.Password != "" {
		t.Fatalf("config not taken from profile: host=%s port=%d db=%s user=%s pwset=%v",
			cc.Host, cc.Port, cc.Database, cc.User, cc.Password != "")
	}
	if cc.TLSConfig != nil || len(cc.Fallbacks) != 0 || cc.ValidateConnect != nil {
		t.Fatal("environment leaked into TLS or session settings")
	}
	if len(cc.RuntimeParams) != 2 || cc.RuntimeParams["application_name"] != "dbscope" || cc.RuntimeParams["client_encoding"] != "UTF8" {
		t.Fatalf("runtime params %v", cc.RuntimeParams)
	}
	if cc.ConnectTimeout.Seconds() != 10 {
		t.Fatalf("connect timeout %v", cc.ConnectTimeout)
	}
	cc, _ = connConfig(testProfile(SSLDisable), "pw")
	if cc.Password != "pw" {
		t.Fatal("password not applied")
	}
}

func TestTLSModes(t *testing.T) {
	cfgs, _ := tlsConfigsFor(testProfile(SSLDisable))
	if len(cfgs) != 1 || cfgs[0] != nil {
		t.Fatal("disable must be plaintext only")
	}
	cfgs, _ = tlsConfigsFor(testProfile(SSLPrefer))
	if len(cfgs) != 2 || cfgs[0] == nil || !cfgs[0].InsecureSkipVerify || cfgs[1] != nil {
		t.Fatal("prefer must try TLS then plaintext")
	}
	cfgs, _ = tlsConfigsFor(testProfile(SSLRequire))
	if len(cfgs) != 1 || cfgs[0] == nil || !cfgs[0].InsecureSkipVerify {
		t.Fatal("require must be TLS only without verification")
	}
	cfgs, _ = tlsConfigsFor(testProfile(SSLVerifyCA))
	if len(cfgs) != 1 || !cfgs[0].InsecureSkipVerify || cfgs[0].VerifyPeerCertificate == nil {
		t.Fatal("verify-ca must verify the chain itself")
	}
	cfgs, _ = tlsConfigsFor(testProfile(SSLVerifyFull))
	if len(cfgs) != 1 || cfgs[0].InsecureSkipVerify || cfgs[0].ServerName != "db.example" {
		t.Fatal("verify-full must verify chain and hostname")
	}

	p := testProfile(SSLRequire)
	p.SSLRootCert = filepath.Join(t.TempDir(), "missing.crt")
	if _, err := tlsConfigsFor(p); err == nil || classify(err).Kind != "tls" {
		t.Fatalf("missing root cert: %v", err)
	}
	if err := os.WriteFile(p.SSLRootCert, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsConfigsFor(p); err == nil {
		t.Fatal("non-PEM root cert accepted")
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		err      error
		kind     string
		sqlstate string
	}{
		{&pgconn.PgError{Code: "28P01", Message: "password authentication failed"}, "auth", "28P01"},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "28000", Message: "no pg_hba.conf entry"}), "auth", "28000"},
		{&pgconn.PgError{Code: "3D000", Message: "database does not exist"}, "database", "3D000"},
		{context.DeadlineExceeded, "timeout", ""},
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, "network", ""},
		{&net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}, "network", ""},
		{&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsTimeout: true}}, "timeout", ""},
		{x509.UnknownAuthorityError{}, "tls", ""},
		{fmt.Errorf("failed to write startup message: %w", &tls.CertificateVerificationError{Err: errors.New("x")}), "tls", ""},
		{errors.New("server refused TLS connection"), "tls", ""},
		{errors.New("something else"), "other", ""},
	} {
		got := classify(c.err)
		if got.Kind != c.kind || got.SQLState != c.sqlstate || got.Message == "" {
			t.Errorf("classify(%v) = %+v, want kind %s sqlstate %q", c.err, got, c.kind, c.sqlstate)
		}
	}
}

func TestSSLDisableWarning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no network needed; the warning is computed before connecting
	res := Test(ctx, testProfile(SSLDisable), "")
	if res.OK || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "sslMode is disable") {
		t.Fatalf("result %+v", res)
	}
	p := testProfile(SSLDisable)
	p.Host = "127.0.0.1"
	if res := Test(ctx, p, ""); len(res.Warnings) != 0 {
		t.Fatalf("loopback should not warn: %v", res.Warnings)
	}
}

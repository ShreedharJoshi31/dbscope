package conn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Integration tests run against DBSCOPE_TEST_DSN when set (it must be a
// superuser on a disposable server: the tests create and drop roles), or
// else a postgres:17-alpine container started through testcontainers.

var (
	dbOnce  sync.Once
	dbAdmin Profile
	dbPass  string
	dbSkip  string
	dbStop  func()
)

func TestMain(m *testing.M) {
	code := m.Run()
	if dbStop != nil {
		dbStop()
	}
	os.Exit(code)
}

func testDB(t *testing.T) (Profile, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	dbOnce.Do(startDB)
	if dbSkip != "" {
		t.Skip(dbSkip)
	}
	return dbAdmin, dbPass
}

func startDB() {
	dsn := os.Getenv("DBSCOPE_TEST_DSN")
	if dsn == "" {
		if err := dockerHealthy(); err != nil {
			dbSkip = "no DBSCOPE_TEST_DSN and Docker is unavailable: " + err.Error()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ctr, err := postgres.Run(ctx, "postgres:17-alpine",
			postgres.WithDatabase("dbscope"), postgres.WithUsername("admin"), postgres.WithPassword("admin-pass"),
			postgres.BasicWaitStrategies())
		if ctr != nil {
			dbStop = func() { testcontainers.TerminateContainer(ctr) }
		}
		if err != nil {
			dbSkip = "could not start postgres container: " + err.Error()
			return
		}
		if dsn, err = ctr.ConnectionString(ctx, "sslmode=disable"); err != nil {
			dbSkip = "container connection string: " + err.Error()
			return
		}
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		dbSkip = "DBSCOPE_TEST_DSN does not parse" // never echo the DSN
		return
	}
	// pgtest.SSLMode, which this package cannot import: no sslmode means prefer.
	mode := SSLDisable
	if cfg.TLSConfig != nil {
		mode = SSLRequire
		for _, fb := range cfg.Fallbacks {
			if fb.TLSConfig == nil {
				mode = SSLPrefer
			}
		}
	}
	dbAdmin, _ = ProfileInput{
		Name: "admin", Host: cfg.Host, Port: ptr(int(cfg.Port)), Database: cfg.Database, User: cfg.User,
		SSLMode: mode, Env: EnvLocal,
	}.Profile()
	dbPass = cfg.Password
}

func dockerHealthy() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	p, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err
	}
	defer p.Close()
	return p.Health(context.Background())
}

func adminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	p, pass := testDB(t)
	cc, err := connConfig(p, pass)
	if err != nil {
		t.Fatal(err)
	}
	c, err := pgx.ConnectConfig(t.Context(), cc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func suffix() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fixture creates a schema with one table and a login role granted privs on
// it, and drops them all when the test ends.
func fixture(t *testing.T, privs string) (Profile, string, string) {
	t.Helper()
	admin := adminConn(t)
	p, _ := testDB(t)
	sfx := suffix()
	schema, role, pass := "dbscope_it_"+sfx, "dbscope_role_"+sfx, "role-pass-"+sfx
	stmts := []string{
		"CREATE SCHEMA " + schema,
		"CREATE TABLE " + schema + ".orders (id int)",
		"CREATE ROLE " + role + " LOGIN PASSWORD '" + pass + "'",
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{p.Database}.Sanitize() + " TO " + role,
		"GRANT USAGE ON SCHEMA " + schema + " TO " + role,
		"GRANT " + privs + " ON " + schema + ".orders TO " + role,
	}
	t.Cleanup(func() {
		ctx := context.Background()
		admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		admin.Exec(ctx, "DROP OWNED BY "+role)
		admin.Exec(ctx, "DROP ROLE IF EXISTS "+role)
	})
	for _, s := range stmts {
		if _, err := admin.Exec(t.Context(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	p.User = role
	return p, pass, schema
}

func TestIntegrationSuccess(t *testing.T) {
	p, pass := testDB(t)
	res := Test(t.Context(), p, pass)
	if !res.OK || res.Error != nil {
		t.Fatalf("not ok: %+v", res.Error)
	}
	if res.LatencyMs == nil || res.IsReplica == nil || *res.IsReplica || res.DefaultReadOnly == nil || *res.DefaultReadOnly ||
		res.PgStatStatements == nil {
		t.Fatalf("missing fields: %+v", res)
	}
	major, _, _ := strings.Cut(res.ServerVersion, ".")
	if m, err := strconv.Atoi(major); err != nil || m != res.ServerVersionNum/10000 {
		t.Fatalf("serverVersion %q does not match serverVersionNum %d", res.ServerVersion, res.ServerVersionNum)
	}
	if !slices.Contains(res.WritePrivileges, "SUPERUSER") {
		t.Fatalf("superuser not reported: %v", res.WritePrivileges)
	}
	if len(res.WritePrivileges) > maxPrivileges+1 {
		t.Fatalf("privileges not capped: %d entries", len(res.WritePrivileges))
	}

	// The facts a prod-replica check needs come as fields, not duplicated as warnings.
	p.Env = EnvProdReplica
	res = Test(t.Context(), p, pass)
	if res.IsReplica == nil || *res.IsReplica || len(res.WritePrivileges) == 0 {
		t.Fatalf("prod-replica facts missing: replica=%v privileges=%v", res.IsReplica, res.WritePrivileges)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings duplicate the fields: %v", res.Warnings)
	}
}

func TestIntegrationWrongPassword(t *testing.T) {
	p, _ := testDB(t)
	res := Test(t.Context(), p, "definitely-wrong")
	if res.OK || res.Error == nil || res.Error.Kind != "auth" || res.Error.SQLState != "28P01" {
		t.Fatalf("want auth error, got %+v", res.Error)
	}
	if strings.Contains(res.Error.Message, "definitely-wrong") {
		t.Fatal("password echoed in error")
	}
}

func TestIntegrationClosedPort(t *testing.T) {
	p, pass := testDB(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.Host, p.Port = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	res := Test(t.Context(), p, pass)
	if res.OK || res.Error == nil || res.Error.Kind != "network" {
		t.Fatalf("want network error, got %+v", res.Error)
	}
}

func TestIntegrationSelectOnlyRole(t *testing.T) {
	p, pass, _ := fixture(t, "SELECT")
	res := Test(t.Context(), p, pass)
	if !res.OK {
		t.Fatalf("not ok: %+v", res.Error)
	}
	// Assumes PostgreSQL 15+ defaults, where PUBLIC has no CREATE on schema public.
	if res.WritePrivileges == nil || len(res.WritePrivileges) != 0 {
		t.Fatalf("want empty writePrivileges, got %#v", res.WritePrivileges)
	}
	p.Env = EnvProdReplica
	for _, w := range Test(t.Context(), p, pass).Warnings {
		if strings.Contains(w, "write privileges") {
			t.Fatalf("unexpected warning: %s", w)
		}
	}
}

func TestIntegrationInsertRole(t *testing.T) {
	p, pass, schema := fixture(t, "SELECT, INSERT")
	res := Test(t.Context(), p, pass)
	if !res.OK {
		t.Fatalf("not ok: %+v", res.Error)
	}
	want := "INSERT on " + schema + ".orders"
	if !slices.Equal(res.WritePrivileges, []string{want}) {
		t.Fatalf("want [%s], got %v", want, res.WritePrivileges)
	}
}

func TestIntegrationPrivilegeCap(t *testing.T) {
	admin := adminConn(t)
	p, pass, schema := fixture(t, "INSERT")
	for i := range 30 {
		if _, err := admin.Exec(t.Context(), fmt.Sprintf("CREATE TABLE %s.t%02d (id int)", schema, i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(t.Context(), "GRANT UPDATE ON ALL TABLES IN SCHEMA "+schema+" TO "+p.User); err != nil {
		t.Fatal(err)
	}
	res := Test(t.Context(), p, pass)
	if len(res.WritePrivileges) != maxPrivileges+1 || res.WritePrivileges[maxPrivileges] != "... and 11 more" {
		t.Fatalf("cap: %d entries, last %q", len(res.WritePrivileges), res.WritePrivileges[len(res.WritePrivileges)-1])
	}
}

// A database whose sessions default to another client encoding must still
// return UTF-8, or pgx hands back invalid strings.
func TestIntegrationClientEncoding(t *testing.T) {
	p, pass := testDB(t)
	ctx := t.Context()
	cc, err := connConfig(p, pass)
	if err != nil {
		t.Fatal(err)
	}
	c, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "CREATE TABLE enc(s text); INSERT INTO enc VALUES ('café ñ')"); err != nil {
		t.Fatal(err)
	}
	// A DBSCOPE_TEST_DSN server outlives the run: leave it as found, so the test can run again.
	t.Cleanup(func() {
		if c, err := pgx.ConnectConfig(context.Background(), cc); err == nil {
			c.Exec(context.Background(), "DROP TABLE enc; ALTER DATABASE "+pgx.Identifier{p.Database}.Sanitize()+" RESET client_encoding")
			c.Close(context.Background())
		}
	})
	if _, err := c.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{p.Database}.Sanitize()+" SET client_encoding = 'LATIN1'"); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)

	pool, err := NewPool(ctx, p, pass)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var s string
	if err := pool.QueryRow(ctx, "SELECT s FROM enc").Scan(&s); err != nil {
		t.Fatal(err)
	}
	if s != "café ñ" {
		t.Fatalf("got %q", s)
	}
}

func TestIntegrationPool(t *testing.T) {
	p, pass := testDB(t)
	pool, err := NewPool(t.Context(), p, pass)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if got := pool.Config().MaxConns; got != 2 {
		t.Fatalf("MaxConns %d", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := pool.Exec(ctx, "SELECT pg_sleep(30)"); err == nil {
		t.Fatal("pg_sleep was not cancelled")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel took %v", d)
	}
	var one int
	if err := pool.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("pool unusable after cancel: %v", err)
	}
}

// TestIntegrationRDSIAMReplacesPassword shows the RDS IAM token, not the
// stored password, is what reaches the server: with the right password and
// auth=rds-iam, the login fails because the token is sent instead.
func TestIntegrationRDSIAMReplacesPassword(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	p, pass := testDB(t)
	if res := Test(t.Context(), p, pass); !res.OK {
		t.Fatalf("plain login: %+v", res.Error)
	}
	p.Options = "auth=rds-iam&region=eu-west-1"
	res := Test(t.Context(), p, pass)
	if res.OK || res.Error == nil || res.Error.SQLState != "28P01" {
		t.Fatalf("the stored password was used, or another error: %+v", res.Error)
	}
	pool, err := NewPool(t.Context(), p, pass)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(t.Context()); err == nil || !strings.Contains(err.Error(), "28P01") {
		t.Fatalf("pool login with the stored password: %v", err)
	}
}

// Package pgtest gives integration tests a fresh PostgreSQL database per test.
//
// Servers come from DBSCOPE_TEST_DSN when it is set (it must be a superuser
// on a disposable server: tests create databases and roles), or else from
// postgres:<version>-alpine containers started through testcontainers. When
// neither is available the tests are skipped with the reason.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/dbscope/dbscope/internal/conn"
)

// Server is a running PostgreSQL server the tests may create databases on.
type Server struct {
	// Name is "14", "18", ... for containers or "dsn" for DBSCOPE_TEST_DSN.
	Name string
	// Major is the server's major version, e.g. 18.
	Major int
	admin *pgx.ConnConfig
}

type started struct {
	once sync.Once
	srv  *Server
	skip string
	stop func()
}

var (
	mu      sync.Mutex
	servers = map[string]*started{}
)

// Main runs the package's tests and then stops any containers they started.
func Main(m *testing.M) {
	code := m.Run()
	mu.Lock()
	for _, s := range servers {
		if s.stop != nil {
			s.stop()
		}
	}
	mu.Unlock()
	os.Exit(code)
}

// Servers returns a server per requested major version, or the single
// DBSCOPE_TEST_DSN server when that is set. It skips the test in -short mode
// or when no server can be started.
func Servers(t testing.TB, versions ...string) []*Server {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	if os.Getenv("DBSCOPE_TEST_DSN") != "" {
		versions = []string{"dsn"}
	}
	var out []*Server
	for _, v := range versions {
		mu.Lock()
		s := servers[v]
		if s == nil {
			s = &started{}
			servers[v] = s
		}
		mu.Unlock()
		s.once.Do(func() { s.srv, s.stop, s.skip = start(v) })
		if s.skip != "" {
			t.Skip(s.skip)
		}
		out = append(out, s.srv)
	}
	return out
}

func start(version string) (*Server, func(), string) {
	dsn := os.Getenv("DBSCOPE_TEST_DSN")
	var stop func()
	if version != "dsn" {
		if err := dockerHealthy(); err != nil {
			return nil, nil, "no DBSCOPE_TEST_DSN and Docker is unavailable: " + err.Error()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ctr, err := postgres.Run(ctx, "postgres:"+version+"-alpine",
			postgres.WithDatabase("dbscope"), postgres.WithUsername("admin"), postgres.WithPassword("admin-pass"),
			postgres.BasicWaitStrategies())
		if ctr != nil {
			stop = func() { testcontainers.TerminateContainer(ctr) }
		}
		if err != nil {
			return nil, stop, "could not start postgres:" + version + "-alpine: " + err.Error()
		}
		if dsn, err = ctr.ConnectionString(ctx, "sslmode=disable"); err != nil {
			return nil, stop, "container connection string: " + err.Error()
		}
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, stop, "DBSCOPE_TEST_DSN does not parse" // never echo the DSN
	}
	srv := &Server{Name: version, admin: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, stop, "connect to test server: " + conn.Classify(err).Message
	}
	defer c.Close(ctx)
	var num int
	if err := c.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		return nil, stop, "read server version: " + err.Error()
	}
	srv.Major = num / 10000
	return srv, stop, ""
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

// DB is a database created for one test and dropped, together with the
// roles made through Role, when the test ends.
type DB struct {
	Name string
	// Admin is a superuser connection to the database.
	Admin *pgx.Conn
	srv   *Server
	cfg   *pgx.ConnConfig
	roles []string
}

func suffix() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NewDB creates an empty database on the server.
func (s *Server) NewDB(t testing.TB) *DB {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, s.admin)
	if err != nil {
		t.Fatal(err)
	}
	d := &DB{Name: "dbscope_t_" + suffix(), srv: s}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+d.Name); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if d.Admin != nil {
			d.Admin.Close(ctx)
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+d.Name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", d.Name, err)
		}
		for _, r := range d.roles {
			if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+r); err != nil {
				t.Errorf("drop role %s: %v", r, err)
			}
		}
		admin.Close(ctx)
	})
	d.cfg = s.admin.Copy()
	d.cfg.Database = d.Name
	if d.Admin, err = pgx.ConnectConfig(ctx, d.cfg); err != nil {
		t.Fatal(err)
	}
	return d
}

// Exec runs sql, which may hold several statements, as the superuser.
func (d *DB) Exec(t testing.TB, sql string) {
	t.Helper()
	if _, err := d.Admin.Exec(context.Background(), sql); err != nil {
		t.Fatalf("exec: %v\n%s", err, sql)
	}
}

// Role returns a unique role name derived from base, dropped at cleanup.
// The caller creates the role.
func (d *DB) Role(base string) string {
	name := base + "_" + suffix()
	d.roles = append(d.roles, name)
	return name
}

// Superuser returns a profile for the database as the superuser, and its password.
func (d *DB) Superuser(t testing.TB) (conn.Profile, string) {
	t.Helper()
	return d.As(t, d.cfg.User), d.cfg.Password
}

// As returns a profile for the database as user; the caller knows its password.
func (d *DB) As(t testing.TB, user string) conn.Profile {
	t.Helper()
	p, err := conn.ProfileInput{
		Name: "test " + d.Name, Host: d.cfg.Host, Port: ptr(int(d.cfg.Port)), Database: d.Name, User: user,
		SSLMode: sslMode(d.cfg), Env: conn.EnvLocal,
	}.Profile()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sslMode(cfg *pgx.ConnConfig) conn.SSLMode {
	if cfg.TLSConfig != nil {
		return conn.SSLRequire
	}
	return conn.SSLDisable
}

func ptr[T any](v T) *T { return &v }

// String names the server in subtest names.
func (s *Server) String() string {
	if s.Name == "dsn" {
		return "dsn-pg" + strconv.Itoa(s.Major)
	}
	return "pg" + s.Name
}

// Connect opens a connection to the database as user, closed at cleanup.
func (d *DB) Connect(t testing.TB, user, password string) *pgx.Conn {
	t.Helper()
	cfg := d.cfg.Copy()
	cfg.User, cfg.Password = user, password
	c, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

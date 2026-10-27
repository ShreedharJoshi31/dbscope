package capture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/dbscope/dbscope/internal/pgtest"
)

func TestMain(m *testing.M) {
	// Re-executed by TestCapture as the "migrate command".
	if os.Getenv("DBSCOPE_CAPTURE_HELPER") != "" {
		os.Exit(helper(os.Getenv("DBSCOPE_CAPTURE_HELPER")))
	}
	pgtest.Main(m)
}

// helper acts like a framework's migrate command, connecting through
// DATABASE_URL the way the proxy tells it to.
func helper(mode string) int {
	if mode == "noconnect" {
		return 0
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer c.Close(ctx)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version varchar PRIMARY KEY)`,
		`SELECT version FROM schema_migrations`,
		`BEGIN`,
		`CREATE TABLE users (id bigint PRIMARY KEY, email text)`,
		`INSERT INTO "schema_migrations" ("version") VALUES ('1') RETURNING "version"`,
		`COMMIT`,
		`CREATE INDEX CONCURRENTLY users_email ON users (email)`,
		`INSERT INTO "schema_migrations" ("version") VALUES ('2') RETURNING "version"`,
	} {
		if _, err := c.Exec(ctx, q, pgx.QueryExecModeSimpleProtocol); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	// Rails records versions with binds when prepared statements are on.
	if _, err := c.Exec(ctx, `INSERT INTO "schema_migrations" ("version") VALUES ($1)`, pgx.QueryExecModeExec, "3"); err != nil {
		return 1
	}
	return 0
}

func dbURL(t *testing.T, d *pgtest.DB) string {
	cfg := d.Admin.Config()
	ssl := "disable"
	if cfg.TLSConfig != nil {
		ssl = "require"
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password),
		Host: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), Path: "/" + cfg.Database, RawQuery: "sslmode=" + ssl}
	return u.String()
}

func startProxy(t *testing.T, d *pgtest.DB) (*Proxy, *Recorder, string) {
	cfg, err := pgconn.ParseConfig(dbURL(t, d))
	if err != nil {
		t.Fatal(err)
	}
	rec := &Recorder{}
	p, err := Listen("127.0.0.1:0", UpstreamFrom(cfg), rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close(time.Second) })
	u := url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password),
		Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port())), Path: "/" + cfg.Database,
		// prefer: the proxy answers SSLRequest with 'N' and pgx retries in plaintext.
		RawQuery: "sslmode=prefer"}
	return p, rec, u.String()
}

func TestProxyRecordsWhatRan(t *testing.T) {
	d := pgtest.Servers(t, "17")[0].NewDB(t)
	_, rec, proxyURL := startProxy(t, d)
	ctx := t.Context()
	c, err := pgx.Connect(ctx, proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())

	mustExec := func(sql string, mode pgx.QueryExecMode, args ...any) {
		t.Helper()
		if _, err := c.Exec(ctx, sql, append([]any{mode}, args...)...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec("CREATE TABLE a (id int, note text); INSERT INTO a VALUES (1, 'x')", pgx.QueryExecModeSimpleProtocol)
	mustExec("INSERT INTO a VALUES ($1, $2)", pgx.QueryExecModeExec, 2, "it's")
	if _, err := c.Exec(ctx, "INSERT INTO a VALUES (3); INSERT INTO missing VALUES (1)", pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("expected an error")
	}
	// One pipeline with a single Sync: an implicit transaction on the server.
	b := &pgconn.Batch{}
	for _, q := range []string{"INSERT INTO a VALUES (4)", "INSERT INTO missing VALUES (2)", "INSERT INTO a VALUES (5)"} {
		b.ExecParams(q, nil, nil, nil, nil)
	}
	if _, err := c.PgConn().ExecBatch(ctx, b).ReadAll(); err == nil {
		t.Fatal("expected a batch error")
	}
	mustExec("BEGIN", pgx.QueryExecModeSimpleProtocol)
	mustExec("INSERT INTO a VALUES (6)", pgx.QueryExecModeSimpleProtocol)
	mustExec("COMMIT", pgx.QueryExecModeSimpleProtocol)

	events, conns, _ := rec.Snapshot()
	if conns < 1 {
		t.Fatalf("connections %d", conns)
	}
	type row struct {
		sql string
		ok  bool
	}
	var got []row
	for _, e := range events {
		got = append(got, row{e.SQL, e.Error == ""})
	}
	want := []row{
		{"CREATE TABLE a (id int, note text)", true},
		{"INSERT INTO a VALUES (1, 'x')", true},
		{"INSERT INTO a VALUES ($1, $2)", true},
		// The failing statement rolls back the rest of its implicit transaction.
		{"INSERT INTO a VALUES (3)", false},
		{"INSERT INTO missing VALUES (1)", false},
		{"INSERT INTO a VALUES (4)", false},
		{"INSERT INTO missing VALUES (2)", false},
		{"BEGIN", true},
		{"INSERT INTO a VALUES (6)", true},
		{"COMMIT", true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events\n%v\nwant\n%v", got, want)
	}
	var n int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM a").Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows in a: %d %v (the proxy must not change what runs)", n, err)
	}
	ins := events[2]
	if sql, _ := Render(ins.SQL, ins.Params); !strings.Contains(sql, "'it''s'") {
		t.Fatalf("rendered %q", sql)
	}
	res := Segment(events)
	if res.Unrecorded == nil || len(res.Failed) != 4 {
		t.Fatalf("segment: unrecorded %v failed %d", res.Unrecorded, len(res.Failed))
	}
	if sql := res.Unrecorded.SQL; !strings.HasSuffix(sql[0], "BEGIN") || sql[3] != "COMMIT" {
		t.Fatalf("the multi-statement query should be wrapped: %q", sql)
	}
}

func TestProxyRecordsCommitFailureAtSync(t *testing.T) {
	d := pgtest.Servers(t, "17")[0].NewDB(t)
	d.Exec(t, `CREATE TABLE p (id int PRIMARY KEY);
		CREATE TABLE c (p int REFERENCES p DEFERRABLE INITIALLY DEFERRED)`)
	_, rec, proxyURL := startProxy(t, d)
	c, err := pgx.Connect(t.Context(), proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	// The INSERT completes; the deferred check fails when Sync commits it.
	if _, err := c.Exec(t.Context(), "INSERT INTO c VALUES ($1)", pgx.QueryExecModeExec, 99); err == nil {
		t.Fatal("expected 23503 at commit")
	}
	events, _, _ := rec.Snapshot()
	if len(events) != 1 || !strings.Contains(events[0].Error, "23503") {
		t.Fatalf("events %+v", events)
	}
}

func TestProxyForwardsCancelAndRefusesDirectTLS(t *testing.T) {
	d := pgtest.Servers(t, "17")[0].NewDB(t)
	p, rec, proxyURL := startProxy(t, d)
	cfg, err := pgx.ParseConfig(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: 10 * time.Second}
	}
	c, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.Exec(ctx, "SELECT pg_sleep(20)")
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "57014" {
		t.Fatalf("want query_canceled through the proxy, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel was not forwarded")
	}

	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port())))
	if err != nil {
		t.Fatal(err)
	}
	raw.Write([]byte{0x16, 3, 1, 0, 5})
	raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := raw.Read(make([]byte, 1)); err == nil {
		t.Fatal("direct TLS should be refused")
	}
	raw.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, notes := rec.Snapshot()
		if slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "sslmode=disable") }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("notes %q", notes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCapture(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	shadow := srv.NewDB(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	opts := func(mode string) Options {
		t.Setenv("DBSCOPE_CAPTURE_HELPER", mode)
		return Options{Shadow: dbURL(t, shadow), Command: []string{self}, Dir: t.TempDir(), Stdout: os.Stdout, Stderr: os.Stderr}
	}

	if _, err := Capture(t.Context(), opts("noconnect")); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("want ErrNoConnection, got %v", err)
	}
	o := opts("migrate")
	o.Target = o.Shadow
	if _, err := Capture(t.Context(), o); err == nil || !strings.Contains(err.Error(), "same database") {
		t.Fatalf("want same-database refusal, got %v", err)
	}

	run, err := Capture(t.Context(), opts("migrate"))
	if err != nil {
		t.Fatal(err)
	}
	if run.CommandErr != nil {
		t.Fatal(run.CommandErr)
	}
	if got := keys(run.Migrations); !slices.EqualFunc(got, [][]string{{"1"}, {"2"}, {"3"}}, slices.Equal) || run.Migrations[0].Framework != Rails {
		t.Fatalf("migrations %v", got)
	}
	if got := run.Migrations[1].SQL; len(got) != 1 || !strings.Contains(got[0], "CONCURRENTLY") {
		t.Fatalf("version 2 SQL %q", got)
	}

	// The shadow now has tables, so a second run is refused.
	if _, err := Capture(t.Context(), opts("migrate")); !errors.Is(err, ErrShadowNotEmpty) {
		t.Fatalf("want ErrShadowNotEmpty, got %v", err)
	}

	target := srv.NewDB(t)
	target.Exec(t, `CREATE TABLE schema_migrations (version varchar PRIMARY KEY); INSERT INTO schema_migrations VALUES ('1')`)
	pending, err := Pending(t.Context(), target.Admin, run.Migrations)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(pending); !slices.EqualFunc(got, [][]string{{"2"}, {"3"}}, slices.Equal) {
		t.Fatalf("pending %v", got)
	}
	fresh := srv.NewDB(t)
	if pending, err := Pending(t.Context(), fresh.Admin, run.Migrations); err != nil || len(pending) != 3 {
		t.Fatalf("a database without the bookkeeping table has nothing applied: %d %v", len(pending), err)
	}

	dir := t.TempDir() + "/out"
	files, err := Write(dir, run.Migrations, run.Unrecorded, nil)
	if err != nil || len(files) != 2 {
		// Version 3 recorded no SQL of its own.
		t.Fatalf("files %v %v", files, err)
	}
	if _, err := Write(dir, run.Migrations, nil, nil); err == nil {
		t.Fatal("writing into a non-empty directory should fail")
	}
}

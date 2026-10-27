package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Options configures a capture run.
type Options struct {
	Shadow  string   // URL of the throwaway database the command migrates
	Target  string   // optional URL of the real database: never migrated, read for what it has applied
	Command []string // migrate command and its arguments
	Dir     string   // working directory for the command
	// URLEnv names extra variables to set to the proxy URL, for projects that
	// read something other than the common names below.
	URLEnv        []string
	AllowNonEmpty bool
	Stdout        io.Writer
	Stderr        io.Writer
}

// Run is the outcome of a capture.
type Run struct {
	Result
	// Pending is Migrations without those Target has applied (all of them
	// when there is no Target).
	Pending     []Migration
	Connections int
	Notes       []string
	// CommandErr is the migrate command's failure, if it failed; whatever
	// it ran before failing is still in Result.
	CommandErr error
}

var (
	// ErrNoConnection means the command never connected through the proxy:
	// it ignored the database settings dbscope gave it.
	ErrNoConnection = errors.New("the command never connected through dbscope; it probably used the database its own configuration names, so nothing was captured")
	// ErrShadowNotEmpty guards against pointing --shadow at a real database.
	ErrShadowNotEmpty = errors.New("the shadow database already has tables; capture needs an empty, throwaway database (use --allow-nonempty-shadow if it was prepared on purpose)")
)

// urlVars are set to the proxy URL: the names migrate tools and hosting
// platforms commonly read a PostgreSQL URL from. A tool that loads a .env
// file keeps these values, since dotenv does not override the environment.
var urlVars = []string{"DATABASE_URL", "DIRECT_URL", "DATABASE_URL_UNPOOLED", "POSTGRES_URL", "POSTGRES_URL_NON_POOLING", "POSTGRES_PRISMA_URL"}

// Removed from the command's environment: libpq variables that would steer it
// somewhere else, and dbscope's own --dsn fallback, which names the real database.
var unsetVars = []string{"PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE", "PGREQUIRESSL", "PGSSLNEGOTIATION", "DBSCOPE_DSN"}

var otherURLVar = regexp.MustCompile(`(?i)(^|_)(DATABASE|POSTGRES|POSTGRESQL|PG|DB)_?(URL|URI|DSN)$`)

// Capture runs the command against the shadow through a recording proxy.
func Capture(ctx context.Context, o Options) (*Run, error) {
	cfg, err := pgconn.ParseConfig(o.Shadow)
	if err != nil {
		return nil, fmt.Errorf("--shadow: %w", err)
	}
	if err := strayURLVars(o.URLEnv); err != nil {
		return nil, err
	}
	shadow, err := pgx.Connect(ctx, o.Shadow)
	if err != nil {
		return nil, fmt.Errorf("shadow database: %w", err)
	}
	defer shadow.Close(context.Background())
	var target *pgx.Conn
	if o.Target != "" {
		if target, err = pgx.Connect(ctx, o.Target); err != nil {
			return nil, fmt.Errorf("--dsn: %w", err)
		}
		defer target.Close(context.Background())
		same, err := sameDatabase(ctx, shadow, target)
		if err != nil {
			return nil, err
		}
		if same {
			return nil, errors.New("--shadow and --dsn are the same database; the shadow is migrated and must be a throwaway copy")
		}
	}
	if !o.AllowNonEmpty {
		if err := checkEmpty(ctx, shadow); err != nil {
			return nil, err
		}
	}

	rec := &Recorder{}
	p, err := Listen("127.0.0.1:0", UpstreamFrom(cfg), rec)
	if err != nil {
		return nil, err
	}
	env, cleanup, err := commandEnv(o, cfg, p.Port())
	if err != nil {
		p.Close(0)
		return nil, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, o.Command[0], o.Command[1:]...)
	cmd.Dir, cmd.Env = o.Dir, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, o.Stdout, o.Stderr
	cmdErr := cmd.Run()
	p.Close(2 * time.Second)

	events, conns, notes := rec.Snapshot()
	if conns == 0 {
		if cmdErr != nil {
			return nil, fmt.Errorf("%w (the command also failed: %v)", ErrNoConnection, cmdErr)
		}
		return nil, ErrNoConnection
	}
	run := &Run{Result: Segment(events), Connections: conns, Notes: notes, CommandErr: cmdErr}
	run.Pending = run.Migrations
	if target != nil {
		// The command may have been interrupted; reading what is applied still has to work.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if run.Pending, err = Pending(rctx, target, run.Migrations); err != nil {
			return nil, fmt.Errorf("reading applied migrations from --dsn: %w", err)
		}
	}
	return run, nil
}

func checkEmpty(ctx context.Context, c *pgx.Conn) error {
	var n int
	err := c.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'`).Scan(&n)
	if err != nil {
		return fmt.Errorf("shadow database: %w", err)
	}
	if n > 0 {
		return ErrShadowNotEmpty
	}
	return nil
}

// sameDatabase compares the server and database each connection reached, so
// two host names or addresses for one server still match.
func sameDatabase(ctx context.Context, a, b *pgx.Conn) (bool, error) {
	const q = `SELECT pg_postmaster_start_time()::text || '/' || d.oid FROM pg_database d WHERE d.datname = current_database()`
	var x, y string
	if err := a.QueryRow(ctx, q).Scan(&x); err != nil {
		return false, fmt.Errorf("shadow database: %w", err)
	}
	if err := b.QueryRow(ctx, q).Scan(&y); err != nil {
		return false, fmt.Errorf("--dsn: %w", err)
	}
	return x == y, nil
}

// strayURLVars refuses to run while the environment holds a database URL
// under a name dbscope does not redirect: the command might migrate it.
func strayURLVars(extra []string) error {
	redirected := map[string]bool{}
	for _, n := range append(append([]string(nil), urlVars...), extra...) {
		redirected[strings.ToUpper(n)] = true
	}
	var stray []string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if v != "" && !redirected[strings.ToUpper(k)] && otherURLVar.MatchString(k) {
			stray = append(stray, k)
		}
	}
	if len(stray) > 0 {
		slices.Sort(stray)
		return fmt.Errorf("%s may point the command at another database; pass --url-env for each to send it through dbscope too, or unset it", strings.Join(stray, ", "))
	}
	return nil
}

// commandEnv points the command's database settings at the proxy.
func commandEnv(o Options, cfg *pgconn.Config, port int) ([]string, func(), error) {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:     "/" + cfg.Database,
		RawQuery: "sslmode=disable",
	}
	if cfg.Password == "" {
		u.User = url.User(cfg.User)
	}
	proxyURL := u.String()
	set := map[string]string{
		"PGHOST":     "127.0.0.1",
		"PGPORT":     strconv.Itoa(port),
		"PGDATABASE": cfg.Database,
		"PGUSER":     cfg.User,
		"PGPASSWORD": cfg.Password,
		"PGSSLMODE":  "disable",
	}
	for _, name := range append(append([]string(nil), urlVars...), o.URLEnv...) {
		set[name] = proxyURL
	}
	var temp []string
	cleanup := func() {
		for _, d := range temp {
			os.RemoveAll(d)
		}
	}
	if isRails(o.Command) && os.Getenv("SCHEMA") == "" {
		// Rails loads db/schema.rb into a database without schema_migrations
		// instead of migrating it, and dumps the schema afterwards: point both
		// at a scratch file so the migrations run and the project's file is untouched.
		dir, err := os.MkdirTemp("", "dbscope-rails-")
		if err != nil {
			return nil, nil, err
		}
		temp = append(temp, dir)
		set["SCHEMA"] = filepath.Join(dir, "schema")
	}
	if isDjango(o.Command) {
		dir, orig, err := djangoShim(o, cfg, port)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		temp = append(temp, dir)
		set["DBSCOPE_DJANGO_SETTINGS"] = orig
		set["DJANGO_SETTINGS_MODULE"] = "dbscope_capture_settings"
		set["PYTHONPATH"] = dir + string(os.PathListSeparator) + os.Getenv("PYTHONPATH")
	}
	env := make([]string, 0, len(os.Environ())+len(set))
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !has(set, k) && !slices.Contains(unsetVars, k) {
			env = append(env, kv)
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env, cleanup, nil
}

func isRails(cmd []string) bool {
	for _, a := range cmd {
		if b := filepath.Base(a); b == "rails" || b == "rake" {
			return true
		}
	}
	return false
}

func isDjango(cmd []string) bool {
	for _, a := range cmd {
		if b := filepath.Base(a); b == "manage.py" || b == "django-admin" {
			return true
		}
	}
	return false
}

var settingsDefault = regexp.MustCompile(`setdefault\(\s*["']DJANGO_SETTINGS_MODULE["']\s*,\s*["']([\w.]+)["']`)

// djangoShim writes a settings module that imports the project's settings and
// replaces the default database, since Django reads no database URL variable.
func djangoShim(o Options, cfg *pgconn.Config, port int) (dir, orig string, err error) {
	for _, a := range o.Command {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return "", "", errors.New("remove --settings from the command and set DJANGO_SETTINGS_MODULE instead, so dbscope can point the database at its proxy")
		}
	}
	orig = os.Getenv("DJANGO_SETTINGS_MODULE")
	if orig == "" {
		for _, a := range o.Command {
			if filepath.Base(a) != "manage.py" {
				continue
			}
			src, err := os.ReadFile(filepath.Join(o.Dir, a))
			if err != nil {
				return "", "", err
			}
			if m := settingsDefault.FindSubmatch(src); m != nil {
				orig = string(m[1])
			}
		}
	}
	if orig == "" {
		return "", "", errors.New("could not tell which Django settings module the project uses; set DJANGO_SETTINGS_MODULE")
	}
	dir, err = os.MkdirTemp("", "dbscope-django-")
	if err != nil {
		return "", "", err
	}
	src := fmt.Sprintf(`# Written by dbscope migrate capture: the project's settings with the default
# databases pointed at dbscope's recording proxy.
import importlib, os
globals().update({k: v for k, v in vars(importlib.import_module(os.environ["DBSCOPE_DJANGO_SETTINGS"])).items() if not k.startswith("__")})
# Every alias, so "migrate --database other" cannot reach a real database either.
DATABASES = {alias: {**db, "HOST": "127.0.0.1", "PORT": "%d", "NAME": %q, "USER": %q, "PASSWORD": os.environ.get("PGPASSWORD", ""),
    "OPTIONS": {**db.get("OPTIONS", {}), "sslmode": "disable"}} for alias, db in DATABASES.items()}
`, port, cfg.Database, cfg.User)
	if err := os.WriteFile(filepath.Join(dir, "dbscope_capture_settings.py"), []byte(src), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	return dir, orig, nil
}

func has(m map[string]string, k string) bool { _, ok := m[k]; return ok }

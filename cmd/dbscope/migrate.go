package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbscope/dbscope/internal/agent"
	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/report"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/migrate/stats"
	"github.com/dbscope/dbscope/internal/mysql"
	"github.com/dbscope/dbscope/internal/sqlite"
)

// Exit codes of migrate check and stats export (docs/API.md).
const (
	exitOK          = 0
	exitThreshold   = 1
	exitUsage       = 2
	exitUnavailable = 3
	exitCommand     = 4
)

const migrateUsage = `usage:
  dbscope migrate check PATH... [--dsn URL | --schema-dump FILE [--stats FILE]] [--pg-version N]
                        [--fail-on low|medium|high|critical] [--format text|json|sarif|github]
                        [--tx-mode auto|file|statement|none] [--assume-lock-timeout]
  dbscope migrate check PATH... --sqlite FILE | --sqlite-schema FILE
                        [--fail-on low|medium|high|critical] [--format text|json|sarif|github]
  dbscope migrate check PATH... --mysql URL | --mysql-version VERSION
                        [--fail-on low|medium|high|critical] [--format text|json|sarif|github]

  dbscope migrate capture --shadow URL [--dsn URL] [--out DIR] [--url-env NAME]...
                          [--allow-nonempty-shadow] -- COMMAND [ARGS...]

Analyzes migration files (or directories of them) against a base schema:
a live database (--dsn, or the DBSCOPE_DSN environment variable), or a
pg_dump --schema-only file optionally combined with a "dbscope stats export"
file. For SQLite the base is a database file (--sqlite, opened read-only) or a
schema script such as the output of sqlite3 app.db .schema (--sqlite-schema);
see docs/ENGINES.md. For MySQL and MariaDB the base is a live server
(--mysql mysql://user:password@host:3306/db, or the DBSCOPE_MYSQL_DSN
environment variable), or only a server version with no sizes
(--mysql-version 8.4, or mariadb-11.8). Exits 1 when any statement is rated at or above --fail-on (default
high), 2 on usage errors and 3 when the base schema cannot be read.

capture runs an ORM's migrate command (bin/rails db:migrate, python manage.py
migrate, alembic upgrade head, ...) against an empty throwaway --shadow
database through a recording proxy and writes the SQL it ran as one .sql file
per migration into --out, ready for migrate check. With --dsn, migrations that
database has already applied are left out. Exits 3 when the shadow cannot be
used and 4 when the command failed or never connected through dbscope.
See docs/ORM-CAPTURE.md.
`

const statsUsage = `usage:
  dbscope stats export --dsn URL [--schemas a,b] > stats.json

Writes relation sizes, row estimates, the server version and role/database
timeouts as JSON (no row data). The DSN can also come from DBSCOPE_DSN.
`

// statementTimeout bounds every catalog query the CLI runs.
const statementTimeout = 30 * time.Second

// parseInterleaved parses flags that may appear before or after positional
// arguments, returning the positionals.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		rest := fs.Args()
		if rest[0] == "--" {
			return append(pos, rest[1:]...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func dsnFrom(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("DBSCOPE_DSN")
}

// openPool connects with the CLI's limits; errors never contain the DSN.
func openPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("the DSN does not parse as a PostgreSQL URL") // never echo the DSN
	}
	pc.ConnConfig.RuntimeParams["client_encoding"] = "UTF8"
	pc.ConnConfig.RuntimeParams["application_name"] = "dbscope migrate"
	pc.ConnConfig.ConnectTimeout = 10 * time.Second
	pc.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, errors.New("could not create a connection pool")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("could not connect: " + conn.Classify(err).Message)
	}
	return pool, nil
}

func migrateCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "capture" {
		return captureCmd(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprint(stderr, migrateUsage)
		return exitUsage
	}
	fs := flag.NewFlagSet("migrate check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, migrateUsage) }
	dsnFlag := fs.String("dsn", "", "postgres:// URL of the base database (or DBSCOPE_DSN)")
	dumpPath := fs.String("schema-dump", "", "pg_dump --schema-only output to use as the base schema")
	statsPath := fs.String("stats", "", `"dbscope stats export" file with sizes for --schema-dump`)
	pgVersion := fs.String("pg-version", "", "PostgreSQL version for version-dependent rules (e.g. 17 or 170002)")
	failOn := fs.String("fail-on", "high", "exit 1 when a statement is rated at or above this")
	format := fs.String("format", "text", "text, json, sarif or github")
	txMode := fs.String("tx-mode", "auto", "auto, file, statement or none")
	assume := fs.Bool("assume-lock-timeout", false, "the deploy tool sets lock_timeout itself")
	sqliteFile := fs.String("sqlite", "", "SQLite database file to use as the base schema (opened read-only)")
	sqliteSchema := fs.String("sqlite-schema", "", "SQLite schema script (sqlite3 FILE .schema) to use as the base schema")
	mysqlURL := fs.String("mysql", "", "mysql:// URL of the base MySQL or MariaDB server (or DBSCOPE_MYSQL_DSN)")
	mysqlVersion := fs.String("mysql-version", "", "check MySQL migrations without a server, as this version (8.4, mariadb-11.8)")
	paths, err := parseInterleaved(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "dbscope migrate check: %s\n%s", msg, migrateUsage)
		return exitUsage
	}
	limit, ok := score.ParseRating(*failOn)
	if !ok {
		return usage("--fail-on must be low, medium, high or critical")
	}
	if !slices.Contains(report.Formats, *format) {
		return usage("--format must be text, json, sarif or github")
	}
	mode, err := ingest.ParseMode(*txMode)
	if err != nil {
		return usage("--tx-mode must be auto, file, statement or none")
	}
	version := 0
	if *pgVersion != "" {
		if version, err = parseVersion(*pgVersion); err != nil {
			return usage(err.Error())
		}
	}
	if len(paths) == 0 {
		return usage("at least one migration file or directory is required")
	}
	if *mysqlURL == "" && *mysqlVersion == "" && *dsnFlag == "" && *dumpPath == "" && *sqliteFile == "" && *sqliteSchema == "" {
		*mysqlURL = os.Getenv("DBSCOPE_MYSQL_DSN")
		if *mysqlURL != "" && os.Getenv("DBSCOPE_DSN") != "" {
			return usage("both DBSCOPE_DSN and DBSCOPE_MYSQL_DSN are set; pass --dsn or --mysql to choose")
		}
	}
	if *mysqlURL != "" || *mysqlVersion != "" {
		switch {
		case *mysqlURL != "" && *mysqlVersion != "":
			return usage("use either --mysql or --mysql-version, not both")
		case *dsnFlag != "" || *dumpPath != "" || *statsPath != "" || *pgVersion != "" || *txMode != "auto" || *assume || *sqliteFile != "" || *sqliteSchema != "":
			return usage("--mysql and --mysql-version take no PostgreSQL or SQLite options")
		}
		return checkMySQL(stdout, stderr, paths, *mysqlURL, *mysqlVersion, *format, limit)
	}
	if *sqliteFile != "" || *sqliteSchema != "" {
		switch {
		case *sqliteFile != "" && *sqliteSchema != "":
			return usage("use either --sqlite or --sqlite-schema, not both")
		case *dsnFlag != "" || *dumpPath != "" || *statsPath != "" || *pgVersion != "" || *txMode != "auto" || *assume:
			return usage("--sqlite and --sqlite-schema take no PostgreSQL options (--dsn, --schema-dump, --stats, --pg-version, --tx-mode, --assume-lock-timeout)")
		}
		return checkSQLite(stdout, stderr, paths, *sqliteFile, *sqliteSchema, *format, limit)
	}
	dsn := dsnFrom(*dsnFlag)
	switch {
	case *dumpPath != "" && *dsnFlag != "":
		return usage("use either --dsn or --schema-dump, not both")
	case *statsPath != "" && *dumpPath == "":
		return usage("--stats needs --schema-dump")
	case *dumpPath == "" && dsn == "":
		return usage("a base schema is required: --dsn URL (or DBSCOPE_DSN) or --schema-dump FILE")
	}
	cwd, _ := os.Getwd()
	inputs, err := ingest.ReadPaths(paths, ingest.RepoRoot(cwd))
	if err != nil {
		return usage(err.Error())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var base *migrate.Base
	if *dumpPath != "" {
		base, err = dumpBase(*dumpPath, *statsPath)
	} else {
		base, err = liveBase(ctx, dsn)
	}
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: base schema unavailable: %v\n", err)
		return exitUnavailable
	}
	if c, ok := base.Impact.(interface{ Close() }); ok {
		defer c.Close()
	}
	rep, err := migrate.Analyze(ctx, base, migrate.Request{Files: inputs, TxMode: mode, AssumeLockTimeout: *assume, PGVersion: version})
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %v\n", err)
		return exitUnavailable
	}
	if err := report.Write(stdout, *format, rep, agent.Version); err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %v\n", err)
		return exitUsage
	}
	if rep.Exceeds(limit) {
		return exitThreshold
	}
	return exitOK
}

func checkSQLite(stdout, stderr io.Writer, paths []string, file, schemaPath, format string, limit score.Rating) int {
	cwd, _ := os.Getwd()
	inputs, err := ingest.ReadPaths(paths, ingest.RepoRoot(cwd))
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %s\n%s", err, migrateUsage)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var base *sqlite.Base
	if file != "" {
		var db *sqlite.DB
		if db, err = sqlite.Open(file, 5*time.Second); err == nil {
			base, err = sqlite.ReadBase(ctx, db.DB)
			db.Close()
		}
	} else {
		var b []byte
		if b, err = os.ReadFile(schemaPath); err == nil {
			base = sqlite.BaseFromDump(string(b))
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: base schema unavailable: %v\n", err)
		return exitUnavailable
	}
	rep, err := sqlite.Analyze(ctx, base, inputs)
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %v\n", err)
		return exitUnavailable
	}
	if err := report.Write(stdout, format, rep, agent.Version); err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %v\n", err)
		return exitUsage
	}
	if rep.Exceeds(limit) {
		return exitThreshold
	}
	return exitOK
}

func checkMySQL(stdout, stderr io.Writer, paths []string, rawURL, version, format string, limit score.Rating) int {
	cwd, _ := os.Getwd()
	inputs, err := ingest.ReadPaths(paths, ingest.RepoRoot(cwd))
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %s\n%s", err, migrateUsage)
		return exitUsage
	}
	var base *mysql.Base
	if version != "" {
		srv, err := mysql.ParseServer(version)
		if err != nil {
			fmt.Fprintf(stderr, "dbscope migrate check: --mysql-version: %v\n%s", err, migrateUsage)
			return exitUsage
		}
		base = &mysql.Base{Server: srv, Tables: map[string]*mysql.TableInfo{}, Source: "none"}
	} else {
		p, password, err := mysql.ParseURL(rawURL)
		if err != nil {
			fmt.Fprintf(stderr, "dbscope migrate check: --mysql: %v\n%s", err, migrateUsage)
			return exitUsage
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		db, err := mysql.Open(p, password, 30*time.Second)
		if err == nil {
			base, err = mysql.ReadBase(ctx, db, nil, p.Database)
			db.Close()
		}
		if err != nil {
			fmt.Fprintf(stderr, "dbscope migrate check: base schema unavailable: %v\n", err)
			return exitUnavailable
		}
	}
	rep := mysql.Analyze(base, inputs)
	if err := report.Write(stdout, format, rep, agent.Version); err != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %v\n", err)
		return exitUsage
	}
	if rep.Exceeds(limit) {
		return exitThreshold
	}
	return exitOK
}

// parseVersion accepts a major version (14-18) or a server_version_num.
func parseVersion(s string) (int, error) {
	n, err := strconv.Atoi(s)
	switch {
	case err != nil:
	case n >= 14 && n <= 99:
		return n * 10000, nil
	case n >= 140000 && n < 1000000:
		return n, nil
	}
	return 0, errors.New("--pg-version must be a major version like 17 or a server_version_num like 170002")
}

func dumpBase(dumpPath, statsPath string) (*migrate.Base, error) {
	b, err := os.ReadFile(dumpPath)
	if err != nil {
		return nil, fmt.Errorf("read --schema-dump: %w", err)
	}
	var f *stats.File
	if statsPath != "" {
		r, err := os.Open(statsPath)
		if err != nil {
			return nil, fmt.Errorf("read --stats: %w", err)
		}
		defer r.Close()
		if f, err = stats.Decode(r); err != nil {
			return nil, err
		}
	}
	return migrate.DumpBase(string(b), f), nil
}

// poolBase keeps the pool open until the analysis finishes.
type poolBase struct {
	migrate.Impacter
	pool *pgxpool.Pool
}

func (p poolBase) Close() { p.pool.Close() }

func liveBase(ctx context.Context, dsn string) (*migrate.Base, error) {
	pool, err := openPool(ctx, dsn)
	if err != nil {
		return nil, err
	}
	b, err := migrate.LiveBase(ctx, pool, nil, statementTimeout)
	if err != nil {
		pool.Close()
		return nil, errors.New(conn.Classify(err).Message)
	}
	b.Impact = poolBase{Impacter: b.Impact, pool: pool}
	return b, nil
}

func statsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprint(stderr, statsUsage)
		return exitUsage
	}
	fs := flag.NewFlagSet("stats export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, statsUsage) }
	dsnFlag := fs.String("dsn", "", "postgres:// URL (or DBSCOPE_DSN)")
	schemas := fs.String("schemas", "", "comma-separated schemas (default: every schema the role can use)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	dsn := dsnFrom(*dsnFlag)
	if fs.NArg() > 0 || dsn == "" {
		fmt.Fprint(stderr, statsUsage)
		return exitUsage
	}
	var list []string
	for _, s := range strings.Split(*schemas, ",") {
		if s = strings.TrimSpace(s); s != "" {
			list = append(list, s)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := openPool(ctx, dsn)
	if err != nil {
		fmt.Fprintf(stderr, "dbscope stats export: %v\n", err)
		return exitUnavailable
	}
	defer pool.Close()
	f, warnings, err := stats.Export(ctx, pool, list, statementTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "dbscope stats export: %s\n", conn.Classify(err).Message)
		return exitUnavailable
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "dbscope stats export: warning: %s\n", w)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		fmt.Fprintf(stderr, "dbscope stats export: %v\n", err)
		return exitUnavailable
	}
	return exitOK
}

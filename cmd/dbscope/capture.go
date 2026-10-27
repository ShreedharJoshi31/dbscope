package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate/capture"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func captureCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, migrateUsage) }
	shadow := fs.String("shadow", "", "postgres:// URL of an empty, throwaway database the command will migrate")
	dsnFlag := fs.String("dsn", "", "postgres:// URL of the real database; its applied migrations are left out (or DBSCOPE_DSN)")
	out := fs.String("out", "dbscope-capture", "directory for the captured .sql files (must be empty or absent)")
	allow := fs.Bool("allow-nonempty-shadow", false, "allow a shadow database that already has tables")
	var urlEnv listFlag
	fs.Var(&urlEnv, "url-env", "also set this environment variable to the proxy URL (repeatable)")

	usage := func(msg string) int {
		fmt.Fprintf(stderr, "dbscope migrate capture: %s\n%s", msg, migrateUsage)
		return exitUsage
	}
	sep := slices.Index(args, "--")
	if sep < 0 || sep == len(args)-1 {
		return usage("give the migrate command after --")
	}
	if err := fs.Parse(args[:sep]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		return usage("unexpected argument " + fs.Arg(0) + " before --")
	}
	if *shadow == "" {
		return usage("--shadow is required")
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate capture: %v\n", err)
		return exitUnavailable
	}

	// Checked before anything runs: once the shadow is migrated a rerun needs a new one.
	if err := capture.CheckOut(*out); err != nil {
		fmt.Fprintf(stderr, "dbscope migrate capture: %v\n", err)
		return exitUnavailable
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	target := dsnFrom(*dsnFlag)
	fmt.Fprintf(stderr, "dbscope: running %q against the shadow database through a recording proxy\n", strings.Join(args[sep+1:], " "))
	run, err := capture.Capture(ctx, capture.Options{
		Shadow: *shadow, Target: target, Command: args[sep+1:], Dir: dir,
		URLEnv: urlEnv, AllowNonEmpty: *allow, Stdout: stdout, Stderr: stderr,
	})
	switch {
	case errors.Is(err, capture.ErrNoConnection):
		fmt.Fprintf(stderr, "dbscope migrate capture: %v\n", err)
		return exitCommand
	case err != nil:
		fmt.Fprintf(stderr, "dbscope migrate capture: %s\n", safeErr(err))
		return exitUnavailable
	}

	ms := run.Pending
	files, err := capture.Write(*out, ms, run.Unrecorded, capture.DrizzleNames(dir))
	if err != nil {
		fmt.Fprintf(stderr, "dbscope migrate capture: %v\n", err)
		return exitUnavailable
	}
	printCapture(stderr, run, ms, files, *out, target != "")
	if run.CommandErr != nil {
		fmt.Fprintf(stderr, "dbscope: the migrate command failed (%v); the files hold what it ran before failing\n", run.CommandErr)
		return exitCommand
	}
	return exitOK
}

func printCapture(w io.Writer, run *capture.Run, ms []capture.Migration, files []string, out string, filtered bool) {
	fw := "no known framework"
	if len(run.Migrations) > 0 && run.Migrations[0].Framework != "" {
		fw = string(run.Migrations[0].Framework)
	}
	fmt.Fprintf(w, "dbscope: captured %s (%s) over %s", plural(countKeys(run.Migrations), "migration"), fw, plural(run.Connections, "connection"))
	if filtered {
		fmt.Fprintf(w, "; %d not yet applied to --dsn", countKeys(ms))
	}
	fmt.Fprintf(w, "\ndbscope: wrote %s to %s\n", plural(len(files), "file"), out)
	for _, f := range files {
		fmt.Fprintf(w, "  %s\n", f)
	}
	fmt.Fprintf(w, "dbscope: left out %d queries, setup and bookkeeping statements", run.Dropped)
	if run.RolledBack > 0 {
		fmt.Fprintf(w, " and %d rolled-back statements", run.RolledBack)
	}
	fmt.Fprintln(w)
	for _, e := range run.Failed {
		fmt.Fprintf(w, "dbscope: failed on the shadow: %s\n  %s\n", e.Error, firstLine(e.SQL))
	}
	for _, n := range run.Notes {
		fmt.Fprintf(w, "dbscope: %s\n", n)
	}
	if len(files) > 0 {
		fmt.Fprintf(w, "next: dbscope migrate check %s --dsn <staging or replica URL>\n", out)
	}
}

func countKeys(ms []capture.Migration) int {
	n := 0
	for _, m := range ms {
		n += len(m.Keys)
	}
	return n
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// safeErr keeps connection errors from echoing a URL with its password.
func safeErr(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "://") {
		return conn.Classify(err).Message
	}
	return msg
}

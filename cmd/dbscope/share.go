package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/share"
	"github.com/dbscope/dbscope/internal/store"
)

const shareUsage = `usage:
  dbscope share export --connection NAME [--out FILE]
  dbscope share import --connection NAME [--replace] [--dry-run] FILE

Exports or imports a connection's canvas layouts, inferred foreign key
decisions and assistant glossary as one JSON file meant to be committed to the
application's repository (docs/SHARING.md). Import merges by default: entries
in the file win and local-only entries are kept; --replace makes the
connection's state exactly the file's. Connection names are matched without
regard to case.
`

func shareCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "export" && args[0] != "import") {
		fmt.Fprint(stderr, shareUsage)
		return exitUsage
	}
	sub := args[0]
	fs := flag.NewFlagSet("share "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, shareUsage) }
	name := fs.String("connection", "", "name of the connection, as shown in the UI")
	out := fs.String("out", "", "export: write to this file instead of standard output")
	replace := fs.Bool("replace", false, "import: make the connection's state exactly the file's")
	dryRun := fs.Bool("dry-run", false, "import: show what would change without changing anything")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(stderr, "dbscope share %s: %s\n", sub, fmt.Sprintf(format, a...))
		return code
	}
	switch {
	case *name == "":
		return fail(exitUsage, "--connection is required")
	case sub == "export" && len(pos) > 0, sub == "import" && len(pos) != 1:
		fmt.Fprint(stderr, shareUsage)
		return exitUsage
	}

	dir, err := configDir()
	if err != nil {
		return fail(exitUnavailable, "%v", err)
	}
	st, err := store.Open(filepath.Join(dir, "dbscope.db"))
	if err != nil {
		return fail(exitUnavailable, "%v", err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := profileByName(ctx, st, *name)
	if err != nil {
		return fail(exitUnavailable, "%v", err)
	}
	current, err := st.Shared(ctx, p.ID)
	if err != nil {
		return fail(exitUnavailable, "%v", err)
	}

	if sub == "export" {
		data := share.FromStore(current).Marshal()
		if *out == "" {
			stdout.Write(data)
			return exitOK
		}
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			return fail(exitUnavailable, "%v", err)
		}
		fmt.Fprintf(stderr, "dbscope: wrote %s\n", *out)
		return exitOK
	}

	if fi, err := os.Stat(pos[0]); err != nil {
		return fail(exitUnavailable, "%v", err)
	} else if fi.Size() > share.MaxBytes {
		return fail(exitUsage, "%s is larger than %d MiB", pos[0], share.MaxBytes>>20)
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return fail(exitUnavailable, "%v", err)
	}
	incoming, err := share.Parse(data)
	if err != nil {
		return fail(exitUsage, "%s: %v", pos[0], err)
	}
	mode := share.Merge
	if *replace {
		mode = share.Replace
	}
	var preview share.Preview
	plan := func(cur store.Shared) (store.Shared, error) {
		var result store.Shared
		preview, result = share.Plan(share.FromStore(cur), incoming, mode, nil)
		return result, share.Check(result)
	}
	verb := "Imported"
	if *dryRun {
		verb = "Would import"
		_, err = plan(current)
	} else {
		err = st.UpdateShared(ctx, p.ID, plan)
	}
	var rejected *share.CheckError
	switch {
	case errors.As(err, &rejected):
		return fail(exitUsage, "%v", err)
	case err != nil:
		return fail(exitUnavailable, "%v", err)
	}
	fmt.Fprintf(stdout, "%s %s into %s (%s):\n", verb, pos[0], p.Name, mode)
	for _, s := range []struct {
		label string
		sec   share.Section
	}{{"table positions", preview.Tables}, {"foreign key decisions", preview.ForeignKeys}, {"glossary terms", preview.Glossary}} {
		fmt.Fprintf(stdout, "  %-22s %s\n", s.label, counts(s.sec))
		for _, it := range s.sec.Items {
			if it.Change == share.Changed && it.Detail != "" {
				fmt.Fprintf(stdout, "    changed %s: %s\n", it.Key, it.Detail)
			}
		}
	}
	if !*dryRun {
		fmt.Fprintln(stdout, "If dbscope is open in a browser, reload it so it shows the import instead of saving over it.")
	}
	return exitOK
}

func counts(s share.Section) string {
	var parts []string
	for _, c := range []string{share.Added, share.Changed, share.Unchanged, share.Kept, share.Removed} {
		if n := s.Counts[c]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, c))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func profileByName(ctx context.Context, st *store.Store, name string) (conn.Profile, error) {
	ps, err := st.ListProfiles(ctx)
	if err != nil {
		return conn.Profile{}, err
	}
	var names []string
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return conn.Profile{}, errors.New("there are no saved connections; add one in the UI first")
	}
	return conn.Profile{}, fmt.Errorf("no connection named %q (saved: %s)", name, strings.Join(names, ", "))
}

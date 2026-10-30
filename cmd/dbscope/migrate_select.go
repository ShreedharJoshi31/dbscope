package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/history"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// selection narrows the files migrate check analyzes: --since keeps those
// changed since a git ref, --pending drops those the migration tool's history
// shows applied.
type selection struct {
	since   string
	pending bool
}

// read reads the paths and applies --since.
func (s selection) read(paths []string) ([]ingest.Input, error) {
	cwd, _ := os.Getwd()
	root := ingest.RepoRoot(cwd)
	inputs, err := ingest.ReadPaths(paths, root)
	if err != nil || s.since == "" {
		return inputs, err
	}
	changed, err := changedSince(cwd, s.since, paths)
	if err != nil {
		return nil, err
	}
	var out []ingest.Input
	for _, in := range inputs {
		name := filepath.FromSlash(in.Name)
		if !filepath.IsAbs(name) {
			name = filepath.Join(root, name)
		}
		// Flyway .sql.conf and atlas.sum describe how the other files run, so they stay.
		lower := strings.ToLower(in.Name)
		if changed[realPath(name)] || strings.HasSuffix(lower, ".sql.conf") || filepath.Base(lower) == "atlas.sum" {
			out = append(out, in)
		}
	}
	return out, nil
}

// changedSince returns the real paths of files that differ between ref and the
// working tree (git diff --name-only ref --) or are untracked, under paths.
func changedSince(dir, ref string, paths []string) (map[string]bool, error) {
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return "", errors.New(msg)
		}
		return string(out), nil
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("--since needs a git repository: %s", err)
	}
	top = strings.TrimSpace(top)
	if strings.HasPrefix(ref, "-") {
		return nil, fmt.Errorf("--since %q is not a git ref", ref)
	}
	diff, err := git(append([]string{"diff", "--name-only", ref, "--"}, paths...)...)
	if err != nil {
		return nil, fmt.Errorf("--since %s: %s", ref, err)
	}
	untracked, err := git(append([]string{"ls-files", "--others", "--exclude-standard", "--full-name", "--"}, paths...)...)
	if err != nil {
		return nil, fmt.Errorf("--since %s: %s", ref, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(diff+untracked, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[realPath(filepath.Join(top, filepath.FromSlash(line)))] = true
		}
	}
	return out, nil
}

// realPath resolves symbolic links (macOS's /var is /private/var) so git's and the walk's paths compare equal.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// dropApplied applies --pending: it reads the history over q, says on stderr
// which files were skipped, and returns the rest and what goes in the report.
func (s selection) dropApplied(ctx context.Context, stderr io.Writer, q history.Querier, d history.Dialect, inputs []ingest.Input) ([]ingest.Input, history.Result) {
	if !s.pending {
		return inputs, history.Result{}
	}
	res := history.SQL(ctx, q, d, inputs)
	if p := res.Pending; p != nil {
		fmt.Fprintf(stderr, "dbscope migrate check: %s (%s) has %d of these files applied; skipped:\n", p.Tool, p.Table, len(p.Applied))
		for _, f := range p.Applied {
			fmt.Fprintf(stderr, "  %s\n", f)
		}
		for _, f := range p.Changed {
			fmt.Fprintf(stderr, "dbscope migrate check: %s was applied but has changed since; checking it again\n", f)
		}
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(stderr, "dbscope migrate check: warning: %s\n", w)
	}
	return res.Keep(inputs), res
}

// noteHistory puts what --pending read into the report.
func noteHistory(rep *migrate.Report, res history.Result) {
	rep.Pending = res.Pending
	rep.Warnings = append(rep.Warnings, res.Warnings...)
}

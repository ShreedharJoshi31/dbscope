package ingest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// MaxFileBytes caps one migration file (the API's limit, applied to the CLI too).
const MaxFileBytes = 1 << 20

// ReadPaths reads files and directories (recursively) for the CLI. Names
// are slash-separated and relative to root (the repository root, for
// repo-relative SARIF URIs) when the file is under it. Only *.sql,
// *.sql.conf and atlas.sum are read from directories. Files named on the
// command line keep their order; files found in a directory are sorted
// naturally, which Load refines for detected frameworks. A golang-migrate
// up file or Flyway versioned file named on its own brings its down or undo
// file from the same directory along, for the rollback check.
func ReadPaths(paths []string, root string) ([]Input, error) {
	var out []Input
	add := func(p string) error {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if info.Size() > MaxFileBytes {
			return fmt.Errorf("%s is larger than 1 MiB", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, Input{Name: relName(p, root), SQL: string(b)})
		return nil
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if err := add(p); err != nil {
				return nil, err
			}
			for _, sib := range downSiblings(p) {
				if !slices.Contains(paths, sib) {
					if err := add(sib); err != nil {
						return nil, err
					}
				}
			}
			continue
		}
		var found []string
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); path != p && (strings.HasPrefix(name, ".") || name == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			name := strings.ToLower(d.Name())
			if strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".sql.conf") || name == "atlas.sum" {
				found = append(found, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		slices.SortFunc(found, func(a, b string) int { return Natural(filepath.ToSlash(a), filepath.ToSlash(b)) })
		for _, f := range found {
			if err := add(f); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func relName(p, root string) string {
	abs, err := filepath.Abs(p)
	if err == nil && root != "" {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// RepoRoot returns the nearest directory at or above dir that holds .git,
// or dir itself.
func RepoRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs
		}
		d = parent
	}
}

// downSiblings finds the down file of a golang-migrate up file, or the undo
// file of a Flyway versioned file, next to it.
func downSiblings(p string) []string {
	base := filepath.Base(p)
	var match func(string) bool
	switch {
	case golangUp.MatchString(base):
		key := golangKey(filepath.ToSlash(p))
		match = func(n string) bool {
			return golangDown.MatchString(n) && golangKey(filepath.ToSlash(filepath.Join(filepath.Dir(p), n))) == key
		}
	case flywayV.MatchString(base):
		key := flywayKey(base)
		match = func(n string) bool { return flywayU.MatchString(n) && flywayKey(n) == key }
	default:
		return nil
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && match(e.Name()) {
			out = append(out, filepath.Join(filepath.Dir(p), e.Name()))
		}
	}
	return out
}

package main

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/dbscope/dbscope/internal/migrate"
	codeusage "github.com/dbscope/dbscope/internal/migrate/usage"
)

// codeDir is migrate check's --code folder and codeSkip its base schema files, package variables so each
// engine's check needs no new parameter.
var (
	codeDir  string
	codeSkip []string
)

// withUsages searches codeDir, leaving out the migration paths, for what rep drops or renames.
func withUsages(rep *migrate.Report, paths []string) {
	if codeDir == "" {
		return
	}
	skip := make([]string, 0, len(paths)+len(codeSkip))
	for _, p := range append(slices.Clone(paths), codeSkip...) {
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			skip = append(skip, abs)
		}
	}
	codeusage.Apply(context.Background(), rep, codeDir, skip)
}

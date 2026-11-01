// Package usage finds lines of application code that still name what a
// migration drops or renames, which the database itself cannot show. It only
// reads the project folder: it follows no symlinks and runs nothing in it.
package usage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// RuleID is the finding added to the statement that drops or renames a target still named in the project.
const RuleID = "DBS-STILL-USED"

const (
	maxPerTarget = 20
	maxText      = 200
	binaryProbe  = 8 << 10
)

// Target is a relation or column the migration drops or renames.
type Target struct {
	// Name is how the report names it: "public.customers" or "public.customers.email".
	Name string
	// Ident is what a line must name: the bare relation or column name.
	Ident string
	// Table is the bare relation name of a column; a column is reported only
	// in files that also name the table or its model.
	Table string
	// Found is set by Search: how many lines name the target, of which at most 20 are returned.
	Found int
}

// Budget bounds a search.
type Budget struct {
	MaxFiles     int
	MaxFileBytes int64
	Timeout      time.Duration
	// Skip are paths left out: absolute files or folders, or migration file
	// names relative to some folder, which skip any project file ending in them.
	Skip []string
}

// DefaultBudget is what the agent and the CLI search with.
var DefaultBudget = Budget{MaxFiles: 50_000, MaxFileBytes: 1 << 20, Timeout: 5 * time.Second}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, ".next": true, ".venv": true, "__pycache__": true, "coverage": true}

// Apply searches dir for every relation and column rep drops or renames,
// fills rep.Usages, and adds DBS-STILL-USED to the statement that drops or
// renames each one still named. skip lists the migration folder and files.
// An empty dir does nothing.
func Apply(ctx context.Context, rep *migrate.Report, dir string, skip []string) {
	if dir == "" || rep == nil {
		return
	}
	targets := Targets(rep)
	if len(targets) == 0 {
		return
	}
	b := DefaultBudget
	b.Skip = skip
	usages, warnings := Search(ctx, dir, targets, b)
	rep.Usages = usages
	rep.Warnings = append(rep.Warnings, warnings...)
	for _, t := range targets {
		if t.Found == 0 {
			continue
		}
		var first migrate.Usage
		for _, u := range usages {
			if u.Name == t.Name {
				first = u
				break
			}
		}
		i := statementFor(rep, t)
		if i < 0 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s is still named in the project, but no statement was found that drops or renames it", t.Name))
			continue
		}
		places := "places"
		if t.Found == 1 {
			places = "place"
		}
		s := &rep.Statements[i]
		s.Findings = append(s.Findings, migrate.Finding{RuleID: RuleID, Rating: score.High,
			Message:         fmt.Sprintf("%s is still named in %d %s in the project, e.g. %s:%d", t.Name, t.Found, places, first.File, first.Line),
			SafeAlternative: "expand and contract: stop using it in one release, drop it in the next"})
		s.Rating = score.Max(s.Rating, score.High)
		rep.Rating = score.Max(rep.Rating, score.High)
	}
}

// Names are the names of the migration files an analysis was given, for Budget.Skip.
func Names(files []ingest.Input) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

// Targets are the relations rep's diff drops (a renamed relation shows as
// dropped and added) and the columns it drops or renames.
func Targets(rep *migrate.Report) []Target {
	whole := rep.Engine == "mongodb" || rep.Engine == "elasticsearch" || rep.Engine == "opensearch"
	bare := func(name string) string {
		if whole {
			return name // collection and index names may hold dots
		}
		return lastPart(name)
	}
	var out []Target
	seen := map[string]bool{}
	add := func(t Target) {
		if t.Ident != "" && !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	for _, r := range rep.Diff.Dropped {
		add(Target{Name: r, Ident: bare(r)})
	}
	for _, c := range rep.Diff.Columns {
		if c.Change == "dropped" || c.Change == "renamed" {
			add(Target{Name: c.Relation + "." + c.Column, Ident: c.Column, Table: bare(c.Relation)})
		}
	}
	return out
}

// lastPart is the last segment of a qualified name, without quotes.
func lastPart(name string) string {
	if strings.HasSuffix(name, `"`) {
		if i := strings.LastIndex(name[:len(name)-1], `"`); i >= 0 {
			return strings.ReplaceAll(name[i+1:len(name)-1], `""`, `"`)
		}
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// statementFor is the first statement that names t and drops, renames,
// deletes or unsets something, else the first that names it, else -1.
func statementFor(rep *migrate.Report, t Target) int {
	ident, table := lower([]byte(t.Ident)), lower([]byte(t.Table))
	fallback := -1
	for i, s := range rep.Statements {
		text := lower([]byte(s.SQL + "\n" + s.Kind))
		if !hasIdent(text, ident) || (len(table) > 0 && !hasIdent(text, table)) {
			continue
		}
		for _, verb := range []string{"drop", "rename", "delete", "unset"} {
			if bytes.Contains(text, []byte(verb)) {
				return i
			}
		}
		if fallback < 0 {
			fallback = i
		}
	}
	return fallback
}

type matcher struct {
	t      *Target
	ident  []byte
	models [][]byte // for a column: names of its table, one of which the file must name
}

// Search walks dir and returns the lines naming each target (at most 20 per
// target, in walk order), setting each target's Found to the full count.
// Warnings say what was cut short or could not be read.
func Search(ctx context.Context, dir string, targets []Target, b Budget) ([]migrate.Usage, []string) {
	var warnings []string
	root, err := filepath.EvalSymlinks(dir)
	if err == nil {
		var st os.FileInfo
		if st, err = os.Stat(root); err == nil && !st.IsDir() {
			err = errors.New("not a folder")
		}
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("the project folder %s was not searched: %v", dir, err)}
	}
	ms := make([]matcher, len(targets))
	for i := range targets {
		t := &targets[i]
		t.Found = 0
		ms[i] = matcher{t: t, ident: lower([]byte(t.Ident))}
		if t.Table != "" {
			for _, m := range modelNames(t.Table) {
				ms[i].models = append(ms[i].models, []byte(m))
			}
		}
	}
	absSkip, nameSkip := map[string]bool{}, []string{}
	for _, s := range b.Skip {
		switch {
		case s == "":
		case filepath.IsAbs(s):
			absSkip[filepath.Clean(s)] = true
			if r, err := filepath.EvalSymlinks(s); err == nil {
				absSkip[r] = true
			}
		default:
			nameSkip = append(nameSkip, filepath.ToSlash(filepath.Clean(s)))
		}
	}
	skipped := func(path, rel string) bool {
		if absSkip[path] {
			return true
		}
		for _, n := range nameSkip {
			if rel == n || strings.HasSuffix(rel, "/"+n) {
				return true
			}
		}
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()
	// The context's timer goroutine may not have run yet when the deadline
	// passes, so the clock is checked too.
	deadline := time.Now().Add(b.Timeout)
	hits := make([][]migrate.Usage, len(ms))
	files, unreadable := 0, 0
	var cut string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			unreadable++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			cut = fmt.Sprintf("the project search stopped after %s with %d files read; files after them were not searched", b.Timeout, files)
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || skipped(path, rel)) {
				return fs.SkipDir
			}
			return nil
		}
		// Symlinks, sockets and devices are never opened.
		if !d.Type().IsRegular() || skipped(path, rel) {
			return nil
		}
		if files >= b.MaxFiles {
			cut = fmt.Sprintf("the project search stopped at %d files; files after them were not searched", b.MaxFiles)
			return fs.SkipAll
		}
		files++
		content, err := readFile(path, b.MaxFileBytes)
		if err != nil {
			unreadable++
			return nil
		}
		if content != nil {
			scan(content, rel, ms, hits)
		}
		return nil
	})
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("the project folder %s was not searched: %v", dir, walkErr))
	}
	if cut != "" {
		warnings = append(warnings, cut)
	}
	if unreadable > 0 {
		warnings = append(warnings, fmt.Sprintf("%d files or folders of the project could not be read and were not searched", unreadable))
	}
	var out []migrate.Usage
	for _, h := range hits {
		out = append(out, h...)
	}
	return out, warnings
}

// readFile returns a regular file's content, or nil for one over limit or binary (a NUL in its first 8 KiB).
func readFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit || bytes.IndexByte(content[:min(len(content), binaryProbe)], 0) >= 0 {
		return nil, nil
	}
	return content, nil
}

func scan(content []byte, rel string, ms []matcher, hits [][]migrate.Usage) {
	low := lower(content)
	var live []int
	for i, m := range ms {
		if !hasIdent(low, m.ident) {
			continue
		}
		if m.models != nil && !anyIdent(low, m.models) {
			continue
		}
		live = append(live, i)
	}
	if len(live) == 0 {
		return
	}
	line, start := 1, 0
	for start <= len(content) {
		end := bytes.IndexByte(content[start:], '\n')
		if end < 0 {
			end = len(content)
		} else {
			end += start
		}
		for _, i := range live {
			if !hasIdent(low[start:end], ms[i].ident) {
				continue
			}
			ms[i].t.Found++
			if len(hits[i]) < maxPerTarget {
				hits[i] = append(hits[i], migrate.Usage{Name: ms[i].t.Name, File: rel, Line: line, Text: clip(string(content[start:end]))})
			}
		}
		line++
		start = end + 1
	}
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxText {
		return s
	}
	n := 0
	for i := range s {
		if n == maxText {
			return s[:i]
		}
		n++
	}
	return s
}

// modelNames are the lowercase names a file may use for a table: the table,
// its singular and plural, and both without underscores (CamelCase model names).
func modelNames(table string) []string {
	t := string(lower([]byte(table)))
	set := map[string]bool{t: true}
	switch {
	case strings.HasSuffix(t, "ies") && len(t) > 3:
		set[t[:len(t)-3]+"y"] = true
	case strings.HasSuffix(t, "sses"), strings.HasSuffix(t, "xes"), strings.HasSuffix(t, "ches"), strings.HasSuffix(t, "shes"):
		set[t[:len(t)-2]] = true
	case strings.HasSuffix(t, "ss"):
		set[t+"es"] = true
	case strings.HasSuffix(t, "s"):
		set[t[:len(t)-1]] = true
	case strings.HasSuffix(t, "y") && len(t) > 1 && !strings.ContainsRune("aeiou", rune(t[len(t)-2])):
		set[t[:len(t)-1]+"ies"] = true
	default:
		set[t+"s"] = true
	}
	var out []string
	for n := range set {
		out = append(out, n)
		if strings.Contains(n, "_") {
			out = append(out, strings.ReplaceAll(n, "_", ""))
		}
	}
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// hasIdent reports whether s holds needle with no identifier character
// ([A-Za-z0-9_]) right before or after it. Both are lowercase.
func hasIdent(s, needle []byte) bool {
	if len(needle) == 0 {
		return false
	}
	for off := 0; ; {
		i := bytes.Index(s[off:], needle)
		if i < 0 {
			return false
		}
		i += off
		j := i + len(needle)
		if (i == 0 || !isIdentByte(s[i-1])) && (j == len(s) || !isIdentByte(s[j])) {
			return true
		}
		off = i + 1
	}
}

func anyIdent(s []byte, needles [][]byte) bool {
	for _, n := range needles {
		if hasIdent(s, n) {
			return true
		}
	}
	return false
}

// lower lowercases ASCII letters only, so byte offsets stay those of the original.
func lower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

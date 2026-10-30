package ingest

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Tools whose migrations carry a rollback (a "down").
const (
	ToolGolangMigrate = "golang-migrate"
	ToolFlyway        = "flyway"
	ToolGoose         = "goose"
	ToolDbmate        = "dbmate"
	ToolMigrateMongo  = "migrate-mongo"
)

// Down is the rollback of one up file.
type Down struct {
	Tool string
	// Name is the file holding the rollback: a separate file, or the up
	// file itself for tools that keep both in one file.
	Name string
	// Text is the rollback alone. Every other byte of Name is blanked, so
	// lines and columns match the file. Empty when Missing.
	Text string
	// Missing: the tool supports rollbacks and this up file has none.
	Missing bool
}

// Paired is the input split into forward migrations and their rollbacks.
type Paired struct {
	// Forward holds the inputs to analyze as migrations, without down
	// files and with down sections blanked.
	Forward []Input
	// Downs is keyed by up file name; a file whose tool has no rollbacks
	// has no entry.
	Downs    map[string]*Down
	Warnings []string
}

var (
	// golang-migrate: {version}_{title}.up.{ext} and .down.{ext}, paired by
	// version (https://github.com/golang-migrate/migrate/blob/master/MIGRATIONS.md).
	golangUp   = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)
	golangDown = regexp.MustCompile(`^(\d+)_.*\.down\.sql$`)
	// Flyway undo migrations U{version}__{desc}.sql undo the versioned
	// migration of the same version (Teams edition,
	// https://documentation.red-gate.com/fd/undo-migrations-277578901.html).
	flywayVersion = regexp.MustCompile(`^[VU](\d+(?:[._]\d+)*)__.*\.sql$`)
	// goose: `-- +goose Up` and `-- +goose Down` sections in one file
	// (https://pressly.github.io/goose/documentation/annotations/).
	gooseMark = regexp.MustCompile(`(?im)^[ \t]*--[ \t]*\+goose[ \t]+(up|down)\b.*$`)
	// dbmate: `-- migrate:up` and `-- migrate:down` sections in one file
	// (https://github.com/amacneil/dbmate#creating-migrations).
	dbmateMark = regexp.MustCompile(`(?im)^[ \t]*--[ \t]*migrate:(up|down)\b.*$`)
)

// Pair finds each up file's rollback. Down files and down sections are
// left out of Forward so they are never analyzed as migrations.
func Pair(inputs []Input) Paired {
	p := Paired{Downs: map[string]*Down{}}
	golangDowns := map[string]Input{}
	flywayUndos := map[string]Input{}
	used := map[string]bool{}
	var rest []Input
	for _, in := range inputs {
		base := path.Base(in.Name)
		switch {
		case golangDown.MatchString(base):
			golangDowns[golangKey(in.Name)] = in
		case flywayU.MatchString(base):
			flywayUndos[flywayKey(base)] = in
		default:
			rest = append(rest, in)
		}
	}
	for _, in := range rest {
		base := path.Base(in.Name)
		lower := strings.ToLower(base)
		switch {
		case golangUp.MatchString(base):
			d := &Down{Tool: ToolGolangMigrate, Missing: true}
			if dn, ok := golangDowns[golangKey(in.Name)]; ok {
				d.Name, d.Text, d.Missing = dn.Name, dn.SQL, false
				used[dn.Name] = true
			}
			p.Downs[in.Name] = d
		case flywayV.MatchString(base) && len(flywayUndos) > 0:
			d := &Down{Tool: ToolFlyway, Missing: true}
			if u, ok := flywayUndos[flywayKey(base)]; ok {
				d.Name, d.Text, d.Missing = u.Name, u.SQL, false
				used[u.Name] = true
			}
			p.Downs[in.Name] = d
		case strings.HasSuffix(lower, ".sql") && hasMark(in.SQL, gooseMark, "up"):
			in = splitSections(in, ToolGoose, gooseMark, &p)
		case strings.HasSuffix(lower, ".sql") && hasMark(in.SQL, dbmateMark, "up"):
			in = splitSections(in, ToolDbmate, dbmateMark, &p)
		case strings.HasSuffix(lower, ".js"):
			in = splitMigrateMongo(in, &p)
		}
		p.Forward = append(p.Forward, in)
	}
	for _, m := range []map[string]Input{golangDowns, flywayUndos} {
		for _, in := range m {
			if !used[in.Name] {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s has no matching up migration in the files checked; it was not checked", in.Name))
			}
		}
	}
	slices.Sort(p.Warnings)
	return p
}

func golangKey(name string) string {
	v := golangMigrate.FindStringSubmatch(path.Base(name))[1]
	n, err := strconv.ParseUint(v, 10, 64)
	if err == nil {
		v = strconv.FormatUint(n, 10)
	}
	return path.Dir(name) + "\x00" + v
}

// flywayKey normalises a version as Flyway compares them: "1.0" equals "1".
func flywayKey(base string) string {
	parts := strings.FieldsFunc(flywayVersion.FindStringSubmatch(base)[1], func(r rune) bool { return r == '.' || r == '_' })
	nums := make([]string, len(parts))
	for i, s := range parts {
		n, _ := strconv.ParseInt(s, 10, 64)
		nums[i] = strconv.FormatInt(n, 10)
	}
	for len(nums) > 1 && nums[len(nums)-1] == "0" {
		nums = nums[:len(nums)-1]
	}
	return strings.Join(nums, ".")
}

func hasMark(text string, mark *regexp.Regexp, which string) bool {
	for _, m := range mark.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(m[1], which) {
			return true
		}
	}
	return false
}

// splitSections moves a file's down section into its Down and blanks it in the forward file.
func splitSections(in Input, tool string, mark *regexp.Regexp, p *Paired) Input {
	d := &Down{Tool: tool, Name: in.Name, Missing: true}
	p.Downs[in.Name] = d
	ms := mark.FindAllStringSubmatchIndex(in.SQL, -1)
	for i, m := range ms {
		if !strings.EqualFold(in.SQL[m[2]:m[3]], "down") {
			continue
		}
		end := len(in.SQL)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		d.Missing = false
		d.Text = keepOnly(in.SQL, m[0], end)
		in.SQL = blank(in.SQL, m[0], end)
		break
	}
	return in
}

// splitMigrateMongo handles migrate-mongo files, which export up(db, client)
// and down(db, client) functions
// (https://github.com/seppevs/migrate-mongo#creating-a-new-migration-script).
func splitMigrateMongo(in Input, p *Paired) Input {
	if _, _, ok := jsFunction(in.SQL, "up"); !ok {
		return in
	}
	d := &Down{Tool: ToolMigrateMongo, Name: in.Name, Missing: true}
	p.Downs[in.Name] = d
	if start, end, ok := jsFunction(in.SQL, "down"); ok {
		d.Missing = false
		d.Text = keepOnly(in.SQL, start, end)
		in.SQL = blank(in.SQL, start, end)
	}
	return in
}

// blank replaces s[start:end] with spaces, keeping newlines.
func blank(s string, start, end int) string {
	b := []byte(s)
	for i := start; i < end; i++ {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

// keepOnly blanks everything outside s[start:end].
func keepOnly(s string, start, end int) string {
	return blank(blank(s, 0, start), end, len(s))
}

// jsFunction finds a top-level function named name in a migrate-mongo
// module: `name(db) {...}` or `name: ...` in the exported object, or
// `exports.name = ...`. It returns the byte range from the name to the end
// of the function.
func jsFunction(src, name string) (start, end int, ok bool) {
	depth := 0
	for i := 0; i < len(src); {
		if j := skipJSNonCode(src, i); j > i {
			i = j
			continue
		}
		c := src[i]
		switch {
		case c == '{' || c == '(' || c == '[':
			depth++
		case c == '}' || c == ')' || c == ']':
			depth--
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdent(src[j]) {
				j++
			}
			if src[i:j] == name && depth <= 1 && (i == 0 || !isIdent(src[i-1])) && memberOK(src, i) {
				k := skipSpace(src, j)
				if k < len(src) && (src[k] == '(' || src[k] == ':' || (src[k] == '=' && (k+1 >= len(src) || src[k+1] != '='))) {
					return i, jsFunctionEnd(src, k), true
				}
			}
			i = j
			continue
		}
		i++
	}
	return 0, 0, false
}

// memberOK rejects `x.down` unless x is exports or module.exports.
func memberOK(src string, i int) bool {
	k := i - 1
	for k >= 0 && (src[k] == ' ' || src[k] == '\t') {
		k--
	}
	if k < 0 || src[k] != '.' {
		return true
	}
	return strings.HasSuffix(strings.TrimRight(src[:k], " \t"), "exports")
}

// jsFunctionEnd returns where the function starting at i ends: after its
// first top-level block, or at a top-level ',' or ';' or closing bracket.
func jsFunctionEnd(src string, i int) int {
	depth := 0
	for i < len(src) {
		if j := skipJSNonCode(src, i); j > i {
			i = j
			continue
		}
		switch src[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
			if depth < 0 {
				return i
			}
			if depth == 0 && src[i] == '}' {
				return i + 1
			}
		case ',', ';':
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return len(src)
}

// skipJSNonCode returns the index after a string, template literal or
// comment starting at i, or i.
func skipJSNonCode(src string, i int) int {
	c := src[i]
	switch {
	case c == '\'' || c == '"' || c == '`':
		for j := i + 1; j < len(src); j++ {
			switch src[j] {
			case '\\':
				j++
			case c:
				return j + 1
			}
		}
		return len(src)
	case c == '/' && i+1 < len(src) && src[i+1] == '/':
		if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
			return i + j
		}
		return len(src)
	case c == '/' && i+1 < len(src) && src[i+1] == '*':
		if j := strings.Index(src[i+2:], "*/"); j >= 0 {
			return i + 2 + j + 2
		}
		return len(src)
	}
	return i
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdent(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

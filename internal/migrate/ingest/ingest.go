// Package ingest turns migration files into an ordered list of statements,
// each with the transaction context it will run in.
//
// Ordering (docs/research-migrations.md section 5):
//   - Flyway: V<version>__<desc>.sql by numeric version, then R__ repeatables by description;
//   - golang-migrate: <version>_<title>.up.sql by version (.down.sql skipped);
//   - Prisma: migrations/<timestamp>_<name>/migration.sql by directory name;
//   - otherwise the order given (API) or natural, numeric-aware filename order (CLI directories).
//
// Transaction context per statement is one of none, implicit, explicit or
// framework, from explicit BEGIN/COMMIT in the file, `-- atlas:txmode none`,
// a Flyway `<file>.sql.conf` with executeInTransaction=false, the
// golang-migrate/Prisma rule that a multi-statement file runs as one
// implicit transaction, and the txMode override.
package ingest

import (
	"cmp"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/dbscope/dbscope/internal/migrate/parse"
)

// Tx is a statement's transaction context.
type Tx string

const (
	TxNone      Tx = "none"
	TxImplicit  Tx = "implicit"
	TxExplicit  Tx = "explicit"
	TxFramework Tx = "framework"
)

// Mode overrides transaction detection.
type Mode string

const (
	ModeAuto      Mode = "auto"
	ModeFile      Mode = "file"      // every file runs in one transaction
	ModeStatement Mode = "statement" // every statement commits on its own
	ModeNone      Mode = "none"      // no wrapping; only explicit BEGIN/COMMIT
)

// ParseMode validates a txMode value ("" is auto).
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", ModeAuto:
		return ModeAuto, nil
	case ModeFile, ModeStatement, ModeNone:
		return Mode(s), nil
	}
	return "", fmt.Errorf("txMode must be one of auto, file, statement, none")
}

// Framework is the detected migration tool.
type Framework string

const (
	Flyway        Framework = "flyway"
	GolangMigrate Framework = "golang-migrate"
	Prisma        Framework = "prisma"
	Atlas         Framework = "atlas"
	Plain         Framework = "plain"
)

// Input is one file as given: Name is its path (slash-separated, relative
// to the repository when known).
type Input struct {
	Name string
	SQL  string
}

// Statement is a parsed statement with its transaction context.
type Statement struct {
	parse.Statement
	Tx Tx
}

// File is one migration file, normalised and parsed.
type File struct {
	Name       string
	Framework  Framework
	Statements []Statement
	Meta       []parse.Meta
	// Wrapped: the tool runs the whole file in one transaction.
	Wrapped bool
	// WrapTx is the context of statements outside explicit BEGIN/COMMIT.
	WrapTx Tx
}

// Set is the ordered migration.
type Set struct {
	Files     []File
	Framework Framework
	Warnings  []string
}

// Options control loading.
type Options struct {
	Mode Mode
	// KeepOrder keeps the given order when no framework is detected
	// (the API); otherwise plain files are sorted naturally.
	KeepOrder bool
}

var (
	flywayV       = regexp.MustCompile(`^V(\d+(?:[._]\d+)*)__.*\.sql$`)
	flywayR       = regexp.MustCompile(`^R__(.*)\.sql$`)
	flywayU       = regexp.MustCompile(`^U\d+(?:[._]\d+)*__.*\.sql$`)
	golangMigrate = regexp.MustCompile(`^(\d+)_.*\.(up|down)\.sql$`)
	prismaPath    = regexp.MustCompile(`(?:^|/)migrations/([^/]+)/migration\.sql$`)
	atlasTxMode   = regexp.MustCompile(`(?m)^\s*--\s*atlas:txmode\s+(\w+)`)
	confInTx      = regexp.MustCompile(`(?m)^\s*executeInTransaction\s*=\s*(\w+)`)
)

// Normalize strips a UTF-8 BOM and turns CRLF and lone CR into LF.
func Normalize(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Load orders, parses and annotates the inputs.
func Load(inputs []Input, opts Options) *Set {
	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}
	set := &Set{}
	confs := map[string]string{}
	atlasSum := map[string]bool{}
	var sqlFiles []Input
	seen := map[string]string{}
	for _, in := range inputs {
		lower := strings.ToLower(in.Name)
		if prev, ok := seen[lower]; ok && prev != in.Name {
			set.Warnings = append(set.Warnings, fmt.Sprintf("%s and %s differ only in letter case; they collide on case-insensitive file systems", prev, in.Name))
		}
		seen[lower] = in.Name
		base := path.Base(in.Name)
		switch {
		case strings.HasSuffix(base, ".sql.conf"):
			confs[strings.TrimSuffix(in.Name, ".conf")] = in.SQL
		case base == "atlas.sum":
			atlasSum[path.Dir(in.Name)] = true
		case strings.HasSuffix(strings.ToLower(base), ".sql"):
			sqlFiles = append(sqlFiles, in)
		}
	}
	set.Framework = detect(sqlFiles, atlasSum)
	ordered := order(set.Framework, sqlFiles, opts.KeepOrder)
	for _, in := range ordered {
		set.Files = append(set.Files, load(in, set.Framework, confs, opts.Mode))
	}
	return set
}

func detect(files []Input, atlasSum map[string]bool) Framework {
	if len(files) == 0 {
		return Plain
	}
	all := func(ok func(Input) bool) bool {
		return !slices.ContainsFunc(files, func(f Input) bool { return !ok(f) })
	}
	switch {
	case all(func(f Input) bool {
		b := path.Base(f.Name)
		return flywayV.MatchString(b) || flywayR.MatchString(b) || flywayU.MatchString(b)
	}):
		return Flyway
	case all(func(f Input) bool { return golangMigrate.MatchString(path.Base(f.Name)) }):
		return GolangMigrate
	case all(func(f Input) bool { return prismaPath.MatchString(f.Name) }):
		return Prisma
	case len(atlasSum) > 0 || slices.ContainsFunc(files, func(f Input) bool { return atlasTxMode.MatchString(f.SQL) }):
		return Atlas
	}
	return Plain
}

func order(fw Framework, files []Input, keep bool) []Input {
	out := slices.Clone(files)
	switch fw {
	case Flyway:
		out = slices.DeleteFunc(out, func(f Input) bool { return flywayU.MatchString(path.Base(f.Name)) })
		slices.SortStableFunc(out, func(a, b Input) int {
			ma, mb := flywayV.FindStringSubmatch(path.Base(a.Name)), flywayV.FindStringSubmatch(path.Base(b.Name))
			switch {
			case ma != nil && mb != nil:
				return cmpVersion(ma[1], mb[1])
			case ma != nil:
				return -1 // versioned migrations run before repeatables
			case mb != nil:
				return 1
			}
			return strings.Compare(path.Base(a.Name), path.Base(b.Name))
		})
	case GolangMigrate:
		out = slices.DeleteFunc(out, func(f Input) bool { return strings.HasSuffix(f.Name, ".down.sql") })
		slices.SortStableFunc(out, func(a, b Input) int {
			va := golangMigrate.FindStringSubmatch(path.Base(a.Name))[1]
			vb := golangMigrate.FindStringSubmatch(path.Base(b.Name))[1]
			return cmpVersion(va, vb)
		})
	case Prisma:
		slices.SortStableFunc(out, func(a, b Input) int {
			return Natural(prismaPath.FindStringSubmatch(a.Name)[1], prismaPath.FindStringSubmatch(b.Name)[1])
		})
	default:
		if !keep {
			slices.SortStableFunc(out, func(a, b Input) int { return Natural(a.Name, b.Name) })
		}
	}
	return out
}

// cmpVersion compares dotted/underscored numeric versions ("1.10" > "1.9").
func cmpVersion(a, b string) int {
	pa := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '_' })
	pb := strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '_' })
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int64
		if i < len(pa) {
			x, _ = strconv.ParseInt(pa[i], 10, 64)
		}
		if i < len(pb) {
			y, _ = strconv.ParseInt(pb[i], 10, 64)
		}
		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}

// Natural compares strings with digit runs compared as numbers ("2_a" < "10_a").
func Natural(a, b string) int {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if c := cmp.Or(cmp.Compare(len(na), len(nb)), strings.Compare(na, nb)); c != 0 {
				return c
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return cmp.Compare(a[0], b[0])
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func digits(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

func load(in Input, fw Framework, confs map[string]string, mode Mode) File {
	text := Normalize(in.SQL)
	pf := parse.Split(text)
	f := File{Name: in.Name, Framework: fw, Meta: pf.Meta, WrapTx: TxNone}
	switch mode {
	case ModeFile:
		f.Wrapped, f.WrapTx = true, TxFramework
	case ModeStatement, ModeNone:
	default:
		f.Wrapped, f.WrapTx = autoWrap(fw, text, pf.Statements, confs[in.Name])
	}
	explicit := false
	for _, s := range pf.Statements {
		st := Statement{Statement: s, Tx: f.WrapTx}
		if t := s.Node.GetTransactionStmt(); t != nil {
			switch t.GetKind() {
			case pg_query.TransactionStmtKind_TRANS_STMT_BEGIN, pg_query.TransactionStmtKind_TRANS_STMT_START:
				explicit = true
			case pg_query.TransactionStmtKind_TRANS_STMT_COMMIT, pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK:
				st.Tx = TxExplicit
				explicit = false
			}
		}
		if explicit {
			st.Tx = TxExplicit
		}
		f.Statements = append(f.Statements, st)
	}
	return f
}

// autoWrap decides whether the tool wraps the whole file in a transaction.
func autoWrap(fw Framework, text string, stmts []parse.Statement, conf string) (bool, Tx) {
	switch fw {
	case Flyway:
		if m := confInTx.FindStringSubmatch(conf); m != nil && strings.EqualFold(m[1], "false") {
			return false, TxNone
		}
		// Flyway's PostgreSQL parser runs a script that contains a statement
		// PostgreSQL refuses in a transaction without one.
		if slices.ContainsFunc(stmts, func(s parse.Statement) bool { return nonTransactional(s.Node) }) {
			return false, TxNone
		}
		return true, TxFramework
	case GolangMigrate, Prisma:
		// The file is sent as one multi-statement query: PostgreSQL runs it
		// as one implicit transaction when it has more than one statement.
		if len(stmts) > 1 {
			return true, TxImplicit
		}
		return false, TxNone
	case Atlas:
		if m := atlasTxMode.FindStringSubmatch(text); m != nil && strings.EqualFold(m[1], "none") {
			return false, TxNone
		}
		return true, TxFramework
	}
	return false, TxNone
}

// nonTransactional mirrors the statements Flyway's PostgreSQL parser runs
// outside a transaction (CONCURRENTLY index operations, VACUUM, CREATE/DROP
// DATABASE and TABLESPACE, ALTER SYSTEM, DISCARD ALL).
func nonTransactional(n *pg_query.Node) bool {
	switch {
	case n == nil:
		return false
	case n.GetIndexStmt() != nil:
		return n.GetIndexStmt().GetConcurrent()
	case n.GetDropStmt() != nil:
		return n.GetDropStmt().GetConcurrent()
	case n.GetReindexStmt() != nil:
		for _, p := range n.GetReindexStmt().GetParams() {
			if p.GetDefElem().GetDefname() == "concurrently" {
				return true
			}
		}
	case n.GetVacuumStmt() != nil:
		return n.GetVacuumStmt().GetIsVacuumcmd()
	case n.GetCreatedbStmt() != nil, n.GetDropdbStmt() != nil, n.GetAlterSystemStmt() != nil,
		n.GetCreateTableSpaceStmt() != nil, n.GetDropTableSpaceStmt() != nil:
		return true
	case n.GetDiscardStmt() != nil:
		return true
	case n.GetAlterTableStmt() != nil:
		for _, c := range n.GetAlterTableStmt().GetCmds() {
			if c.GetAlterTableCmd().GetDef().GetPartitionCmd().GetConcurrent() {
				return true
			}
		}
	}
	return false
}

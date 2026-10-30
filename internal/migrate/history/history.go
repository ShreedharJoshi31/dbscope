// Package history reads a migration tool's history (the table, collection,
// nodes or index where it records applied migrations) and says which
// migration files are already applied, so that only pending ones are
// analyzed. Every read is a bounded SELECT, find or search; a read that fails
// becomes a warning and every file is then checked.
package history

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// maxRows bounds every history read; histories hold one row per migration run.
const maxRows = 100000

// DefaultTimeout bounds each read when the caller's context has no sooner deadline.
const DefaultTimeout = 30 * time.Second

// Result is what the history says about the files.
type Result struct {
	// Pending is nil when no history was found or none of the files is named as its tool names migrations.
	Pending  *migrate.Pending
	Warnings []string
}

// Keep returns files without those the history shows applied (changed files stay).
func (r Result) Keep(files []ingest.Input) []ingest.Input {
	if r.Pending == nil || len(r.Pending.Applied) == 0 {
		return files
	}
	drop := slices.Clone(r.Pending.Applied)
	// An applied migration's separate rollback file (golang-migrate .down.sql, Flyway U) goes with it.
	for up, d := range ingest.Pair(files).Downs {
		if d != nil && d.Name != up && slices.Contains(r.Pending.Applied, up) {
			drop = append(drop, d.Name)
		}
	}
	return slices.DeleteFunc(slices.Clone(files), func(f ingest.Input) bool { return slices.Contains(drop, f.Name) })
}

type state int

const (
	other   state = iota // not a migration file of this tool: kept and not listed
	pending              // not applied yet
	applied
	changed // applied, but the file's checksum differs from the recorded one
)

// classify builds the Result for one tool's history.
func classify(tool, table string, files []ingest.Input, of func(ingest.Input) state) Result {
	p := &migrate.Pending{Tool: tool, Table: table, Applied: []string{}, Changed: []string{}}
	known := false
	for _, f := range files {
		switch of(f) {
		case other:
			continue
		case applied:
			p.Applied = append(p.Applied, f.Name)
		case changed:
			p.Changed = append(p.Changed, f.Name)
		}
		known = true
	}
	if !known {
		return Result{Warnings: []string{fmt.Sprintf("%s (%s) was found, but no file is named the way %s names migrations; every file is checked", table, tool, tool)}}
	}
	return Result{Pending: p}
}

func notFound(looked string) Result {
	return Result{Warnings: []string{"no migration history was found (looked for " + looked + "); every file is checked"}}
}

func readFailed(what string, err error) Result {
	return Result{Warnings: []string{fmt.Sprintf("could not read %s: %v; every file is checked", what, err)}}
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, DefaultTimeout)
}

var (
	flywayV       = regexp.MustCompile(`^V(\d+(?:[._]\d+)*)__.*\.sql$`)
	flywayR       = regexp.MustCompile(`^R__.*\.sql$`)
	golangMigrate = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)
	numbered      = regexp.MustCompile(`^(\d+)_.*\.sql$`) // goose, dbmate
	prismaPath    = regexp.MustCompile(`(?:^|/)migrations/([^/]+)/migration\.sql$`)
	neo4jV        = regexp.MustCompile(`^V(\d+(?:[._]\d+)*)__.*\.cypher$`)
	evolutionV    = regexp.MustCompile(`(?i)^V(\d+(?:\.\d+)*)__.*\.http$`)
	gooseMarker   = regexp.MustCompile(`(?m)^\s*--\s*\+goose\s+Up`)
	dbmateMarker  = regexp.MustCompile(`(?m)^\s*--\s*migrate:up`)
	cypherEnd     = regexp.MustCompile(`;(?:\r\n|\r|\n)`)
)

// version normalises a dotted or underscored numeric version the way Flyway
// and neo4j-migrations compare them: parts are numbers, so leading zeros and
// trailing zero parts do not matter ("1_01.0" is "1.1").
func version(v string) string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '_' })
	for i, p := range parts {
		if p = strings.TrimLeft(p, "0"); p == "" {
			p = "0"
		}
		parts[i] = p
	}
	for len(parts) > 1 && parts[len(parts)-1] == "0" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, ".")
}

// cmpVersion compares two normalised versions numerically.
func cmpVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		x, y := "0", "0"
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if c := cmp.Or(cmp.Compare(len(x), len(y)), strings.Compare(x, y)); c != 0 {
			return c
		}
	}
	return 0
}

// FlywayChecksum is Flyway's checksum of a script: CRC32 over the UTF-8 bytes
// of each line without its terminator, a leading BOM removed, as a signed
// 32-bit integer (org.flywaydb.core.internal.resolver.ChecksumCalculator).
func FlywayChecksum(text string) int32 {
	text = strings.TrimPrefix(text, "\ufeff")
	h := crc32.NewIEEE()
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		h.Write([]byte(line))
	}
	return int32(h.Sum32())
}

// Neo4jChecksum is neo4j-migrations' default checksum of a Cypher script:
// CRC32 over its statements (split on ";" at a line end, trimmed, the
// trailing ";" removed, empty ones skipped) with no separator, as an
// unsigned decimal (ac.simons.neo4j.migrations.core.DefaultCypherResource).
// Its ":use" splitting is not reproduced.
func Neo4jChecksum(text string) string {
	h := crc32.NewIEEE()
	for _, s := range cypherEnd.Split(text, -1) {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";"))
		if s != "" {
			h.Write([]byte(s))
		}
	}
	return fmt.Sprint(h.Sum32())
}

// sha256Hex is the hash migrate-mongo records as fileHash.
func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// PrismaChecksumMatches mirrors Prisma's script_matches_checksum
// (schema-engine/connectors/schema-connector/src/checksum.rs): the SHA-256 of
// the script as is, with CRLF turned into LF, or with LF turned into CRLF,
// in lowercase hex, zero-padded or (for old rows) not.
func PrismaChecksumMatches(script, checksum string) bool {
	for _, s := range []string{script, strings.ReplaceAll(script, "\r\n", "\n"), strings.ReplaceAll(script, "\n", "\r\n")} {
		sum := sha256.Sum256([]byte(s))
		if len(checksum) == len(sum)*2 || checksum == "" {
			if hex.EncodeToString(sum[:]) == checksum {
				return true
			}
			continue
		}
		var b strings.Builder
		for _, x := range sum {
			fmt.Fprintf(&b, "%x", x)
		}
		if b.String() == checksum {
			return true
		}
	}
	return false
}

func base(name string) string { return path.Base(name) }

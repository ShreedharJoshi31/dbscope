package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/migrate/parse"
)

// appliedSQL lists what each framework's bookkeeping table records as applied.
var appliedSQL = map[Framework]string{
	Rails:   `SELECT version FROM schema_migrations`,
	Django:  `SELECT app || '.' || name FROM django_migrations`,
	Alembic: `SELECT version_num FROM alembic_version`,
	// Drizzle 1.0 adds a name column; 0.x identifies migrations by created_at.
	Drizzle: `SELECT coalesce(to_jsonb(m)->>'name', to_jsonb(m)->>'created_at') FROM drizzle.__drizzle_migrations m`,
	Prisma:  `SELECT migration_name FROM _prisma_migrations WHERE finished_at IS NOT NULL AND rolled_back_at IS NULL`,
}

// Pending drops the migrations the target database has already applied.
// Alembic stores only the current head, so everything up to it is applied.
func Pending(ctx context.Context, target *pgx.Conn, ms []Migration) ([]Migration, error) {
	if len(ms) == 0 || ms[0].Framework == "" {
		return ms, nil
	}
	fw := ms[0].Framework
	applied := map[string]bool{}
	rows, err := target.Query(ctx, appliedSQL[fw])
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "42P01" || pe.Code == "3F000") {
			return ms, nil // no bookkeeping table: nothing applied yet
		}
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "42P01" || pe.Code == "3F000") {
			return ms, nil
		}
		return nil, err
	}
	for _, k := range keys {
		applied[k] = true
	}
	if fw == Alembic {
		last := -1
		for i, m := range ms {
			for _, k := range m.Keys {
				if applied[k] {
					last = i
				}
			}
		}
		return ms[last+1:], nil
	}
	var out []Migration
	for _, m := range ms {
		for _, k := range m.Keys {
			if !applied[k] {
				out = append(out, m)
				break
			}
		}
	}
	return out, nil
}

// DrizzleNames maps a Drizzle 0.x journal's "when" to its migration tag, from
// the first meta/_journal.json found within two levels of dir.
func DrizzleNames(dir string) map[string]string {
	out := map[string]string{}
	for _, pat := range []string{"meta/_journal.json", "*/meta/_journal.json", "*/*/meta/_journal.json"} {
		paths, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var j struct {
				Entries []struct {
					When int64  `json:"when"`
					Tag  string `json:"tag"`
				} `json:"entries"`
			}
			if json.Unmarshal(src, &j) != nil {
				continue
			}
			for _, e := range j.Entries {
				out[fmt.Sprint(e.When)] = e.Tag
			}
			return out
		}
	}
	return out
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// CheckOut fails unless dir is empty or absent, so old and new captures never mix.
func CheckOut(dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty; choose a new --out directory so old and new captures do not mix", dir)
	}
	return nil
}

// Write writes one .sql file per migration into dir, which must be empty or
// absent, and returns the file names in order.
func Write(dir string, ms []Migration, unrecorded *Migration, names map[string]string) ([]string, error) {
	if err := CheckOut(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	display := func(k string) string {
		if n := names[k]; n != "" {
			return n
		}
		return k
	}
	all := ms
	if unrecorded != nil {
		all = append(append([]Migration(nil), ms...), *unrecorded)
	}
	var files []string
	for i, m := range all {
		if len(m.SQL) == 0 {
			continue
		}
		label := "unrecorded"
		var shown []string
		for _, k := range m.Keys {
			shown = append(shown, display(k))
		}
		if unrecorded != nil && i == len(all)-1 {
			shown = nil
		} else if len(shown) > 0 {
			label = shown[0]
			if len(shown) > 1 {
				label += "_to_" + shown[len(shown)-1]
			}
		}
		name := fmt.Sprintf("%04d_%s.sql", len(files)+1, strings.Trim(unsafe.ReplaceAllString(label, "_"), "_"))
		var b strings.Builder
		switch {
		case len(shown) == 1:
			fmt.Fprintf(&b, "-- Captured by dbscope from %s migration %s.\n", m.Framework, shown[0])
		case len(shown) > 1:
			fmt.Fprintf(&b, "-- Captured by dbscope from %s migrations %s,\n-- which share one transaction, so its locks span all of them.\n", m.Framework, strings.Join(shown, ", "))
		default:
			b.WriteString("-- Captured by dbscope: statements no migration was recorded for.\n")
		}
		if m.Partial {
			b.WriteString("-- Some parameters were sent in binary and are left as $n.\n")
		}
		b.WriteString("\n")
		for _, s := range m.SQL {
			b.WriteString(statementText(s))
			b.WriteString(";\n\n")
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
			return nil, err
		}
		files = append(files, name)
	}
	return files, nil
}

// statementText is s from its first to its last token, keeping comment lines
// before it, so a trailing "-- comment" cannot swallow the ';' written after.
func statementText(s string) string {
	f := parse.Split(s)
	if len(f.Statements) != 1 {
		return strings.TrimRight(strings.TrimSpace(s), ";")
	}
	st := f.Statements[0]
	lead := strings.TrimSpace(s[:st.Start])
	if lead == "" {
		return st.SQL
	}
	return lead + "\n" + st.SQL
}

// Package redact strips data from PostgreSQL errors before they are sent
// to a language model for repair (docs/NL.md "Prompt hygiene"). Error
// messages can carry row values: CAST(secret AS int) fails with
// invalid input syntax for type integer: "<the secret>".
package redact

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Placeholder replaces every quoted literal.
const Placeholder = "<redacted>"

var hexBytes = regexp.MustCompile(`0x[0-9A-Fa-f]{2,}(?:\s+0x[0-9A-Fa-f]{2,})*`)

// Literals replaces every quoted span ('…' or "…") with Placeholder. A span
// runs from the first quote to the last quote of the same kind, so a value
// that itself contains quotes cannot split the span and leak a piece; an
// unmatched quote redacts the rest of the text. Byte dumps such as
// "0xe2 0x80" (encoding errors) are redacted too.
func Literals(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexAny(s, `'"`)
		if i < 0 {
			b.WriteString(s)
			break
		}
		q := s[i]
		j := strings.LastIndexByte(s, q)
		b.WriteString(s[:i])
		b.WriteString(Placeholder)
		if j <= i {
			break // unmatched: drop the rest
		}
		s = s[j+1:]
	}
	return hexBytes.ReplaceAllString(b.String(), Placeholder)
}

// Error renders a database error for a repair prompt: SQLSTATE, the
// message and the hint with literals redacted, and the position in the
// executed SQL. Detail, Where and the internal query are never included.
func Error(code, message, hint string, position int) string {
	var b strings.Builder
	if code != "" {
		b.WriteString("SQLSTATE " + code + ": ")
	}
	b.WriteString(Literals(message))
	if hint != "" {
		b.WriteString(" (hint: " + Literals(hint) + ")")
	}
	if position > 0 {
		b.WriteString(" at character " + strconv.Itoa(position) + " of the executed SQL")
	}
	return b.String()
}

// PgError is Error for a pgx error; position is the caller's mapping onto
// the executed SQL (exec.DBError.Position), not pgErr.Position.
func PgError(e *pgconn.PgError, position int) string {
	return Error(e.Code, e.Message, e.Hint, position)
}

package capture

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgproto3"
)

// Param is one bound parameter value.
type Param struct {
	Null   bool
	Binary bool
	Value  []byte
}

// Event is one statement the server finished, successfully or not.
type Event struct {
	Seq      int
	Conn     int
	Database string
	// Unit groups statements of one Query message or one Sync batch; the
	// server runs a unit with several statements as one implicit transaction
	// when no transaction is open.
	Unit   int
	InTx   bool // a transaction block was open when the unit started
	SQL    string
	Params []Param
	Tag    string
	Error  string
}

// Recorder collects events from every proxied connection in completion order.
type Recorder struct {
	mu     sync.Mutex
	events []Event
	notes  []string
	conns  int
}

func (r *Recorder) add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Seq = len(r.events) + 1
	r.events = append(r.events, e)
}

// failUnit marks the statements of an implicit transaction that a later
// statement's error rolled back.
func (r *Recorder) failUnit(conn, unit int, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.events {
		if e := &r.events[i]; e.Conn == conn && e.Unit == unit && !e.InTx && e.Error == "" {
			e.Error = "rolled back: " + msg
		}
	}
}

func (r *Recorder) connected() {
	r.mu.Lock()
	r.conns++
	r.mu.Unlock()
}

func (r *Recorder) note(format string, args ...any) {
	r.mu.Lock()
	r.notes = append(r.notes, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

// Snapshot returns the events, connection count and notes so far.
func (r *Recorder) Snapshot() (events []Event, conns int, notes []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...), r.conns, append([]string(nil), r.notes...)
}

func params(b pgproto3.Bind) []Param {
	out := make([]Param, len(b.Parameters))
	for i, v := range b.Parameters {
		f := int16(0)
		switch len(b.ParameterFormatCodes) {
		case 0:
		case 1:
			f = b.ParameterFormatCodes[0]
		default:
			if i < len(b.ParameterFormatCodes) {
				f = b.ParameterFormatCodes[i]
			}
		}
		out[i] = Param{Null: v == nil, Binary: f == 1, Value: append([]byte(nil), v...)}
	}
	return out
}

// Text is the parameter as a string when that is unambiguous: text format, or
// binary bytes that are printable UTF-8 (the binary form of text and varchar).
func (p Param) Text() (string, bool) {
	if p.Null {
		return "", false
	}
	s := string(p.Value)
	if !p.Binary {
		return s, true
	}
	if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 }) >= 0 {
		return "", false
	}
	return s, true
}

// Render substitutes text-format parameters into sql as quoted literals.
// Binary parameters stay as $n; ok reports whether every one was replaced.
func Render(sql string, ps []Param) (out string, ok bool) {
	if len(ps) == 0 {
		return sql, true
	}
	ok = true
	var b strings.Builder
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'' || c == '"':
			j := closeQuote(sql, i, c)
			b.WriteString(sql[i:j])
			i = j
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				j = len(sql) - i
			}
			b.WriteString(sql[i : i+j])
			i += j
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			j := closeComment(sql, i)
			b.WriteString(sql[i:j])
			i = j
		case c == '$':
			if j, ok2 := dollarTag(sql, i); ok2 {
				end := strings.Index(sql[j:], sql[i:j])
				if end < 0 {
					b.WriteString(sql[i:])
					return b.String(), ok
				}
				end += j + (j - i)
				b.WriteString(sql[i:end])
				i = end
				continue
			}
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			n := 0
			fmt.Sscanf(sql[i+1:j], "%d", &n)
			if j == i+1 || n < 1 || n > len(ps) || (i > 0 && isIdent(sql[i-1])) {
				b.WriteByte(c)
				i++
				continue
			}
			p := ps[n-1]
			switch {
			case p.Null:
				b.WriteString("NULL")
			case p.Binary:
				b.WriteString(sql[i:j])
				ok = false
			default:
				b.WriteString("'" + strings.ReplaceAll(string(p.Value), "'", "''") + "'")
			}
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), ok
}

// closeQuote returns the index after the quoted token starting at i. A
// doubled quote is an escaped quote; E'...' strings also escape with backslash.
func closeQuote(s string, i int, q byte) int {
	escapes := q == '\'' && i > 0 && (s[i-1] == 'E' || s[i-1] == 'e') && (i < 2 || !isIdent(s[i-2]))
	for j := i + 1; j < len(s); j++ {
		switch {
		case escapes && s[j] == '\\':
			j++
		case s[j] == q && j+1 < len(s) && s[j+1] == q:
			j++
		case s[j] == q:
			return j + 1
		}
	}
	return len(s)
}

// closeComment returns the index after a nested /* */ comment at i.
func closeComment(s string, i int) int {
	depth := 0
	for j := i; j+1 < len(s); j++ {
		switch {
		case s[j] == '/' && s[j+1] == '*':
			depth++
			j++
		case s[j] == '*' && s[j+1] == '/':
			depth--
			j++
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(s)
}

// dollarTag reports whether a dollar-quote opener ($$ or $tag$) starts at i,
// returning the index after it.
func dollarTag(s string, i int) (int, bool) {
	if i > 0 && isIdent(s[i-1]) {
		return 0, false
	}
	j := i + 1
	if j < len(s) && s[j] >= '0' && s[j] <= '9' {
		return 0, false
	}
	for j < len(s) && isIdent(s[j]) {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return j + 1, true
	}
	return 0, false
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

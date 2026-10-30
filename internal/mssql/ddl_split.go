package mssql

import (
	"regexp"
	"strings"
)

// Piece is one statement of a script with the 1-based line and column of
// its first token. Batch counts the GO separators before it.
type Piece struct {
	SQL                   string
	Line, Column, EndLine int
	Batch                 int
}

// goLine is sqlcmd's and SSMS's batch separator: GO alone on its line, with
// an optional repeat count (https://learn.microsoft.com/sql/t-sql/language-elements/sql-server-utilities-statements-go).
var goLine = regexp.MustCompile(`(?i)^[ \t]*go(?:[ \t]+\d+)?[ \t]*(?:--.*)?\r?$`)

// statementStarts are words that begin a new statement when they open a
// line, since T-SQL needs no semicolon between statements.
var statementStarts = map[string]bool{
	"alter": true, "create": true, "drop": true, "truncate": true, "exec": true, "execute": true,
	"insert": true, "update": true, "delete": true, "set": true, "commit": true, "rollback": true,
	"print": true, "declare": true, "use": true,
}

// bodies are statements that run to the end of their batch.
var bodies = map[string]bool{"procedure": true, "proc": true, "function": true, "trigger": true, "view": true}

// Split divides a T-SQL script into statements at semicolons, at GO lines
// and at lines that start a new statement, outside strings and comments.
func Split(script string) []Piece {
	var out []Piece
	type mark struct{ start, end, batch int }
	var cuts []mark
	start, batch := 0, 0
	cut := func(end, next int) {
		cuts = append(cuts, mark{start, end, batch})
		start = next
	}
	depth := 0
	lineStart := true
	var pieceWords []string // the first words of the current piece
	for i := 0; i < len(script); {
		c := script[i]
		if lineStart {
			lineStart = false
			end := strings.IndexByte(script[i:], '\n')
			if end < 0 {
				end = len(script) - i
			}
			if goLine.MatchString(script[i : i+end]) {
				cut(i, min(i+end+1, len(script)))
				batch++
				depth, pieceWords = 0, nil
				i = start
				lineStart = true
				continue
			}
		}
		switch {
		case c == '\n':
			lineStart = true
			i++
		case strings.HasPrefix(script[i:], "--"):
			j := strings.IndexByte(script[i:], '\n')
			if j < 0 {
				i = len(script)
				continue
			}
			i += j
		case strings.HasPrefix(script[i:], "/*"):
			d := 0
			for i < len(script) {
				if strings.HasPrefix(script[i:], "/*") {
					d, i = d+1, i+2
				} else if strings.HasPrefix(script[i:], "*/") {
					d, i = d-1, i+2
					if d == 0 {
						break
					}
				} else {
					i++
				}
			}
		case c == '\'' || c == '"' || c == '[':
			end := c
			if c == '[' {
				end = ']'
			}
			j := i + 1
			for j < len(script) {
				if script[j] == end {
					if j+1 < len(script) && script[j+1] == end {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = min(j+1, len(script))
			pieceWords = append(pieceWords, "")
		case c == ';':
			cut(i, i+1)
			depth, pieceWords = 0, nil
			i++
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			i++
		case isWord(rune(c)):
			j := i
			for j < len(script) && isWord(rune(script[j])) {
				j++
			}
			w := strings.ToLower(script[i:j])
			if depth == 0 && len(pieceWords) > 0 && firstOnLine(script, i) && statementStarts[w] && splits(pieceWords, w) {
				cut(i, i)
				pieceWords = nil
			}
			pieceWords = append(pieceWords, w)
			i = j
		default:
			i++
		}
	}
	cut(len(script), len(script))
	for _, m := range cuts {
		text := script[m.start:m.end]
		toks := Tokens(text)
		if len(toks) == 0 || (len(toks) == 1 && toks[0].Semi) {
			continue
		}
		// The statement starts at its first token; comments before it belong to no statement.
		off := m.start + skipTrivia(text)
		sql := strings.TrimSpace(script[off:m.end])
		line := strings.Count(script[:off], "\n") + 1
		col := off - strings.LastIndex(script[:off], "\n")
		out = append(out, Piece{SQL: sql, Line: line, Column: col, EndLine: line + strings.Count(sql, "\n"), Batch: m.batch})
	}
	return out
}

// splits reports whether w, opening a line, starts a statement after the
// piece's words so far.
func splits(piece []string, w string) bool {
	first := piece[0]
	for i, x := range piece {
		// CREATE [OR ALTER] PROCEDURE and the like run to the end of the batch.
		if (first == "create" || first == "alter") && i <= 3 && bodies[x] {
			return false
		}
	}
	switch first {
	case "merge":
		return false
	case "update":
		return w != "set" // UPDATE t <newline> SET ...
	}
	return true
}

// firstOnLine reports whether only spaces precede offset i on its line.
func firstOnLine(s string, i int) bool {
	for j := i - 1; j >= 0 && s[j] != '\n'; j-- {
		if s[j] != ' ' && s[j] != '\t' && s[j] != '\r' {
			return false
		}
	}
	return true
}

// skipTrivia returns the offset of the first token of s, past spaces and comments.
func skipTrivia(s string) int {
	i := 0
	for i < len(s) {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\f' || s[i] == '\v':
			i++
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return len(s)
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "/*"):
			d := 0
			for i < len(s) {
				if strings.HasPrefix(s[i:], "/*") {
					d, i = d+1, i+2
				} else if strings.HasPrefix(s[i:], "*/") {
					d, i = d-1, i+2
					if d == 0 {
						break
					}
				} else {
					i++
				}
			}
		default:
			return i
		}
	}
	return i
}

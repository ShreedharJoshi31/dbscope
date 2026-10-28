package mysql

import (
	"strings"
)

// Piece is one statement of a script with the 1-based line and column of
// its first token.
type Piece struct {
	SQL                   string
	Line, Column, EndLine int
}

// Split divides a migration script into statements. Besides ";" it follows
// the mysql client's DELIMITER command, which migration files use around
// trigger and routine bodies; the delimiter is matched outside strings and
// comments only.
func Split(script string) []Piece {
	var out []Piece
	delim := ";"
	regionStart := 0
	lines := strings.SplitAfter(script, "\n")
	offset := 0
	flush := func(end int) {
		out = append(out, splitRegion(script, regionStart, end, delim)...)
	}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) == 2 && strings.EqualFold(f[0], "delimiter") && pendingEmpty(script[regionStart:offset], delim) {
			flush(offset)
			delim = f[1]
			regionStart = offset + len(l)
		}
		offset += len(l)
	}
	flush(len(script))
	return out
}

// pendingEmpty reports whether text holds no unfinished statement, so a
// DELIMITER line there is the client command rather than part of a statement.
func pendingEmpty(text, delim string) bool {
	rest := text
	if d := delimiters(text, delim); len(d) > 0 {
		rest = text[d[len(d)-1]+len(delim):]
	}
	return len(Tokens(rest)) == 0
}

// delimiters returns the offsets of delim in text outside strings, quoted
// names and comments. Byte-level, since "$$" can end a word token (END$$).
func delimiters(text, delim string) []int {
	code := make([]bool, len(text))
	for _, t := range Tokens(text) {
		if c := t.Text[0]; c == '\'' || c == '"' || c == '`' {
			continue
		}
		for i := t.Pos; i < t.Pos+len(t.Text) && i < len(code); i++ {
			code[i] = true
		}
	}
	var out []int
	for i := 0; i+len(delim) <= len(text); i++ {
		if text[i:i+len(delim)] != delim {
			continue
		}
		ok := true
		for j := i; j < i+len(delim); j++ {
			ok = ok && code[j]
		}
		if ok {
			out = append(out, i)
			i += len(delim) - 1
		}
	}
	return out
}

func splitRegion(script string, from, to int, delim string) []Piece {
	var out []Piece
	region := script[from:to]
	emit := func(start, end int) {
		toks := Tokens(region[start:end])
		if len(toks) == 0 {
			return
		}
		// The statement starts at its first token; comments before it belong to no statement.
		start += toks[0].Pos
		text := strings.TrimSpace(region[start:end])
		abs := from + start
		line := strings.Count(script[:abs], "\n") + 1
		col := abs - strings.LastIndex(script[:abs], "\n")
		out = append(out, Piece{SQL: text, Line: line, Column: col, EndLine: line + strings.Count(text, "\n")})
	}
	start := 0
	for _, d := range delimiters(region, delim) {
		emit(start, d)
		start = d + len(delim)
	}
	emit(start, len(region))
	return out
}

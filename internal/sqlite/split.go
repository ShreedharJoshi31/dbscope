package sqlite

import (
	"strings"
	"unicode"
)

// Piece is one statement of a script, with the 1-based line and column of
// its first token.
type Piece struct {
	SQL          string
	Line, Column int
	EndLine      int
}

// Token classes and states of sqlite3_complete (SQLite's src/complete.c),
// which knows that the semicolons inside CREATE TRIGGER ... BEGIN ... END do
// not end the statement.
const (
	tkSemi = iota
	tkWS
	tkOther
	tkExplain
	tkCreate
	tkTemp
	tkTrigger
	tkEnd
	// tkOpen is a string, quoted name or block comment still open at the end
	// of the script; sqlite3_complete calls such a script incomplete.
	tkOpen
)

var completeTrans = [8][8]uint8{
	/* 0 INVALID */ {1, 0, 2, 3, 4, 2, 2, 2},
	/* 1 START   */ {1, 1, 2, 3, 4, 2, 2, 2},
	/* 2 NORMAL  */ {1, 2, 2, 2, 2, 2, 2, 2},
	/* 3 EXPLAIN */ {1, 3, 3, 2, 4, 2, 2, 2},
	/* 4 CREATE  */ {1, 4, 2, 2, 2, 4, 5, 2},
	/* 5 TRIGGER */ {6, 5, 5, 5, 5, 5, 5, 5},
	/* 6 SEMI    */ {6, 6, 5, 5, 5, 5, 5, 7},
	/* 7 END     */ {1, 7, 5, 5, 5, 5, 5, 5},
}

// Split divides a script into statements the way SQLite would execute them.
// Comments and whitespace between statements are dropped; a statement keeps
// its own text, without the terminating semicolon.
func Split(script string) []Piece {
	var out []Piece
	state := uint8(0)
	start := -1
	line, col := 1, 1
	var startLine, startCol int
	advance := func(from, to int) {
		for _, r := range script[from:to] {
			if r == '\n' {
				line, col = line+1, 1
			} else {
				col++
			}
		}
	}
	emit := func(end int) {
		if start < 0 {
			return
		}
		text := strings.TrimSpace(script[start:end])
		if text != "" {
			out = append(out, Piece{SQL: text, Line: startLine, Column: startCol, EndLine: startLine + strings.Count(text, "\n")})
		}
		start = -1
	}
	for i := 0; i < len(script); {
		j, tok := nextToken(script, i)
		if tok == tkOpen {
			tok = tkOther
		}
		if tok != tkWS && tok != tkSemi && start < 0 {
			start, startLine, startCol = i, line, col
		}
		next := completeTrans[state][tok]
		if tok == tkSemi && next == 1 {
			emit(i)
		}
		state = next
		advance(i, j)
		i = j
	}
	emit(len(script))
	return out
}

func isWord(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) || r >= 0x80
}

// nextToken returns the end of the token at i and its class.
func nextToken(s string, i int) (int, int) {
	c := s[i]
	switch {
	case c == ';':
		return i + 1, tkSemi
	case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
		return i + 1, tkWS
	case c == '-' && i+1 < len(s) && s[i+1] == '-':
		j := strings.IndexByte(s[i:], '\n')
		if j < 0 {
			return len(s), tkWS
		}
		return i + j + 1, tkWS
	case c == '/' && i+1 < len(s) && s[i+1] == '*':
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			return len(s), tkOpen
		}
		return i + 2 + j + 2, tkWS
	case c == '\'' || c == '"' || c == '`' || c == '[':
		end := c
		if c == '[' {
			end = ']'
		}
		j := i + 1
		for j < len(s) && s[j] != end {
			j++
		}
		if j == len(s) {
			return j, tkOpen
		}
		return j + 1, tkOther // a doubled quote is read as two adjacent strings, which is the same class
	case isWord(rune(c)):
		j := i
		for j < len(s) && isWord(rune(s[j])) {
			j++
		}
		switch strings.ToLower(s[i:j]) {
		case "create":
			return j, tkCreate
		case "trigger":
			return j, tkTrigger
		case "temp", "temporary":
			return j, tkTemp
		case "end":
			return j, tkEnd
		case "explain":
			return j, tkExplain
		}
		return j, tkOther
	}
	if unicode.IsSpace(rune(c)) {
		return i + 1, tkWS
	}
	return i + 1, tkOther
}

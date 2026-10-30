package oracle

import "strings"

// Piece is one statement of a script with the 1-based line and column of
// its first token.
type Piece struct {
	SQL                   string
	Line, Column, EndLine int
	// Block is a PL/SQL unit (an anonymous block or CREATE PROCEDURE and the like).
	Block bool
	// Client is a SQL*Plus command, which the client runs and the server
	// never sees; EXEC is not one, since it runs PL/SQL on the server.
	Client bool
}

// sqlplusWords begin SQL*Plus commands, which end at the end of their line.
var sqlplusWords = map[string]bool{
	"prompt": true, "spool": true, "whenever": true, "rem": true, "remark": true, "define": true, "undefine": true,
	"show": true, "exit": true, "quit": true, "pause": true, "host": true, "column": true, "ttitle": true, "btitle": true,
	"var": true, "variable": true, "print": true, "exec": true, "execute": true, "connect": true, "conn": true,
	"set": true, "@": true, "@@": true, "start": true,
}

// Split divides an Oracle script into statements the way SQL*Plus and
// Flyway do: SQL ends at ";" or at a line holding only "/"; a PL/SQL unit
// (BEGIN, DECLARE, CREATE PROCEDURE, FUNCTION, PACKAGE, TRIGGER, TYPE)
// keeps its inner semicolons and ends only at a "/" line or the end of the
// script; SQL*Plus commands (SET DEFINE OFF, PROMPT, WHENEVER, EXEC ...)
// end at the end of their line.
func Split(script string) []Piece {
	toks := Tokens(script)
	var out []Piece
	emit := func(first, last int, block, client bool) {
		if first > last {
			return
		}
		start, end := toks[first].Pos, toks[last].Pos+len(toks[last].Text)
		text := script[start:end]
		line := strings.Count(script[:start], "\n") + 1
		col := start - strings.LastIndex(script[:start], "\n")
		out = append(out, Piece{SQL: text, Line: line, Column: col, EndLine: line + strings.Count(text, "\n"), Block: block, Client: client})
	}
	slashLine := func(i int) bool {
		t := toks[i]
		if t.Text != "/" {
			return false
		}
		ls := strings.LastIndexByte(script[:t.Pos], '\n') + 1
		le := strings.IndexByte(script[t.Pos:], '\n')
		if le < 0 {
			le = len(script)
		} else {
			le += t.Pos
		}
		return strings.TrimSpace(script[ls:t.Pos]) == "" && strings.TrimSpace(script[t.Pos+1:le]) == ""
	}
	lineEnd := func(pos int) int {
		if j := strings.IndexByte(script[pos:], '\n'); j >= 0 {
			return pos + j
		}
		return len(script)
	}
	for i := 0; i < len(toks); {
		if toks[i].Semi || slashLine(i) {
			i++
			continue
		}
		switch {
		case isClientCommand(toks, i):
			end := lineEnd(toks[i].Pos)
			j := i
			for j+1 < len(toks) && toks[j+1].Pos < end {
				j++
			}
			exec := toks[i].Word == "exec" || toks[i].Word == "execute"
			emit(i, j, exec, !exec)
			i = j + 1
		case isBlockStart(toks, i):
			j := i
			for j < len(toks) && !slashLine(j) {
				j++
			}
			emit(i, j-1, true, false)
			i = j
		default:
			j := i
			for j < len(toks) && !toks[j].Semi && !slashLine(j) {
				j++
			}
			emit(i, j-1, false, false)
			i = j
		}
	}
	return out
}

// isClientCommand reports a SQL*Plus command at toks[i]. SET is SQL*Plus's
// unless it is SET TRANSACTION, ROLE or CONSTRAINT(S); EXEC and EXECUTE
// are SQL*Plus's shorthand for a one-line PL/SQL block.
func isClientCommand(toks []Token, i int) bool {
	w := toks[i].Word
	if toks[i].Text == "@" {
		return true
	}
	if !sqlplusWords[w] {
		return false
	}
	if w == "set" && i+1 < len(toks) {
		switch toks[i+1].Word {
		case "transaction", "role", "constraint", "constraints":
			return false
		}
	}
	return true
}

// isBlockStart reports a PL/SQL unit at toks[i].
func isBlockStart(toks []Token, i int) bool {
	switch toks[i].Word {
	case "begin", "declare":
		return true
	case "create":
	default:
		return false
	}
	for j := i + 1; j < len(toks) && j < i+6; j++ {
		switch toks[j].Word {
		case "or", "replace", "editionable", "noneditionable", "editioning", "and", "compile", "resolve", "noforce", "force":
			continue
		case "procedure", "function", "package", "trigger", "type", "library", "java":
			return true
		}
		return false
	}
	return false
}

// Package parse splits a migration file into statements with libpg_query
// (go-pgquery, PostgreSQL 17 grammar) and gives each one a position that
// SARIF can use: a 1-based line and a 1-based column in UTF-16 code units.
//
// The whole file is parsed first. When that fails (a typo, or PostgreSQL 18
// syntax the 17 grammar does not know), the file is split on top-level
// semicolons from libpg_query's scanner, which understands quoting, dollar
// quoting and comments, and each piece is parsed alone. Pieces that still
// fail become statements with Err set; the rest of the file is analyzed
// normally (docs/MIGRATIONS.md, "Parser limit").
package parse

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"github.com/wasilibs/go-pgquery/parser"
)

// Statement is one SQL statement of a file.
type Statement struct {
	// SQL is the statement text from its first to its last token, without
	// the terminating semicolon.
	SQL string
	// Start and End are byte offsets of SQL in the parsed text.
	Start, End int
	// Line and Column locate the first token; EndLine the last. Lines are
	// 1-based; Column is 1-based in UTF-16 code units.
	Line, Column, EndLine int
	// Node is the parse tree, nil when Err is set.
	Node *pg_query.Node
	Err  *Error
}

// Error is a statement the PostgreSQL 17 grammar rejected.
type Error struct {
	Message string
	// Line and Column locate the error inside the file (same units as Statement).
	Line, Column int
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Message) }

// Meta is a psql meta-command line (e.g. pg_dump's "\restrict KEY"); it is
// not SQL, so it is blanked out before parsing.
type Meta struct {
	Line    int
	Command string
}

// File is a parsed file.
type File struct {
	Statements []Statement
	Meta       []Meta
}

// Split parses text into statements. text should already be normalised
// (no BOM, LF line endings). It never fails: problems are reported per
// statement.
func Split(text string) *File {
	idx := newLineIndex(text)
	f := &File{}
	toks, scanErr := scan(text)
	if scanErr == nil {
		text, f.Meta = blankMeta(text, toks, idx)
		if len(f.Meta) > 0 {
			toks, scanErr = scan(text)
		}
	}
	if scanErr == nil {
		if tree, err := safeParse(text); err == nil {
			f.Statements = fromTree(text, tree, toks, idx)
			return f
		}
	}
	f.Statements = splitTokens(text, toks, scanErr, idx)
	return f
}

// token is a scanner token with comments removed.
type token struct {
	kind       pg_query.Token
	start, end int
}

func scan(text string) (toks []token, err error) {
	defer func() {
		if r := recover(); r != nil {
			toks, err = nil, &parser.Error{Message: fmt.Sprintf("the SQL scanner failed: %v", r)}
		}
	}()
	res, err := pgquery.Scan(text)
	if err != nil {
		return nil, err
	}
	for _, t := range res.GetTokens() {
		switch t.GetToken() {
		case pg_query.Token_SQL_COMMENT, pg_query.Token_C_COMMENT:
			continue
		}
		toks = append(toks, token{kind: t.GetToken(), start: int(t.GetStart()), end: int(t.GetEnd())})
	}
	return toks, nil
}

func safeParse(text string) (tree *pg_query.ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			tree, err = nil, &parser.Error{Message: fmt.Sprintf("the SQL parser failed: %v", r)}
		}
	}()
	return pgquery.Parse(text)
}

// blankMeta replaces psql meta-command lines that start a statement with
// spaces, keeping every byte offset (and so every position) unchanged.
func blankMeta(text string, toks []token, idx *lineIndex) (string, []Meta) {
	var metas []Meta
	b := []byte(text)
	atStart := true
	skipUntil := -1 // end of the line being blanked
	for _, t := range toks {
		if t.start < skipUntil {
			continue
		}
		if t.kind == pg_query.Token_ASCII_92 && atStart && lineHead(text, t.start) {
			eol := strings.IndexByte(text[t.start:], '\n')
			if eol < 0 {
				eol = len(text) - t.start
			}
			skipUntil = t.start + eol
			line, _ := idx.position(t.start)
			metas = append(metas, Meta{Line: line, Command: strings.TrimSpace(text[t.start:skipUntil])})
			for j := t.start; j < skipUntil; j++ {
				b[j] = ' '
			}
			continue
		}
		atStart = t.kind == pg_query.Token_ASCII_59
	}
	return string(b), metas
}

// lineHead reports whether only spaces or tabs precede off on its line.
func lineHead(text string, off int) bool {
	for i := off - 1; i >= 0 && text[i] != '\n'; i-- {
		if text[i] != ' ' && text[i] != '\t' {
			return false
		}
	}
	return true
}

func fromTree(text string, tree *pg_query.ParseResult, toks []token, idx *lineIndex) []Statement {
	var out []Statement
	for _, raw := range tree.GetStmts() {
		from := int(raw.GetStmtLocation())
		to := len(text)
		if l := int(raw.GetStmtLen()); l > 0 {
			to = from + l
		}
		s, ok := bounds(text, toks, from, to, idx)
		if !ok {
			continue
		}
		s.Node = raw.GetStmt()
		out = append(out, s)
	}
	return out
}

// bounds trims [from, to) to its first and last token.
func bounds(text string, toks []token, from, to int, idx *lineIndex) (Statement, bool) {
	i := sort.Search(len(toks), func(i int) bool { return toks[i].start >= from })
	j := i
	for j < len(toks) && toks[j].end <= to {
		j++
	}
	// Drop a trailing semicolon token if the range included it.
	for j > i && toks[j-1].kind == pg_query.Token_ASCII_59 {
		j--
	}
	if j <= i {
		return Statement{}, false
	}
	s := Statement{Start: toks[i].start, End: toks[j-1].end}
	s.SQL = text[s.Start:s.End]
	s.Line, s.Column = idx.position(s.Start)
	s.EndLine, _ = idx.position(s.End - 1)
	return s, true
}

// splitTokens cuts the token stream at top-level semicolons and parses each
// piece alone. When the scanner itself failed (e.g. an unterminated quote),
// the text before the error is split normally and the rest of the file is
// one unparseable statement.
func splitTokens(text string, toks []token, scanErr error, idx *lineIndex) []Statement {
	limit := len(text)
	var tail string
	if scanErr != nil {
		msg, pos := errorPos(scanErr)
		tail = msg
		limit = max(0, min(len(text), charToByte(text, pos-1)))
		var err error
		if toks, err = scan(text[:limit]); err != nil {
			toks = nil
			limit = 0
		}
	}
	type piece struct{ from, to int }
	var pieces []piece
	start, depth, routine := 0, 0, false
	for i, t := range toks {
		if i == start {
			routine = isRoutine(toks[i:])
			depth = 0
		}
		if routine {
			switch t.kind {
			case pg_query.Token_BEGIN_P:
				if i+1 < len(toks) && toks[i+1].kind == pg_query.Token_ATOMIC {
					depth++
				}
			case pg_query.Token_CASE:
				if depth > 0 {
					depth++
				}
			case pg_query.Token_END_P:
				if depth > 0 {
					depth--
				}
			}
		}
		if t.kind == pg_query.Token_ASCII_59 && depth == 0 {
			if i > start {
				pieces = append(pieces, piece{toks[start].start, toks[i-1].end})
			}
			start = i + 1
		}
	}
	lastOpen := -1
	if start < len(toks) {
		pieces = append(pieces, piece{toks[start].start, toks[len(toks)-1].end})
		lastOpen = len(pieces) - 1
	}
	var out []Statement
	for k, p := range pieces {
		to := p.to
		if tail != "" && k == lastOpen {
			to = len(text) // the unterminated statement runs to the end
		}
		out = append(out, parsePiece(text, p.from, to, idx))
	}
	if tail != "" && lastOpen < 0 && limit < len(text) {
		from := skipSpace(text, limit)
		if from < len(text) {
			s := Statement{Start: from, End: len(text), SQL: strings.TrimRight(text[from:], " \t\n")}
			s.End = from + len(s.SQL)
			s.Line, s.Column = idx.position(from)
			s.EndLine, _ = idx.position(max(from, s.End-1))
			s.Err = &Error{Message: tail, Line: s.Line, Column: s.Column}
			out = append(out, s)
		}
	} else if tail != "" && len(out) > 0 {
		last := &out[len(out)-1]
		if last.Err == nil || !strings.Contains(last.Err.Message, tail) {
			l, c := idx.position(limit)
			last.Node, last.Err = nil, &Error{Message: tail, Line: l, Column: c}
		}
	}
	return out
}

func skipSpace(text string, off int) int {
	for off < len(text) && strings.IndexByte(" \t\n\r", text[off]) >= 0 {
		off++
	}
	return off
}

// isRoutine reports whether the tokens start CREATE [OR REPLACE] FUNCTION
// or PROCEDURE, whose SQL-standard body (BEGIN ATOMIC ... END) contains
// semicolons that do not end the statement.
func isRoutine(toks []token) bool {
	if len(toks) < 2 || toks[0].kind != pg_query.Token_CREATE {
		return false
	}
	k := 1
	if len(toks) > 3 && toks[1].kind == pg_query.Token_OR && toks[2].kind == pg_query.Token_REPLACE {
		k = 3
	}
	return k < len(toks) && (toks[k].kind == pg_query.Token_FUNCTION || toks[k].kind == pg_query.Token_PROCEDURE)
}

func parsePiece(text string, from, to int, idx *lineIndex) Statement {
	s := Statement{Start: from, End: to, SQL: text[from:to]}
	s.Line, s.Column = idx.position(from)
	s.EndLine, _ = idx.position(max(from, to-1))
	tree, err := safeParse(s.SQL)
	if err == nil && len(tree.GetStmts()) == 1 {
		s.Node = tree.GetStmts()[0].GetStmt()
		return s
	}
	msg, pos := "", 0
	if err != nil {
		msg, pos = errorPos(err)
	} else {
		msg = fmt.Sprintf("expected one statement, the parser found %d", len(tree.GetStmts()))
	}
	e := &Error{Message: msg, Line: s.Line, Column: s.Column}
	if pos > 0 {
		e.Line, e.Column = idx.position(from + charToByte(s.SQL, pos-1))
	}
	s.Err = e
	return s
}

// errorPos returns the parser's message and its 1-based character cursor
// position (0 when unknown).
func errorPos(err error) (string, int) {
	var pe *parser.Error
	if errors.As(err, &pe) {
		return pe.Message, pe.Cursorpos
	}
	return err.Error(), 0
}

// charToByte converts a 0-based character offset (PostgreSQL error cursors
// count characters) into a byte offset.
func charToByte(s string, chars int) int {
	if chars <= 0 {
		return 0
	}
	n := 0
	for i := range s {
		if n == chars {
			return i
		}
		n++
	}
	return len(s)
}

// lineIndex maps byte offsets to (line, UTF-16 column).
type lineIndex struct {
	text   string
	starts []int
}

func newLineIndex(text string) *lineIndex {
	ix := &lineIndex{text: text, starts: []int{0}}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			ix.starts = append(ix.starts, i+1)
		}
	}
	return ix
}

// position returns the 1-based line and 1-based UTF-16 column of off.
func (ix *lineIndex) position(off int) (int, int) {
	off = max(0, min(off, len(ix.text)))
	line := sort.Search(len(ix.starts), func(i int) bool { return ix.starts[i] > off }) - 1
	col := 1
	for _, r := range ix.text[ix.starts[line]:off] {
		if r == utf8.RuneError {
			col++
			continue
		}
		col += utf16.RuneLen(r)
	}
	return line + 1, col
}

// Position exposes the line/column computation for callers that need to
// locate other offsets (e.g. a column reference) in the same text.
func Position(text string, off int) (line, col int) { return newLineIndex(text).position(off) }

// Package mysql reads MySQL and MariaDB databases for dbscope
// (docs/ENGINES.md): the ERD from information_schema and read-only queries.
package mysql

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/dbscope/dbscope/internal/query/guard"
)

// Token is one lexical token of a statement.
type Token struct {
	Text string
	// Word is the lower-cased text of a keyword or bare name; empty otherwise.
	Word string
	Semi bool
}

// Tokens splits MySQL text into tokens, dropping whitespace and comments.
// The contents of /*! ... */ and /*M! ... */ comments are kept as tokens:
// MySQL and MariaDB execute them. Optimizer hints (/*+ ... */) are dropped.
func Tokens(s string) []Token {
	var out []Token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '#' || (c == '-' && strings.HasPrefix(s[i:], "--") && (i+2 == len(s) || s[i+2] <= ' ')):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return out
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "/*!") || strings.HasPrefix(s[i:], "/*M!"):
			// Executable comment: skip the marker and an optional version number, keep the body.
			i += strings.Index(s[i:], "!") + 1
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
			end := strings.Index(s[i:], "*/")
			if end < 0 {
				return append(out, Tokens(s[i:])...)
			}
			out = append(out, Tokens(s[i:i+end])...)
			i += end + 2
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return out
			}
			i += end + 4
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' && c != '`' {
					j += 2
					continue
				}
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			j = min(j+1, len(s))
			out = append(out, Token{Text: s[i:j]})
			i = j
		case c == ';':
			out = append(out, Token{Text: ";", Semi: true})
			i++
		case isWord(rune(c)):
			j := i
			for j < len(s) && isWord(rune(s[j])) {
				j++
			}
			out = append(out, Token{Text: s[i:j], Word: strings.ToLower(s[i:j])})
			i = j
		case c == ':' && i+1 < len(s) && s[i+1] == '=':
			out = append(out, Token{Text: ":="})
			i += 2
		default:
			out = append(out, Token{Text: s[i : i+1]})
			i++
		}
	}
	return out
}

func isWord(r rune) bool {
	return r == '_' || r == '$' || r == '@' || unicode.IsLetter(r) || unicode.IsDigit(r) || r >= 0x80
}

// sideEffects are functions with effects outside the query: sleeping or
// waiting, user locks, and reading files on the server.
var sideEffects = map[string]bool{
	"sleep": true, "benchmark": true, "get_lock": true, "release_lock": true, "release_all_locks": true,
	"load_file": true, "master_pos_wait": true, "source_pos_wait": true, "wait_for_executed_gtid_set": true,
	"wait_until_sql_thread_after_gtids": true,
}

// Check accepts one read-only statement: SELECT, WITH, TABLE, VALUES,
// EXPLAIN, DESCRIBE or SHOW. It is a courtesy that explains refusals early;
// Run's READ ONLY transaction is what the server enforces. It refuses what a
// read-only transaction still allows: writing files with INTO OUTFILE,
// setting variables, locking reads, and functions with side effects.
func Check(sqlText string) error {
	toks := Tokens(sqlText)
	statements, nonEmpty := 0, false
	var words []string
	for i, t := range toks {
		if t.Semi {
			if nonEmpty {
				statements++
			}
			nonEmpty = false
			continue
		}
		nonEmpty = true
		if t.Word != "" {
			words = append(words, t.Word)
			if sideEffects[t.Word] && i+1 < len(toks) && toks[i+1].Text == "(" {
				return &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf("%s has effects outside the query and is not allowed", strings.ToUpper(t.Word))}
			}
		}
		if t.Text == ":=" {
			return &guard.Error{Reason: guard.WritesData, Message: "Assigning a variable with := changes the session and is not allowed"}
		}
	}
	if nonEmpty {
		statements++
	}
	switch {
	case statements > 1:
		return &guard.Error{Reason: guard.MultipleStatements, Message: "Run one statement at a time"}
	case len(words) == 0:
		return &guard.Error{Reason: guard.Syntax, Message: "The statement is empty"}
	}
	switch words[0] {
	case "select", "with", "table", "values", "explain", "describe", "desc", "show":
	default:
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only read-only queries run here; %s is not one", strings.ToUpper(words[0]))}
	}
	for i, w := range words {
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		switch {
		case w == "into":
			return &guard.Error{Reason: guard.SelectInto, Message: "SELECT ... INTO writes a file or sets variables and is not allowed"}
		case w == "for" && (next == "update" || next == "share"):
			return &guard.Error{Reason: guard.LockingClause, Message: "FOR UPDATE and FOR SHARE lock rows and are not allowed"}
		case w == "lock" && next == "in":
			return &guard.Error{Reason: guard.LockingClause, Message: "LOCK IN SHARE MODE locks rows and is not allowed"}
		}
	}
	return nil
}

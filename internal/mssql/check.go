package mssql

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/dbscope/dbscope/internal/query/guard"
)

// Token is one lexical token of a T-SQL statement.
type Token struct {
	Text string
	// Word is the lower-cased text of a keyword or bare name; empty for
	// strings and [bracketed] or "quoted" names.
	Word string
	Semi bool
}

// Tokens splits T-SQL text into tokens, dropping whitespace and comments.
// Block comments nest in T-SQL.
func Tokens(s string) []Token {
	var out []Token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return out
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "/*"):
			depth := 0
			for i < len(s) {
				if strings.HasPrefix(s[i:], "/*") {
					depth, i = depth+1, i+2
				} else if strings.HasPrefix(s[i:], "*/") {
					depth, i = depth-1, i+2
					if depth == 0 {
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
			for j < len(s) {
				if s[j] == end {
					if j+1 < len(s) && s[j+1] == end {
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
		default:
			out = append(out, Token{Text: s[i : i+1]})
			i++
		}
	}
	return out
}

func isWord(r rune) bool {
	return r == '_' || r == '@' || r == '#' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) || r >= 0x80
}

// writeWords are reserved words that begin a statement which changes data,
// schema, permissions or the session. T-SQL needs no semicolon between
// statements, so "SELECT 1 DELETE FROM t" is two statements; as reserved
// words they cannot be bare column names. MERGE is left to the plan gate
// because it is also a join hint.
var writeWords = map[string]bool{
	"insert": true, "update": true, "delete": true, "truncate": true, "drop": true, "alter": true, "create": true,
	"grant": true, "revoke": true, "deny": true, "declare": true, "set": true, "use": true, "backup": true,
	"restore": true, "dbcc": true, "kill": true, "shutdown": true, "reconfigure": true, "bulk": true,
	"exec": true, "execute": true, "begin": true, "commit": true, "rollback": true, "save": true,
}

// lockHints are table hints that take locks a plain read does not.
var lockHints = map[string]bool{"updlock": true, "xlock": true, "tablockx": true, "holdlock": true}

// Check accepts one SELECT, with or without a WITH clause. It is a courtesy
// that explains refusals early; Run's plan gate, which reads the statement
// type the server compiles, is what holds. It also refuses what that gate
// would let through: SELECT INTO, reaching other servers or files with
// OPENROWSET, OPENQUERY or OPENDATASOURCE, advancing a sequence, WAITFOR and
// locking table hints.
func Check(sqlText string) error {
	toks := Tokens(sqlText)
	statements, nonEmpty := 0, false
	var words []string
	for _, t := range toks {
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
	if words[0] != "select" && words[0] != "with" {
		return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("Only read-only queries run here; %s is not one", strings.ToUpper(words[0]))}
	}
	for i, w := range words {
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		switch {
		case w == "into":
			return &guard.Error{Reason: guard.SelectInto, Message: "SELECT ... INTO creates a table and is not allowed"}
		case writeWords[w]:
			return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("%s is not part of a read-only query; run one SELECT", strings.ToUpper(w))}
		case w == "openrowset" || w == "openquery" || w == "opendatasource":
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf("%s reaches another server or a file and is not allowed", strings.ToUpper(w))}
		case w == "next" && next == "value":
			return &guard.Error{Reason: guard.WritesData, Message: "NEXT VALUE FOR advances a sequence and is not allowed"}
		case w == "waitfor":
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: "WAITFOR holds the connection and is not allowed"}
		case lockHints[w]:
			return &guard.Error{Reason: guard.LockingClause, Message: fmt.Sprintf("The %s hint takes locks a read does not need and is not allowed", strings.ToUpper(w))}
		}
	}
	return nil
}

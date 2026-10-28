package oracle

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/dbscope/dbscope/internal/query/guard"
)

// Token is one lexical token of an Oracle SQL statement.
type Token struct {
	Text string
	// Word is the lower-cased text of a keyword or bare name; empty for
	// strings and "quoted" names.
	Word string
	Semi bool
}

// Tokens splits Oracle SQL into tokens, dropping whitespace and comments.
// It reads q'[...]' alternative quoting, whose body may hold a quote.
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
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return out
			}
			i += j + 4
		case (c == 'q' || c == 'Q') && i+2 < len(s) && s[i+1] == '\'':
			// q'X...X' where X is any delimiter, or a bracket closed by its pair.
			open := s[i+2]
			closing := map[byte]byte{'[': ']', '(': ')', '{': '}', '<': '>'}[open]
			if closing == 0 {
				closing = open
			}
			end := strings.Index(s[i+3:], string(closing)+"'")
			j := len(s)
			if end >= 0 {
				j = i + 3 + end + 2
			}
			out = append(out, Token{Text: s[i:j]})
			i = j
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) {
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
			for j < len(s) && (isWord(rune(s[j])) || s[j] == '.' && j+1 < len(s) && isWord(rune(s[j+1]))) {
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
	return r == '_' || r == '$' || r == '#' || unicode.IsLetter(r) || unicode.IsDigit(r) || r >= 0x80
}

// writeWords begin statements that change data, schema, permissions, the
// transaction or the session, or run PL/SQL. They are reserved or keywords
// that cannot appear bare in a query.
var writeWords = map[string]bool{
	"insert": true, "update": true, "delete": true, "merge": true, "truncate": true, "drop": true, "alter": true,
	"create": true, "grant": true, "revoke": true, "rename": true, "comment": true, "begin": true, "declare": true,
	"call": true, "exec": true, "execute": true, "lock": true, "commit": true, "rollback": true, "savepoint": true,
	"set": true, "analyze": true, "audit": true, "noaudit": true, "flashback": true, "purge": true,
}

// safePackages are the dbms_ packages a read may call: they compute, they do
// not reach files, the network, other sessions or dynamic SQL.
var safePackages = map[string]bool{"dbms_lob": true, "dbms_random": true, "dbms_crypto": true}

// Check accepts one SELECT, with or without a WITH clause. Run's READ ONLY
// transaction is what the server enforces; Check refuses what that
// transaction would allow or what would end it: DDL (which commits), PL/SQL,
// FOR UPDATE, and calls into dbms_ and utl_ packages, which reach files, the
// network or dynamic SQL.
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
		pkg, _, _ := strings.Cut(strings.TrimPrefix(w, "sys."), ".")
		switch {
		case w == "for" && next == "update":
			return &guard.Error{Reason: guard.LockingClause, Message: "FOR UPDATE locks rows and is not allowed"}
		case w == "into":
			return &guard.Error{Reason: guard.SelectInto, Message: "INTO belongs to PL/SQL or an INSERT and is not allowed"}
		case writeWords[w]:
			return &guard.Error{Reason: guard.NotSelect, Message: fmt.Sprintf("%s is not part of a read-only query; run one SELECT", strings.ToUpper(w))}
		case (strings.HasPrefix(pkg, "dbms_") && !safePackages[pkg]) || strings.HasPrefix(pkg, "utl_"):
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf("%s can reach files, the network, other sessions or dynamic SQL and is not allowed", strings.ToUpper(pkg))}
		}
	}
	return nil
}

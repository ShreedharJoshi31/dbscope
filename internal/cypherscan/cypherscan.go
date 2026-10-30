// Package cypherscan reads Cypher text into tokens and finds clauses that
// write, for every place dbscope must refuse writing Cypher before it runs:
// Neo4j and Memgraph queries, and Apache AGE's cypher() inside PostgreSQL.
package cypherscan

import (
	"strings"
	"unicode"
)

type Token struct {
	Text string
	Word string // lowercased unquoted word; "" for literals, quoted names and punctuation
	Pos  int    // byte offset in the source
}

// Tokens splits Cypher into words, literals and punctuation, dropping
// comments; backquoted names and strings are never words.
func Tokens(s string) []Token {
	var out []Token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case strings.HasPrefix(s[i:], "//"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = len(s)
			} else {
				i += end + 4
			}
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' && c != '`' {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			out = append(out, Token{Text: s[i:j], Pos: i})
			i = j
		case c == '_' || unicode.IsLetter(rune(c)):
			j := i
			for j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			out = append(out, Token{Text: s[i:j], Word: strings.ToLower(s[i:j]), Pos: i})
			i = j
		default:
			out = append(out, Token{Text: string(c), Pos: i})
			i++
		}
	}
	return out
}

// Writing are clauses and commands that change data, schema, users or
// databases, by their lower-case first word.
var Writing = map[string]string{"create": "CREATE", "merge": "MERGE", "delete": "DELETE", "detach": "DETACH DELETE", "set": "SET",
	"remove": "REMOVE", "foreach": "FOREACH", "drop": "DROP", "alter": "ALTER", "grant": "GRANT", "revoke": "REVOKE", "deny": "DENY",
	"rename": "RENAME", "terminate": "TERMINATE", "enable": "ENABLE", "deallocate": "DEALLOCATE", "reallocate": "REALLOCATE"}

// WriteClause names the first clause in the tokens that writes or commits
// on its own (LOAD CSV, CALL { } IN TRANSACTIONS), or returns "". A word
// after a dot is a property or procedure name part, not a clause.
func WriteClause(toks []Token) string {
	for i, t := range toks {
		if i > 0 && toks[i-1].Text == "." {
			continue
		}
		switch {
		case Writing[t.Word] != "":
			return Writing[t.Word]
		case t.Word == "load" && i+1 < len(toks) && toks[i+1].Word == "csv":
			return "LOAD CSV"
		case t.Word == "in" && i+1 < len(toks) && toks[i+1].Word == "transactions":
			return "CALL { } IN TRANSACTIONS"
		}
	}
	return ""
}

// Package retrieve picks the part of a schema model a question needs and
// renders it as compact DDL for the prompt (docs/NL.md "Retrieval" and
// "Prompt hygiene"). Everything here is deterministic: the same model,
// glossary and question always give the same bytes, so provider prompt
// caches hit.
package retrieve

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// abbreviations expands common identifier abbreviations; both the raw and
// expanded tokens are kept.
var abbreviations = map[string]string{
	"cust": "customer", "qty": "quantity", "amt": "amount", "dt": "date", "addr": "address",
	"num": "number", "txn": "transaction", "acct": "account", "org": "organization",
	"usr": "user", "prod": "product", "cat": "category", "desc": "description",
}

// stopWords are dropped from questions (not from schema documents).
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "in": true, "on": true, "for": true, "to": true,
	"by": true, "with": true, "and": true, "or": true, "is": true, "are": true, "was": true, "were": true,
	"what": true, "which": true, "who": true, "whose": true, "how": true, "many": true, "much": true,
	"show": true, "list": true, "give": true, "me": true, "find": true, "get": true, "per": true,
	"each": true, "all": true, "every": true, "do": true, "does": true, "did": true, "have": true,
	"has": true, "that": true, "this": true, "there": true, "their": true, "it": true, "its": true,
	"from": true, "at": true, "as": true, "be": true, "than": true, "more": true, "most": true,
	"top": true, "please": true, "we": true, "our": true, "i": true, "my": true, "any": true,
}

// split breaks s into lower-case word pieces: on non-alphanumerics,
// between letters and digits, and at camelCase boundaries (acronym-aware:
// "HTTPServer" is "http", "server").
func split(s string) []string {
	var out []string
	runes := []rune(s)
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			out = append(out, strings.ToLower(string(runes[start:end])))
		}
		start = -1
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		prev := runes[i-1]
		switch {
		case unicode.IsDigit(r) != unicode.IsDigit(prev):
			flush(i)
			start = i
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			// userId -> user | Id
			flush(i)
			start = i
		case unicode.IsLower(r) && unicode.IsUpper(prev) && i-1 > start:
			// HTTPServer: the S starts the next word -> HTTP | Server
			flush(i - 1)
			start = i - 1
		}
	}
	flush(len(runes))
	return out
}

// fold applies light plural folding to tokens longer than 3 characters,
// chosen so a singular and its plural fold to the same token.
func fold(t string) string {
	if utf8.RuneCountInString(t) <= 3 {
		return t
	}
	switch {
	case strings.HasSuffix(t, "ies") && len(t) > 4:
		return t[:len(t)-3] + "y"
	case strings.HasSuffix(t, "sses"), strings.HasSuffix(t, "uses"), strings.HasSuffix(t, "xes"),
		strings.HasSuffix(t, "ches"), strings.HasSuffix(t, "shes"), strings.HasSuffix(t, "zzes"):
		return t[:len(t)-2]
	case strings.HasSuffix(t, "ss"), strings.HasSuffix(t, "us"), strings.HasSuffix(t, "is"):
		return t
	case strings.HasSuffix(t, "s"):
		return t[:len(t)-1]
	}
	return t
}

// Tokenize turns an identifier or text into index terms: split, lower-cased,
// plural-folded, with the expansion of an abbreviation added after the raw
// token. Glossary terms are expanded by Select, since they can be phrases.
func Tokenize(s string) []string {
	var out []string
	for _, p := range split(s) {
		out = append(out, expand(p)...)
	}
	return out
}

func expand(piece string) []string {
	t := fold(piece)
	if full, ok := abbreviations[piece]; ok {
		return []string{t, fold(full)}
	}
	return []string{t}
}

// queryTokens tokenises a question, dropping stop words before folding.
func queryTokens(q string) []string {
	var out []string
	for _, p := range split(q) {
		if !stopWords[p] {
			out = append(out, expand(p)...)
		}
	}
	return out
}

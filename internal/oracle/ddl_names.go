package oracle

import "strings"

// ident is an identifier token as Oracle stores it: a bare name upper-cased,
// a "quoted" one unquoted with its case kept.
func ident(t Token) string {
	if strings.HasPrefix(t.Text, `"`) {
		return strings.ReplaceAll(strings.Trim(t.Text, `"`), `""`, `"`)
	}
	return strings.ToUpper(t.Text)
}

// qname reads a possibly owner-qualified name at toks[i]. The tokenizer
// keeps a bare app.t as one word; "APP"."T" and app."T" are three tokens.
func qname(toks []Token, i int) (owner, name string, next int) {
	if i >= len(toks) {
		return "", "", i
	}
	t := toks[i]
	if t.Word != "" && strings.Contains(t.Text, ".") {
		parts := strings.Split(t.Text, ".")
		name = strings.ToUpper(parts[len(parts)-1])
		if len(parts) > 1 {
			owner = strings.ToUpper(parts[len(parts)-2])
		}
		return owner, name, i + 1
	}
	name, next = ident(t), i+1
	if next+1 < len(toks) && toks[next].Text == "." && isName(toks[next+1]) {
		owner, name, next = name, ident(toks[next+1]), next+2
	}
	return owner, name, next
}

func isName(t Token) bool { return t.Word != "" || strings.HasPrefix(t.Text, `"`) }

// words returns the lower-case words of tokens, with quoted names unquoted and lower-cased.
func words(toks []Token) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		switch {
		case t.Word != "":
			out = append(out, t.Word)
		case strings.HasPrefix(t.Text, `"`):
			out = append(out, strings.ToLower(ident(t)))
		}
	}
	return out
}

func hasWord(toks []Token, ws ...string) bool {
	for _, t := range toks {
		for _, w := range ws {
			if t.Word == w {
				return true
			}
		}
	}
	return false
}

func indexOf(toks []Token, word string) int {
	for i, t := range toks {
		if t.Word == word {
			return i
		}
	}
	return -1
}

// items splits a parenthesized list starting at toks[i] ("(" ... ")") at
// its top-level commas; without a parenthesis the rest is one item.
func items(toks []Token, i int) [][]Token {
	if i >= len(toks) {
		return nil
	}
	if toks[i].Text != "(" {
		return [][]Token{toks[i:]}
	}
	var out [][]Token
	depth, start := 0, i+1
	for j := i; j < len(toks); j++ {
		switch toks[j].Text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return append(out, toks[start:j])
			}
		case ",":
			if depth == 1 {
				out = append(out, toks[start:j])
				start = j + 1
			}
		}
	}
	return append(out, toks[start:])
}

// typeOf is a column definition's type, upper-case without spaces inside
// the parentheses, e.g. "VARCHAR2(40CHAR)"; def starts at the type.
func typeOf(def []Token) string {
	var b strings.Builder
	depth := 0
	for i, t := range def {
		if i > 0 && depth == 0 && t.Text != "(" {
			break
		}
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		b.WriteString(strings.ToUpper(t.Text))
	}
	return b.String()
}

// hasSeq reports whether w holds seq as consecutive words.
func hasSeq(w []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(w); i++ {
		ok := true
		for j, s := range seq {
			ok = ok && w[i+j] == s
		}
		if ok {
			return true
		}
	}
	return false
}

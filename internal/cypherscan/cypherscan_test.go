package cypherscan

import "testing"

func TestWriteClause(t *testing.T) {
	for q, want := range map[string]string{
		"MATCH (n) RETURN n":                                 "",
		"MATCH (n) WHERE n.note = 'create; set' RETURN n":    "",
		"MATCH (n:`Create`) RETURN n.set // DELETE here":     "",
		"MATCH (n) /* MERGE */ RETURN n.remove":              "",
		"MATCH (n) SET n.x = 1":                              "SET",
		"MATCH (n) DETACH DELETE n":                          "DETACH DELETE",
		"LOAD CSV FROM 'f' AS r RETURN r":                    "LOAD CSV",
		"MATCH (n) CALL { WITH n RETURN n } IN TRANSACTIONS": "CALL { } IN TRANSACTIONS",
		"UNWIND [1] AS i FOREACH (x IN [1] | CREATE (:A))":   "FOREACH",
	} {
		if got := WriteClause(Tokens(q)); got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
	// Cypher escapes a quote with a backslash; the string stays one token, and SET inside it is no clause.
	toks := Tokens("MATCH (a)\n  RETURN 'it\\' SET'")
	if len(toks) != 6 || toks[5].Pos != 19 || toks[0].Word != "match" || toks[5].Word != "" || WriteClause(toks) != "" {
		t.Errorf("tokens %+v", toks)
	}
}

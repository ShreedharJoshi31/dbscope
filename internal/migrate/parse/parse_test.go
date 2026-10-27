package parse

import (
	"slices"
	"strings"
	"testing"
)

func sqls(f *File) []string {
	var out []string
	for _, s := range f.Statements {
		out = append(out, s.SQL)
	}
	return out
}

func TestSplitWholeFile(t *testing.T) {
	text := "-- header comment\nSET lock_timeout = '1s';\n\n/* block\n comment */ ALTER TABLE t\n  ADD COLUMN a int;\nCREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1; SELECT 2 $$;\nSELECT 'a;b' -- trailing\n"
	f := Split(text)
	want := []string{"SET lock_timeout = '1s'", "ALTER TABLE t\n  ADD COLUMN a int",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1; SELECT 2 $$", "SELECT 'a;b'"}
	if got := sqls(f); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	s := f.Statements[1]
	if s.Line != 5 || s.Column != 13 || s.EndLine != 6 || s.Node.GetAlterTableStmt() == nil {
		t.Fatalf("positions %d:%d-%d node %v", s.Line, s.Column, s.EndLine, s.Node)
	}
	for _, s := range f.Statements {
		if s.Err != nil || s.Node == nil {
			t.Fatalf("%q: %v", s.SQL, s.Err)
		}
	}
}

// The scanner split must agree with the parser's own statement boundaries.
func TestScannerSplitMatchesParser(t *testing.T) {
	text := "BEGIN;\nCREATE TABLE a (id int, note text DEFAULT ';');\n" +
		"CREATE FUNCTION g(x int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN x > 0 THEN 1 ELSE 2 END; SELECT x; END;\n" +
		"CREATE OR REPLACE PROCEDURE p() LANGUAGE sql BEGIN ATOMIC INSERT INTO a VALUES (1); END;\n" +
		"DO $body$ BEGIN PERFORM 1; END $body$;\nCOMMENT ON TABLE a IS 'x;y';\nCOMMIT;"
	whole := Split(text)
	toks, err := scan(text)
	if err != nil {
		t.Fatal(err)
	}
	pieces := splitTokens(text, toks, nil, newLineIndex(text))
	if a, b := sqls(whole), sqls(&File{Statements: pieces}); !slices.Equal(a, b) || len(a) != 7 {
		t.Fatalf("parser %q\nscanner %q", a, b)
	}
	for _, p := range pieces {
		if p.Err != nil {
			t.Fatalf("%q: %v", p.SQL, p.Err)
		}
	}
}

func TestPG18SyntaxIsolated(t *testing.T) {
	text := "ALTER TABLE t ADD COLUMN a int;\n" +
		"ALTER TABLE t ADD COLUMN g int GENERATED ALWAYS AS (a + 1) VIRTUAL;\n" +
		"ALTER TABLE t ADD CONSTRAINT nn NOT NULL a NOT VALID;\n" +
		"ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0) NOT ENFORCED;\n" +
		"CREATE INDEX ON t (a);"
	f := Split(text)
	if len(f.Statements) != 5 {
		t.Fatalf("got %q", sqls(f))
	}
	for i, s := range f.Statements {
		bad := i >= 1 && i <= 3
		if (s.Err != nil) != bad || (s.Node == nil) != bad {
			t.Errorf("%d %q: err %v", i, s.SQL, s.Err)
		}
		if s.Line != i+1 {
			t.Errorf("%d: line %d", i, s.Line)
		}
	}
	if e := f.Statements[1].Err; e.Line != 2 || !strings.Contains(e.Message, "VIRTUAL") || e.Column != 60 {
		t.Errorf("virtual error %+v", e)
	}
}

func TestUnterminatedQuote(t *testing.T) {
	text := "ALTER TABLE t ADD COLUMN a int;\nALTER TABLE t ADD COLUMN b text DEFAULT 'oops;\nALTER TABLE t ADD COLUMN c int;"
	f := Split(text)
	if len(f.Statements) != 2 || f.Statements[0].Err != nil || f.Statements[1].Err == nil {
		t.Fatalf("%q %+v", sqls(f), f.Statements)
	}
	if s := f.Statements[1]; s.Line != 2 || s.EndLine != 3 || !strings.Contains(s.Err.Message, "unterminated") {
		t.Fatalf("%+v %+v", s, s.Err)
	}
}

func TestUTF16Columns(t *testing.T) {
	// "é" is one UTF-16 unit (2 bytes in UTF-8); "😀" is two units (4 bytes).
	text := "SELECT 'é😀'; SELECT 1; SELEC 'é' 2;"
	f := Split(text)
	if len(f.Statements) != 3 {
		t.Fatalf("%q", sqls(f))
	}
	if c := f.Statements[1].Column; c != 15 {
		t.Errorf("second statement column %d, want 15", c)
	}
	if c := f.Statements[2].Column; c != 25 {
		t.Errorf("third statement column %d, want 25", c)
	}
	if e := f.Statements[2].Err; e == nil || e.Column != 25 {
		t.Errorf("error %+v", e)
	}
}

func TestMetaCommands(t *testing.T) {
	text := "\\restrict abcDEF123\nSET statement_timeout = 0;\nCREATE TABLE a (id int);\n\\unrestrict abcDEF123\n"
	f := Split(text)
	if got := sqls(f); !slices.Equal(got, []string{"SET statement_timeout = 0", "CREATE TABLE a (id int)"}) {
		t.Fatalf("%q", got)
	}
	if len(f.Meta) != 2 || f.Meta[0].Line != 1 || f.Meta[1].Command != "\\unrestrict abcDEF123" {
		t.Fatalf("meta %+v", f.Meta)
	}
	if f.Statements[1].Line != 3 {
		t.Fatalf("line %d", f.Statements[1].Line)
	}
}

func TestEmptyAndCommentOnly(t *testing.T) {
	for _, text := range []string{"", "  \n", "-- nothing\n/* here */", ";;"} {
		if f := Split(text); len(f.Statements) != 0 {
			t.Errorf("%q: %q", text, sqls(f))
		}
	}
}

package oracle

import (
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	script := `SET DEFINE OFF
-- a comment; with a semicolon
ALTER TABLE app.t ADD z NUMBER;
CREATE OR REPLACE PROCEDURE app.p IS
BEGIN
  UPDATE app.t SET a = 1; -- inner semicolon
  COMMIT;
END;
/
BEGIN
  EXECUTE IMMEDIATE 'ALTER TABLE app.t ADD y NUMBER';
END;
/
INSERT INTO app.t (b) VALUES ('a;b
/')
/
SET TRANSACTION READ WRITE;
EXEC dbms_stats.gather_table_stats('APP', 'T')
  CREATE INDEX app.i ON app.t (a) ONLINE`
	got := Split(script)
	want := []struct {
		first        string
		line, col    int
		block, cli   bool
		endsWithSemi bool
	}{
		{"SET DEFINE OFF", 1, 1, false, true, false},
		{"ALTER TABLE", 3, 1, false, false, false},
		{"CREATE OR REPLACE PROCEDURE", 4, 1, true, false, true},
		{"BEGIN", 10, 1, true, false, true},
		{"INSERT INTO", 14, 1, false, false, false},
		{"SET TRANSACTION", 17, 1, false, false, false},
		{"EXEC dbms_stats", 18, 1, true, false, false},
		{"CREATE INDEX", 19, 3, false, false, false},
	}
	if len(got) != len(want) {
		for _, p := range got {
			t.Logf("%d:%d %q", p.Line, p.Column, p.SQL)
		}
		t.Fatalf("%d pieces, want %d", len(got), len(want))
	}
	for i, w := range want {
		p := got[i]
		if !strings.HasPrefix(p.SQL, w.first) || p.Line != w.line || p.Column != w.col || p.Block != w.block || p.Client != w.cli ||
			strings.HasSuffix(p.SQL, ";") != w.endsWithSemi {
			t.Errorf("piece %d = %+v, want %+v", i, p, w)
		}
	}
	if !strings.Contains(got[4].SQL, "a;b\n/'") {
		t.Errorf("string literal split: %q", got[4].SQL)
	}
	if got[2].EndLine != 8 {
		t.Errorf("procedure ends on line %d, want 8", got[2].EndLine)
	}
}

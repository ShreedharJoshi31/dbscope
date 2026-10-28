package sqlite

import (
	"slices"
	"testing"
)

func TestSplit(t *testing.T) {
	script := `-- header
CREATE TABLE a (x TEXT DEFAULT ';');
/* block; comment */
CREATE TEMP TRIGGER t AFTER INSERT ON a BEGIN
  UPDATE a SET x = 'end;' WHERE rowid = new.rowid;
  SELECT CASE WHEN 1 THEN 2 END;
END;
INSERT INTO "we;ird" VALUES ('it''s; fine');
EXPLAIN SELECT 1;
CREATE VIEW v AS SELECT 1
`
	got := Split(script)
	var sqls []string
	var lines []int
	for _, p := range got {
		sqls = append(sqls, p.SQL)
		lines = append(lines, p.Line)
	}
	want := []string{
		"CREATE TABLE a (x TEXT DEFAULT ';')",
		"CREATE TEMP TRIGGER t AFTER INSERT ON a BEGIN\n  UPDATE a SET x = 'end;' WHERE rowid = new.rowid;\n  SELECT CASE WHEN 1 THEN 2 END;\nEND",
		`INSERT INTO "we;ird" VALUES ('it''s; fine')`,
		"EXPLAIN SELECT 1",
		"CREATE VIEW v AS SELECT 1",
	}
	if !slices.Equal(sqls, want) {
		t.Fatalf("got %q", sqls)
	}
	if !slices.Equal(lines, []int{2, 4, 8, 9, 10}) {
		t.Errorf("lines %v", lines)
	}
	if got[1].EndLine != 7 {
		t.Errorf("trigger end line %d", got[1].EndLine)
	}
	if n := len(Split(" ;; -- nothing\n")); n != 0 {
		t.Errorf("empty script gave %d statements", n)
	}
}

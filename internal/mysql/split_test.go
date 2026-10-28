package mysql

import (
	"slices"
	"testing"
)

func TestSplit(t *testing.T) {
	script := "-- header\nALTER TABLE t ADD COLUMN a INT DEFAULT ';';\n/* x; */ CREATE INDEX i ON t (a);\n" +
		"DELIMITER $$\nCREATE TRIGGER tr BEFORE INSERT ON t FOR EACH ROW BEGIN\n  SET NEW.a = 1;\n  SET NEW.b = 2;\nEND$$\nDELIMITER ;\n" +
		"DROP TABLE `x;y`;\nSELECT 1"
	var sqls []string
	var lines []int
	for _, p := range Split(script) {
		sqls = append(sqls, p.SQL)
		lines = append(lines, p.Line)
	}
	want := []string{
		"ALTER TABLE t ADD COLUMN a INT DEFAULT ';'",
		"CREATE INDEX i ON t (a)",
		"CREATE TRIGGER tr BEFORE INSERT ON t FOR EACH ROW BEGIN\n  SET NEW.a = 1;\n  SET NEW.b = 2;\nEND",
		"DROP TABLE `x;y`",
		"SELECT 1",
	}
	if !slices.Equal(sqls, want) {
		t.Fatalf("got %q", sqls)
	}
	if !slices.Equal(lines, []int{2, 3, 5, 10, 11}) {
		t.Errorf("lines %v", lines)
	}
}

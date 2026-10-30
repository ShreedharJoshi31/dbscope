package mssql

import (
	"fmt"
	"slices"
	"testing"
)

func TestSplit(t *testing.T) {
	script := `-- header; not a statement
SET LOCK_TIMEOUT 5000;
ALTER TABLE t ADD x int NULL
GO
/* GO inside a comment
GO
*/
CREATE INDEX i ON t (a)
  WITH (ONLINE = ON)
go 2
UPDATE t
SET a = 'it''s; fine'
ALTER TABLE [t;x] DROP COLUMN y
CREATE PROCEDURE p AS
BEGIN
  UPDATE t SET a = 1
  DELETE FROM t
END
GO
IF NOT EXISTS (SELECT 1 FROM sys.columns WHERE name = 'z')
BEGIN
  ALTER TABLE t ADD z int NULL;
END
GO`
	var got []string
	for _, p := range Split(script) {
		got = append(got, fmt.Sprintf("%d:%d:%d b%d %s", p.Line, p.Column, p.EndLine, p.Batch, p.SQL))
	}
	want := []string{
		"2:1:2 b0 SET LOCK_TIMEOUT 5000",
		"3:1:3 b0 ALTER TABLE t ADD x int NULL",
		"8:1:9 b1 CREATE INDEX i ON t (a)\n  WITH (ONLINE = ON)",
		"11:1:12 b2 UPDATE t\nSET a = 'it''s; fine'",
		"13:1:13 b2 ALTER TABLE [t;x] DROP COLUMN y",
		"14:1:18 b2 CREATE PROCEDURE p AS\nBEGIN\n  UPDATE t SET a = 1\n  DELETE FROM t\nEND",
		"20:1:21 b3 IF NOT EXISTS (SELECT 1 FROM sys.columns WHERE name = 'z')\nBEGIN",
		"22:3:22 b3 ALTER TABLE t ADD z int NULL",
		"23:1:23 b3 END",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

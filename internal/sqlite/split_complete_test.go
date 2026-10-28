package sqlite

import (
	"testing"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// complete is Split's notion of sqlite3_complete: the script ends in state START.
func complete(s string) bool {
	state := uint8(0)
	for i := 0; i < len(s); {
		j, tok := nextToken(s, i)
		if tok == tkOpen {
			return false
		}
		state = completeTrans[state][tok]
		i = j
	}
	return state == 1
}

// TestSplitMatchesSQLite compares the ported state machine with the real
// sqlite3_complete on every prefix of scripts that exercise triggers,
// strings, comments and EXPLAIN.
func TestSplitMatchesSQLite(t *testing.T) {
	tls := libc.NewTLS()
	defer tls.Close()
	scripts := []string{
		"CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET x=1; DELETE FROM b; END; SELECT 1;",
		"create temporary trigger t before delete on a for each row begin select case when 1 then 'end;' end; end ;",
		"EXPLAIN CREATE TRIGGER t AFTER UPDATE ON a BEGIN SELECT 1; END; EXPLAIN QUERY PLAN SELECT 2;",
		"CREATE TABLE trigger (end TEXT); SELECT end FROM trigger; CREATE VIEW temp AS SELECT 1;",
		"SELECT 'a;b' -- c;\n; /* d; */ SELECT \"e;f\", [g;h], `i;j`;",
		"CREATE TEMP TABLE x(a); CREATE TRIGGER y INSTEAD OF INSERT ON v BEGIN INSERT INTO x VALUES (new.a); END; END;",
	}
	for _, s := range scripts {
		for n := 1; n <= len(s); n++ {
			prefix := s[:n]
			p, err := libc.CString(prefix)
			if err != nil {
				t.Fatal(err)
			}
			want := sqlite3.Xsqlite3_complete(tls, p) == 1
			libc.Xfree(tls, p)
			if got := complete(prefix); got != want {
				t.Fatalf("prefix %q: Split says complete=%v, sqlite3_complete says %v", prefix, got, want)
			}
		}
	}
}

package timeouts

import (
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/parse"
)

// run steps through sql and returns, for each statement, whether a
// lock_timeout was in effect and the IDs of the findings it produced.
func run(t *testing.T, m *Machine, sql string) ([]bool, [][]string) {
	t.Helper()
	f := parse.Split(sql)
	var lt []bool
	var ids [][]string
	for _, s := range f.Statements {
		if s.Err != nil {
			t.Fatalf("%q: %v", s.SQL, s.Err)
		}
		st, fs := m.Step(s.Node)
		lt = append(lt, st.LockTimeout)
		var x []string
		for _, f := range fs {
			x = append(x, f.ID)
		}
		ids = append(ids, x)
	}
	return lt, ids
}

func eq(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTimeoutStates(t *testing.T) {
	const ddl = "ALTER TABLE t ADD COLUMN c int"
	for _, c := range []struct {
		name    string
		ext     External
		wrapped bool
		sql     string
		want    []bool
		finding int // index of the statement with DBS-SET-LOCAL-NO-TX, or -1
	}{
		{name: "none", sql: ddl, want: []bool{false}, finding: -1},
		{name: "session SET", sql: "SET lock_timeout = '2s';" + ddl, want: []bool{false, true}, finding: -1},
		{name: "SET TO 0 disables", sql: "SET lock_timeout = '2s'; SET lock_timeout = 0;" + ddl, want: []bool{false, true, false}, finding: -1},
		{name: "SET LOCAL outside a transaction", sql: "SET LOCAL lock_timeout = '2s';" + ddl, want: []bool{false, false}, finding: 0},
		{name: "SET LOCAL inside BEGIN", sql: "BEGIN; SET LOCAL lock_timeout = '2s';" + ddl + "; COMMIT;" + ddl,
			want: []bool{false, false, true, true, false}, finding: -1},
		{name: "SET LOCAL in a wrapped file", wrapped: true, sql: "SET LOCAL lock_timeout = '2s';" + ddl, want: []bool{false, true}, finding: -1},
		{name: "ROLLBACK undoes SET", sql: "BEGIN; SET lock_timeout = '2s'; ROLLBACK;" + ddl, want: []bool{false, false, true, false}, finding: -1},
		{name: "COMMIT keeps SET", sql: "BEGIN; SET lock_timeout = '2s'; COMMIT;" + ddl, want: []bool{false, false, true, true}, finding: -1},
		{name: "RESET", sql: "SET lock_timeout = '2s'; RESET lock_timeout;" + ddl, want: []bool{false, true, false}, finding: -1},
		{name: "RESET ALL", sql: "SET lock_timeout = '2s'; RESET ALL;" + ddl, want: []bool{false, true, false}, finding: -1},
		{name: "SET TO DEFAULT", sql: "SET lock_timeout TO DEFAULT;" + ddl, ext: External{LockTimeout: "5s"}, want: []bool{true, true}, finding: -1},
		{name: "role setting", ext: External{LockTimeout: "5s"}, sql: ddl, want: []bool{true}, finding: -1},
		{name: "role setting 0", ext: External{LockTimeout: "0"}, sql: ddl, want: []bool{false}, finding: -1},
		{name: "RESET returns to role setting", ext: External{LockTimeout: "5s"}, sql: "SET lock_timeout = 0; RESET lock_timeout;" + ddl,
			want: []bool{true, false, true}, finding: -1},
		{name: "assumeLockTimeout", ext: External{Assume: true}, sql: ddl, want: []bool{true}, finding: -1},
		{name: "set_config session", sql: "SELECT set_config('lock_timeout', '1s', false);" + ddl, want: []bool{false, true}, finding: -1},
		{name: "set_config local outside a transaction", sql: "SELECT set_config('lock_timeout', '1s', true);" + ddl, want: []bool{false, false}, finding: 0},
		{name: "set_config local inside", sql: "BEGIN; SELECT pg_catalog.set_config('lock_timeout', '1s', true);" + ddl,
			want: []bool{false, false, true}, finding: -1},
		{name: "savepoint rollback", sql: "BEGIN; SAVEPOINT a; SET LOCAL lock_timeout = '1s'; ROLLBACK TO SAVEPOINT a;" + ddl,
			want: []bool{false, false, false, true, false}, finding: -1},
		{name: "savepoint release keeps", sql: "BEGIN; SAVEPOINT a; SET LOCAL lock_timeout = '1s'; RELEASE a;" + ddl,
			want: []bool{false, false, false, true, true}, finding: -1},
		{name: "SET overrides SET LOCAL", sql: "BEGIN; SET LOCAL lock_timeout = '1s'; SET lock_timeout = 0;" + ddl,
			want: []bool{false, false, true, false}, finding: -1},
		{name: "statement_timeout is not a lock_timeout", sql: "SET statement_timeout = '5s';" + ddl, want: []bool{false, false}, finding: -1},
		{name: "integer milliseconds", sql: "SET lock_timeout = 1500;" + ddl, want: []bool{false, true}, finding: -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := New(c.ext)
			m.StartFile(c.wrapped)
			got, ids := run(t, m, c.sql)
			if !eq(got, c.want) {
				t.Errorf("lock_timeout per statement %v, want %v", got, c.want)
			}
			for i, x := range ids {
				has := len(x) == 1 && x[0] == "DBS-SET-LOCAL-NO-TX"
				if has != (i == c.finding) {
					t.Errorf("statement %d findings %v", i, x)
				}
			}
		})
	}
}

func TestFilesStartFresh(t *testing.T) {
	m := New(External{})
	m.StartFile(false)
	run(t, m, "SET lock_timeout = '2s'")
	m.EndFile()
	m.StartFile(false)
	if m.Current().LockTimeout {
		t.Fatal("a SET in one file carried over to the next")
	}
}

func TestStatementTimeoutTracked(t *testing.T) {
	m := New(External{})
	m.StartFile(false)
	run(t, m, "SET statement_timeout = '5s'")
	if st := m.Current(); st.StatementTimeout != "5s" || st.LockTimeout {
		t.Fatalf("%+v", st)
	}
}

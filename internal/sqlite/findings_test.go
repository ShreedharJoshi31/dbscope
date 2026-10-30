package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
)

const findingsFixture = `
CREATE TABLE p (id INTEGER PRIMARY KEY, t TEXT COLLATE NOCASE UNIQUE, a INT, b INT, UNIQUE (a, b));
CREATE TABLE c_none (id INTEGER PRIMARY KEY, pid INT REFERENCES p);
CREATE TABLE c_idx (id INTEGER PRIMARY KEY, pid INT REFERENCES p);
CREATE INDEX c_idx_pid ON c_idx (pid);
CREATE TABLE c_rev (id INTEGER PRIMARY KEY, a INT, b INT, FOREIGN KEY (a, b) REFERENCES p (a, b));
CREATE INDEX c_rev_ba ON c_rev (b, a);
CREATE TABLE c_half (id INTEGER PRIMARY KEY, a INT, b INT, FOREIGN KEY (a, b) REFERENCES p (a, b));
CREATE INDEX c_half_a ON c_half (a);
CREATE TABLE c_part_ok (id INTEGER PRIMARY KEY, pid INT REFERENCES p);
CREATE INDEX c_part_ok_pid ON c_part_ok (pid) WHERE pid IS NOT NULL;
CREATE TABLE c_part_nn (id INTEGER PRIMARY KEY, pid INT REFERENCES p);
CREATE INDEX c_part_nn_pid ON c_part_nn (pid) WHERE pid NOTNULL;
CREATE TABLE c_part_nn2 (id INTEGER PRIMARY KEY, pid INT REFERENCES p);
CREATE INDEX c_part_nn2_pid ON c_part_nn2 (pid) WHERE pid NOT NULL;
CREATE TABLE c_part_and (id INTEGER PRIMARY KEY, a INT, b INT, FOREIGN KEY (a, b) REFERENCES p (a, b));
CREATE INDEX c_part_and_ab ON c_part_and (a, b) WHERE a IS NOT NULL AND (b IS NOT NULL);
CREATE TABLE c_part_bad (id INTEGER PRIMARY KEY, pid INT REFERENCES p, s TEXT);
CREATE INDEX c_part_bad_pid ON c_part_bad (pid) WHERE s <> 'closed';
CREATE TABLE c_coll (id INTEGER PRIMARY KEY, t TEXT REFERENCES p (t));
CREATE INDEX c_coll_t ON c_coll (t);
CREATE TABLE c_coll_ok (id INTEGER PRIMARY KEY, t TEXT REFERENCES p (t));
CREATE INDEX c_coll_ok_t ON c_coll_ok (t COLLATE NOCASE);
CREATE TABLE c_wr (pid INT REFERENCES p, x INT, PRIMARY KEY (pid, x)) WITHOUT ROWID;
CREATE TABLE c_alias (pid INTEGER PRIMARY KEY REFERENCES p);
CREATE TABLE c_expr (id INTEGER PRIMARY KEY, pid INT REFERENCES p);
CREATE INDEX c_expr_pid ON c_expr (pid + 0);

CREATE TABLE aff (f "FLOATING POINT", s STRING, n ANY, bin BYTEA, vb VARBINARY(16),
  d DATETIME, r REAL, i INTEGER, bi BIGINT, dp "DOUBLE PRECISION", v VARCHAR(20), dec DECIMAL(10,2),
  bo BOOLEAN, iv INTERVAL, pt POINT, blank);
CREATE TABLE aff_strict (n ANY, i INTEGER, r REAL, t TEXT) STRICT;
INSERT INTO aff (f, s, n, bin, vb, d, v) VALUES (3, '007', '007', '007', '007', '2020-01-01', '007');
INSERT INTO aff_strict (n) VALUES ('007');
`

func writeFile(t *testing.T, ddl string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.db")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	w.SetMaxOpenConns(1) // PRAGMA foreign_keys is per connection
	t.Cleanup(func() { w.Close() })
	if _, err := w.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return path, w
}

func introspectFile(t *testing.T, path string) *schema.Model {
	t.Helper()
	db, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := Introspect(context.Background(), db.DB)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func findingsOf(t *testing.T, m *schema.Model, table, rule string) []schema.Finding {
	t.Helper()
	r := m.Relation(schema.QualifiedName{Schema: Schema, Name: table})
	if r == nil {
		t.Fatalf("no table %s", table)
	}
	var out []schema.Finding
	for _, f := range r.Findings {
		if f.RuleID == rule {
			out = append(out, f)
		}
	}
	return out
}

func TestFindingsFKNoIndex(t *testing.T) {
	path, w := writeFile(t, findingsFixture)

	// The planner is the reference: with enforcement on, EXPLAIN QUERY PLAN of a
	// parent delete lists the lookup in each child table.
	if _, err := w.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	plan := map[string]string{}
	for _, q := range []string{`EXPLAIN QUERY PLAN DELETE FROM p WHERE id = 1`, `EXPLAIN QUERY PLAN UPDATE p SET id = 2, t = 'x', a = 1 WHERE id = 1`} {
		rows, err := w.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			table := strings.Fields(detail)[1]
			if prev, ok := plan[table]; ok && prev != detail {
				t.Fatalf("%s planned both %q and %q", table, prev, detail)
			}
			plan[table] = detail
		}
		rows.Close()
	}
	cases := []struct {
		table, plan, message string // message "" means no finding
	}{
		{"c_none", "SCAN c_none", "No index can serve lookups on pid: when foreign keys are enforced, each delete from p, and each change to a key of p, scans all of c_none"},
		{"c_idx", "SEARCH c_idx USING COVERING INDEX c_idx_pid (pid=?)", ""},
		{"c_rev", "SEARCH c_rev USING COVERING INDEX c_rev_ba (b=? AND a=?)", ""},
		{"c_half", "SEARCH c_half USING INDEX c_half_a (a=?)", "Only (a) of the foreign key (a, b) leads an index that can serve its lookups"},
		{"c_part_ok", "SEARCH c_part_ok USING COVERING INDEX c_part_ok_pid (pid=?)", ""},
		{"c_part_nn", "SEARCH c_part_nn USING COVERING INDEX c_part_nn_pid (pid=?)", ""},
		{"c_part_nn2", "SEARCH c_part_nn2 USING COVERING INDEX c_part_nn2_pid (pid=?)", ""},
		{"c_part_and", "SEARCH c_part_and USING COVERING INDEX c_part_and_ab (a=? AND b=?)", ""},
		{"c_part_bad", "SCAN c_part_bad", "(c_part_bad_pid has these columns but is partial, WHERE s <> 'closed', which the lookup does not imply)"},
		{"c_coll", "SCAN c_coll", "(c_coll_t has these columns, but its t uses collation BINARY and the parent key NOCASE)"},
		{"c_coll_ok", "SEARCH c_coll_ok USING COVERING INDEX c_coll_ok_t (t=?)", ""},
		{"c_wr", "SEARCH c_wr USING PRIMARY KEY (pid=?)", ""},
		{"c_alias", "SEARCH c_alias USING INTEGER PRIMARY KEY (rowid=?)", ""},
		{"c_expr", "SCAN c_expr", "No index can serve lookups on pid"},
	}
	m := introspectFile(t, path)
	for _, c := range cases {
		if plan[c.table] != c.plan {
			t.Errorf("%s: planner %q, want %q", c.table, plan[c.table], c.plan)
		}
		f := findingsOf(t, m, c.table, "DBS-SQLITE-FK-NO-INDEX")
		switch {
		case c.message == "" && len(f) != 0:
			t.Errorf("%s: unexpected %+v", c.table, f)
		case c.message != "" && (len(f) != 1 || f[0].Level != "warn" || !strings.Contains(f[0].Message, c.message)):
			t.Errorf("%s: %+v, want %q", c.table, f, c.message)
		}
	}
	if f := findingsOf(t, m, "c_none", "DBS-SQLITE-FK-NO-INDEX"); len(f) == 1 && f[0].Column != "pid" {
		t.Errorf("column %+v", f[0])
	}
}

func TestFindingsFKOff(t *testing.T) {
	path, w := writeFile(t, findingsFixture)
	// The claim the finding makes: a new connection does not enforce them.
	var on int
	if err := w.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil || on != 0 {
		t.Fatalf("foreign_keys = %d, %v", on, err)
	}
	if _, err := w.Exec(`INSERT INTO c_none (pid) VALUES (999)`); err != nil {
		t.Fatalf("orphan refused with enforcement off: %v", err)
	}
	var kept int
	if _, err := w.Exec(`CREATE TEMP TABLE casc (pid INT REFERENCES p ON DELETE CASCADE);
		INSERT INTO p (id) VALUES (1); INSERT INTO casc VALUES (1); DELETE FROM p WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := w.QueryRow(`SELECT count(*) FROM casc`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("ON DELETE CASCADE ran with enforcement off: %d %v", kept, err)
	}
	m := introspectFile(t, path)
	var all []string
	for _, r := range m.Relations {
		for _, f := range r.Findings {
			if f.RuleID == "DBS-SQLITE-FK-OFF" {
				all = append(all, r.Name)
			}
		}
	}
	if len(all) != 1 || all[0] != "c_alias" {
		t.Fatalf("FK-OFF on %v, want once on c_alias", all)
	}
	f := findingsOf(t, m, "c_alias", "DBS-SQLITE-FK-OFF")[0]
	if f.Level != "info" || !strings.HasPrefix(f.Message, "14 tables declare foreign keys (shown once, on the first)") ||
		!strings.Contains(f.Message, "dbscope cannot see whether the application's connections turn it on") {
		t.Errorf("%+v", f)
	}

	// No foreign keys, no finding.
	path, _ = writeFile(t, `CREATE TABLE a (id INTEGER PRIMARY KEY); CREATE TABLE b (a_id INT)`)
	for _, r := range introspectFile(t, path).Relations {
		if len(r.Findings) != 0 {
			t.Errorf("%s: %+v", r.Name, r.Findings)
		}
	}
}

func TestFindingsNoStats(t *testing.T) {
	const ddl = `CREATE TABLE loaded (id INTEGER PRIMARY KEY, k INT UNIQUE);
		CREATE TABLE empty (id INTEGER PRIMARY KEY, k INT UNIQUE);
		CREATE TABLE plain (id INTEGER PRIMARY KEY, v TEXT);
		INSERT INTO loaded (k) VALUES (1), (2), (3); INSERT INTO plain (v) VALUES ('x');`

	// Never analyzed: indexed tables are flagged, a table with no index is not.
	path, _ := writeFile(t, ddl)
	m := introspectFile(t, path)
	for _, name := range []string{"loaded", "empty"} {
		if f := findingsOf(t, m, name, "DBS-SQLITE-NO-STATS"); len(f) != 1 || f[0].Level != "info" ||
			!strings.Contains(f[0].Message, "the database has no sqlite_stat1 table") {
			t.Errorf("%s: %+v", name, f)
		}
	}
	if f := findingsOf(t, m, "plain", "DBS-SQLITE-NO-STATS"); len(f) != 0 {
		t.Errorf("plain: %+v", f)
	}

	// PRAGMA optimize, as the message suggests, gathers them for the loaded table.
	path, _ = writeFile(t, ddl+`PRAGMA optimize;`)
	if f := findingsOf(t, introspectFile(t, path), "loaded", "DBS-SQLITE-NO-STATS"); len(f) != 0 {
		t.Errorf("after PRAGMA optimize: %+v", f)
	}

	// After ANALYZE: the loaded table has its row; the empty one gets none, nor does a table created later.
	path, w := writeFile(t, ddl+`ANALYZE; CREATE TABLE later (k INT UNIQUE);`)
	got := map[string]string{}
	rows, err := w.Query(`SELECT tbl, coalesce(idx, ''), stat FROM sqlite_stat1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tbl, idx, stat string
		rows.Scan(&tbl, &idx, &stat)
		got[tbl] = idx + " " + stat
	}
	rows.Close()
	if len(got) != 2 || got["loaded"] != "sqlite_autoindex_loaded_1 3 1" || got["plain"] != " 1" {
		t.Fatalf("sqlite_stat1 %v", got)
	}
	m = introspectFile(t, path)
	for name, want := range map[string]bool{"loaded": false, "plain": false, "empty": true, "later": true} {
		f := findingsOf(t, m, name, "DBS-SQLITE-NO-STATS")
		if !want && len(f) != 0 || want && (len(f) != 1 || !strings.Contains(f[0].Message, "sqlite_stat1 has no row for it")) {
			t.Errorf("%s: %+v", name, f)
		}
	}
}

func TestFindingsTypeAffinity(t *testing.T) {
	path, w := writeFile(t, findingsFixture)
	// The affinity each flagged name really gets, seen in what SQLite stored.
	var f, s, n, bin, vb, d, v, strictN string
	if err := w.QueryRow(`SELECT typeof(f), typeof(s), typeof(n), typeof(bin), typeof(vb), typeof(d), typeof(v),
		(SELECT typeof(n) FROM aff_strict) FROM aff`).Scan(&f, &s, &n, &bin, &vb, &d, &v, &strictN); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join([]string{f, s, n, bin, vb, d, v, strictN}, " "); got != "integer integer integer integer integer text text text" {
		t.Fatalf("stored types %s", got)
	}
	var half, seven, blob string
	if err := w.QueryRow(`SELECT f / 2, s FROM aff`).Scan(&half, &seven); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO aff (bin) VALUES (x'00')`); err != nil {
		t.Fatal(err)
	}
	if err := w.QueryRow(`SELECT typeof(bin) FROM aff WHERE rowid = 2`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if half != "1" || seven != "7" || blob != "blob" {
		t.Fatalf("f / 2 = %s, s = %s, BYTEA blob stored as %s", half, seven, blob)
	}

	m := introspectFile(t, path)
	want := map[string]string{
		"f":   `"FLOATING POINT", which SQLite gives INTEGER affinity, not REAL: the name contains "INT", and that rule is checked before "FLOA"`,
		"s":   `"STRING", which SQLite gives NUMERIC affinity, not TEXT`,
		"n":   `ANY, which keeps values as given only in STRICT tables; in this table it gets NUMERIC affinity`,
		"bin": `"BYTEA", which SQLite gives NUMERIC affinity, not BLOB`,
		"vb":  `"VARBINARY(16)", which SQLite gives NUMERIC affinity, not BLOB`,
	}
	got := findingsOf(t, m, "aff", "DBS-SQLITE-TYPE-AFFINITY")
	if len(got) != len(want) {
		t.Errorf("%d findings, want %d: %+v", len(got), len(want), got)
	}
	for _, g := range got {
		if g.Level != "warn" || want[g.Column] == "" || !strings.HasPrefix(g.Message, g.Column+" is declared "+want[g.Column]) {
			t.Errorf("%+v", g)
		}
	}
	if got := findingsOf(t, m, "aff_strict", "DBS-SQLITE-TYPE-AFFINITY"); len(got) != 0 {
		t.Errorf("strict: %+v", got)
	}
}

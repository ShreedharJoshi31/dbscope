package oracle

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

// TestAsOf runs on a server whose clock is UTC and on one whose clock is
// +05:30, one after the other, with a session time zone of +05:30 as well.
func TestAsOf(t *testing.T) {
	for _, tz := range []string{"", "Asia/Kolkata"} {
		t.Run("clock "+tz, func(t *testing.T) {
			var env []string
			if tz != "" {
				env = append(env, "TZ="+tz)
			}
			asOfServer(t, mytest.Oracle(t, env...), tz != "")
		})
	}
}

func asOfServer(t *testing.T, p conn.Profile, quick bool) {
	ctx := context.Background()
	sys, err := Open(p, mytest.OraclePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	const pass = "TtAsOf2026x"
	for _, q := range []string{
		"CREATE USER tt IDENTIFIED BY " + pass + " DEFAULT TABLESPACE sysaux QUOTA UNLIMITED ON sysaux",
		"GRANT CREATE SESSION, CREATE TABLE, CREATE VIEW TO tt",
		"CREATE TABLE tt.acct (id NUMBER PRIMARY KEY, bal NUMBER)",
		"CREATE TABLE tt.acct2 (id NUMBER PRIMARY KEY, tag VARCHAR2(20))",
		"CREATE TABLE tt.ddl_drop (id NUMBER PRIMARY KEY, v NUMBER, w NUMBER)",
		"CREATE VIEW tt.v_acct AS SELECT id, bal FROM tt.acct",
		"INSERT INTO tt.acct VALUES (1, 100)",
		"INSERT INTO tt.acct2 VALUES (1, 'red')",
		"INSERT INTO tt.ddl_drop VALUES (1, 1, 1)",
		"COMMIT",
		// r_read: READ only; r_fb: FLASHBACK on each object; r_any: FLASHBACK ANY TABLE;
		// r_cat: FLASHBACK ANY TABLE and SELECT_CATALOG_ROLE.
		"CREATE USER r_read IDENTIFIED BY " + pass,
		"CREATE USER r_fb IDENTIFIED BY " + pass,
		"CREATE USER r_any IDENTIFIED BY " + pass,
		"CREATE USER r_cat IDENTIFIED BY " + pass,
		"GRANT CREATE SESSION TO r_read, r_fb, r_any, r_cat",
		"GRANT READ ON tt.acct TO r_read, r_fb, r_any, r_cat",
		"GRANT READ ON tt.acct2 TO r_read, r_fb, r_any, r_cat",
		"GRANT READ ON tt.v_acct TO r_read, r_fb, r_any, r_cat",
		"GRANT READ ON tt.ddl_drop TO r_fb",
		"GRANT FLASHBACK ON tt.acct TO r_fb",
		"GRANT FLASHBACK ON tt.acct2 TO r_fb",
		"GRANT FLASHBACK ON tt.v_acct TO r_fb",
		"GRANT FLASHBACK ON tt.ddl_drop TO r_fb",
		"GRANT FLASHBACK ANY TABLE TO r_any, r_cat",
		"GRANT SELECT_CATALOG_ROLE TO r_cat",
	} {
		mustExec(t, sys, q)
	}
	open := func(user string) *sql.DB {
		u := p
		u.User = user
		db, err := Open(u, pass)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	rRead, rFB, rAny, rCat := open("r_read"), open("r_fb"), open("r_any"), open("r_cat")
	// One connection whose session time zone is +05:30, so Run uses that session.
	ist := open("r_fb")
	ist.SetMaxOpenConns(1)
	mustExec(t, ist, "ALTER SESSION SET TIME_ZONE = '+05:30'")

	now := func() time.Time {
		var s string
		if err := sys.QueryRowContext(ctx, `SELECT TO_CHAR(SYS_EXTRACT_UTC(SYSTIMESTAMP), 'YYYY-MM-DD"T"HH24:MI:SS.FF6"Z"') FROM dual`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	time.Sleep(6 * time.Second)
	atA := now() // bal 100, tag red
	time.Sleep(6 * time.Second)
	mustExec(t, sys, "UPDATE tt.acct SET bal = 200 WHERE id = 1")
	mustExec(t, sys, "UPDATE tt.acct2 SET tag = 'blue' WHERE id = 1")
	mustExec(t, sys, "COMMIT")
	time.Sleep(6 * time.Second)
	atB := now() // bal 200, tag blue
	time.Sleep(6 * time.Second)
	mustExec(t, sys, "UPDATE tt.acct SET bal = 300 WHERE id = 1")
	mustExec(t, sys, "COMMIT")

	// run rewrites, checks and runs a query as the user, returning its rows joined.
	run := func(t *testing.T, db *sql.DB, q string, at time.Time) (string, error) {
		t.Helper()
		rewritten, err := AsOf(q, at, Resolver(ctx, db))
		if err != nil {
			return "", err
		}
		if err := Check(rewritten); err != nil {
			t.Fatalf("Check refused %s: %v", rewritten, err)
		}
		res, err := Run(ctx, db, rewritten, 10, false)
		if err != nil {
			return "", err
		}
		var rows []string
		for _, r := range res.Rows {
			var cells []string
			for _, v := range r {
				cells = append(cells, strings.Trim(string(v), `"`))
			}
			rows = append(rows, strings.Join(cells, ","))
		}
		return strings.Join(rows, "|"), nil
	}
	expect := func(t *testing.T, db *sql.DB, q string, at time.Time, want string) {
		t.Helper()
		got, err := run(t, db, q, at)
		if err != nil || got != want {
			t.Errorf("%s as of %s: got %q, %v; want %q", q, at.Format(time.RFC3339Nano), got, err, want)
		}
	}

	t.Run("checkpoints", func(t *testing.T) {
		for _, db := range []*sql.DB{rFB, rAny, ist} {
			expect(t, db, "SELECT bal FROM tt.acct WHERE id = 1", atA, "100")
			expect(t, db, "SELECT bal FROM tt.acct WHERE id = 1", atB, "200")
			expect(t, db, "SELECT a.bal, b.tag FROM tt.acct a JOIN tt.acct2 b ON b.id = a.id", atA, "100,red")
			expect(t, db, `SELECT bal FROM "TT"."V_ACCT" WHERE id = 1`, atB, "200")
			expect(t, db, "WITH c AS (SELECT id, bal FROM tt.acct) SELECT c.bal, (SELECT tag FROM tt.acct2 x WHERE x.id = c.id) FROM c", atB, "200,blue")
		}
		var tz, clock, dbtz string
		ist.QueryRowContext(ctx, "SELECT SESSIONTIMEZONE, TO_CHAR(SYSTIMESTAMP, 'TZH:TZM'), DBTIMEZONE FROM dual").Scan(&tz, &clock, &dbtz)
		t.Logf("session time zone %s, server clock %s, database time zone %s", tz, clock, dbtz)
		if tz != "+05:30" || (quick && clock != "+05:30") {
			t.Errorf("session time zone %q, server clock %q", tz, clock)
		}
		// AS OF drops the zone of a TIMESTAMP WITH TIME ZONE: instant A as a bare
		// FROM_TZ in another zone's digits reads some other time.
		for _, zone := range []string{"UTC", "+05:30"} {
			loc := time.UTC
			if zone != "UTC" {
				loc = time.FixedZone("", 5*3600+1800)
			}
			var bal int
			err := rFB.QueryRowContext(ctx, "SELECT bal FROM tt.acct AS OF TIMESTAMP FROM_TZ(TIMESTAMP '"+
				atA.In(loc).Format("2006-01-02 15:04:05.000000")+"', '"+zone+"') WHERE id = 1").Scan(&bal)
			t.Logf("instant A as a bare FROM_TZ(..., '%s'): bal %d, err %v", zone, bal, err)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		_, err := run(t, rRead, "SELECT bal FROM tt.acct WHERE id = 1", atA)
		if err == nil || !strings.Contains(err.Error(), "tt.acct needs the FLASHBACK privilege") {
			t.Errorf("READ-only user: %v", err)
		}
		_, err = run(t, rFB, "SELECT * FROM tt.nope", atA)
		if err == nil || !strings.Contains(err.Error(), "tt.nope is not a table or view this user can see in TT") {
			t.Errorf("unknown table: %v", err)
		}
		// The rewrite run directly by a READ-only user gets ORA-41900, which names FLASHBACK on this path.
		_, err = Run(ctx, rRead, "SELECT bal FROM tt.acct "+AsOfClause(atA)+" WHERE id = 1", 10, false)
		if err == nil || !strings.Contains(err.Error(), "ORA-41900") || !strings.Contains(err.Error(), "FLASHBACK") {
			t.Errorf("READ-only user running the rewrite: %v", err)
		}
	})

	if quick {
		return
	}
	t.Run("before DDL", func(t *testing.T) {
		before := now()
		time.Sleep(4 * time.Second)
		mustExec(t, sys, "ALTER TABLE tt.ddl_drop DROP COLUMN w")
		time.Sleep(4 * time.Second)
		_, err := run(t, rFB, "SELECT * FROM tt.ddl_drop", before)
		if err == nil || !strings.Contains(err.Error(), "ORA-01466") {
			t.Errorf("as of before DROP COLUMN: %v", err)
		}
		b, err := Bounds(ctx, rFB, []schema.QualifiedName{{Schema: "TT", Name: "DDL_DROP"}})
		if err != nil {
			t.Fatal(err)
		}
		tb := b.Tables[0]
		if tb.Oldest == nil || !tb.Oldest.After(before) || !strings.Contains(tb.Note, "ORA-01466") {
			t.Errorf("bounds after DDL %+v (before %s)", tb, before)
		}
		for s := 1; s <= 6; s++ {
			_, err := run(t, rFB, "SELECT id, v FROM tt.ddl_drop", tb.Oldest.Add(time.Duration(s-5)*time.Second))
			t.Logf("LAST_DDL_TIME + %ds: %v", s, err)
		}
		expect(t, rFB, "SELECT id, v FROM tt.ddl_drop", *tb.Oldest, "1,1")
	})

	t.Run("bounds", func(t *testing.T) {
		names := []schema.QualifiedName{{Schema: "SYS", Name: "DUAL"}, {Schema: "TT", Name: "ACCT"}, {Schema: "TT", Name: "NOPE"}}
		var param, tuned int64
		if err := sys.QueryRowContext(ctx, "SELECT TO_NUMBER(value) FROM v$parameter WHERE name = 'undo_retention'").Scan(&param); err != nil {
			t.Fatal(err)
		}
		if err := sys.QueryRowContext(ctx, "SELECT max(tuned_undoretention) KEEP (DENSE_RANK LAST ORDER BY begin_time) FROM v$undostat").Scan(&tuned); err != nil {
			t.Fatal(err)
		}
		t.Logf("undo_retention %d, latest TUNED_UNDORETENTION %d", param, tuned)
		wantSource, wantRetention := timetravel.SourceUndoRetention, param
		if tuned > param {
			wantSource, wantRetention = timetravel.SourceTunedUndoRetention, tuned
		}
		for _, c := range []struct {
			user      string
			db        *sql.DB
			source    string
			retention int64
		}{{"r_any (no catalog role)", rAny, timetravel.SourceAssumedDefault, 900}, {"r_cat", rCat, wantSource, wantRetention}} {
			b, err := Bounds(ctx, c.db, names)
			if err != nil {
				t.Fatalf("%s: %v", c.user, err)
			}
			t.Logf("%s: %+v", c.user, b)
			dual, acct, nope := b.Tables[0], b.Tables[1], b.Tables[2]
			if want := b.Now.Add(-time.Duration(c.retention) * time.Second); dual.Source != c.source || !dual.Available ||
				dual.Oldest == nil || !dual.Oldest.Equal(want) || dual.Note != "" {
				t.Errorf("%s: SYS.DUAL %+v, want source %s oldest %s", c.user, dual, c.source, want)
			}
			if acct.Source != c.source || acct.Oldest == nil || acct.Oldest.Before(b.Now.Add(-10*time.Minute)) || !strings.Contains(acct.Note, "ORA-01466") {
				t.Errorf("%s: TT.ACCT %+v; a table created minutes ago is bounded by its DDL", c.user, acct)
			}
			if nope.Kind != nil || nope.Available || nope.Oldest != nil || nope.Source != timetravel.SourceNone {
				t.Errorf("%s: TT.NOPE %+v", c.user, nope)
			}
			assumed := strings.Contains(b.Note, "SELECT_CATALOG_ROLE")
			if !strings.Contains(b.Note, "target, not a promise") || assumed != (c.source == timetravel.SourceAssumedDefault) {
				t.Errorf("%s: note %q", c.user, b.Note)
			}
		}
		b, err := Bounds(ctx, rRead, []schema.QualifiedName{{Schema: "TT", Name: "ACCT"}})
		if err != nil {
			t.Fatal(err)
		}
		if tb := b.Tables[0]; tb.Available || tb.Oldest != nil || tb.Source != timetravel.SourceNone || !strings.Contains(tb.Note, "FLASHBACK") {
			t.Errorf("READ-only user bounds %+v", tb)
		}
	})
}

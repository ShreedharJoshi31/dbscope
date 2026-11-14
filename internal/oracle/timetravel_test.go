package oracle

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestTimeTravelDetection(t *testing.T) {
	p := mytest.Oracle(t)
	ctx := context.Background()
	sys, err := Open(p, mytest.OraclePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	const pass = "TtDetect2026x"
	for _, q := range []string{
		"CREATE USER tt IDENTIFIED BY " + pass + " DEFAULT TABLESPACE sysaux QUOTA UNLIMITED ON sysaux",
		"GRANT CREATE SESSION, CREATE TABLE, CREATE VIEW TO tt",
		"CREATE TABLE tt.acct (id NUMBER PRIMARY KEY, bal NUMBER)",
		"CREATE TABLE tt.acct2 (id NUMBER PRIMARY KEY, tag VARCHAR2(20))",
		"CREATE VIEW tt.v_acct AS SELECT id, bal FROM tt.acct",
		// r_read: READ only; r_fb: FLASHBACK on acct and on the view; r_any: FLASHBACK ANY TABLE;
		// r_role: FLASHBACK on acct through a role; r_nested: through a role granted to a role.
		"CREATE ROLE fb_role",
		"GRANT FLASHBACK ON tt.acct TO fb_role",
		"CREATE ROLE outer_role",
		"GRANT fb_role TO outer_role",
		"CREATE USER r_read IDENTIFIED BY " + pass,
		"CREATE USER r_fb IDENTIFIED BY " + pass,
		"CREATE USER r_any IDENTIFIED BY " + pass,
		"CREATE USER r_role IDENTIFIED BY " + pass,
		"CREATE USER r_nested IDENTIFIED BY " + pass,
		"GRANT CREATE SESSION TO r_read, r_fb, r_any, r_role, r_nested",
		"GRANT READ ON tt.acct TO r_read, r_fb, r_any, r_role, r_nested",
		"GRANT READ ON tt.acct2 TO r_read, r_fb, r_any, r_role, r_nested",
		"GRANT READ ON tt.v_acct TO r_read, r_fb, r_any, r_role, r_nested",
		"GRANT FLASHBACK ON tt.acct TO r_fb",
		"GRANT FLASHBACK ON tt.v_acct TO r_fb",
		"GRANT FLASHBACK ANY TABLE TO r_any",
		"GRANT fb_role TO r_role",
		"GRANT outer_role TO r_nested",
	} {
		mustExec(t, sys, q)
	}
	time.Sleep(10 * time.Second) // AS OF a time before a table's DDL fails with ORA-01466

	yes := &schema.TimeTravel{Kind: "flashback", Available: true}
	no := &schema.TimeTravel{Kind: "flashback", Available: false, Note: "Needs the FLASHBACK privilege on this table, or FLASHBACK ANY TABLE"}
	for _, c := range []struct {
		user string
		want map[string]*schema.TimeTravel
	}{
		{"tt", map[string]*schema.TimeTravel{"ACCT": yes, "ACCT2": yes, "V_ACCT": yes}},
		{"r_read", map[string]*schema.TimeTravel{"ACCT": no, "ACCT2": no, "V_ACCT": no}},
		{"r_fb", map[string]*schema.TimeTravel{"ACCT": yes, "ACCT2": no, "V_ACCT": yes}},
		{"r_any", map[string]*schema.TimeTravel{"ACCT": yes, "ACCT2": yes, "V_ACCT": yes}},
		{"r_role", map[string]*schema.TimeTravel{"ACCT": yes, "ACCT2": no, "V_ACCT": no}},
		{"r_nested", map[string]*schema.TimeTravel{"ACCT": yes, "ACCT2": no, "V_ACCT": no}},
	} {
		t.Run(c.user, func(t *testing.T) {
			u := p
			u.User = c.user
			db, err := Open(u, pass)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m, err := Introspect(ctx, db, []string{"TT"})
			if err != nil {
				t.Fatal(err)
			}
			if m.TimeTravel == nil || !m.TimeTravel.Supported {
				t.Errorf("model time travel %+v", m.TimeTravel)
			}
			got := map[string]*schema.TimeTravel{}
			for _, r := range m.Relations {
				got[r.Name] = r.TimeTravel
			}
			if !reflect.DeepEqual(got, c.want) {
				for n, tt := range got {
					t.Logf("got %s: %+v", n, tt)
				}
				t.Errorf("time travel flags differ")
			}
			// The flag must match what the server does with an AS OF read.
			for n, tt := range got {
				var count int
				err := db.QueryRowContext(ctx, "SELECT count(*) FROM tt."+n+" AS OF TIMESTAMP (SYSTIMESTAMP - INTERVAL '1' SECOND)").Scan(&count)
				if (err == nil) != tt.Available {
					t.Errorf("%s AS OF: available %v but err %v", n, tt.Available, err)
				}
			}
		})
	}
}

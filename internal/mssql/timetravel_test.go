package mssql

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestRetention(t *testing.T) {
	for _, c := range []struct {
		period sql.NullInt64
		unit   string
		want   string
	}{
		{sql.NullInt64{Int64: 3, Valid: true}, "MONTH", "3 months"},
		{sql.NullInt64{Int64: 1, Valid: true}, "DAY", "1 day"},
		{sql.NullInt64{Int64: -1, Valid: true}, "INFINITE", "infinite"},
		{sql.NullInt64{}, "", ""},
	} {
		if got := retention(c.period, c.unit); got != c.want {
			t.Errorf("retention(%v, %q) = %q, want %q", c.period, c.unit, got, c.want)
		}
	}
}

func TestTimeTravelDetection(t *testing.T) {
	for _, name := range []string{"2025", "2022"} {
		t.Run(name, func(t *testing.T) { timeTravelServer(t, name) })
	}
}

const ttPass = "Tt-Detect-Pass-2026!"

func timeTravelServer(t *testing.T, name string) {
	p := mytest.MSSQL(t, name)
	ctx := context.Background()
	admin, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	mustExec(t, admin, "CREATE DATABASE tt")
	p.Database = "tt"
	db, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE dbo.acct (id int PRIMARY KEY, bal int,
			valid_from datetime2 GENERATED ALWAYS AS ROW START NOT NULL,
			valid_to datetime2 GENERATED ALWAYS AS ROW END NOT NULL,
			PERIOD FOR SYSTEM_TIME (valid_from, valid_to))
			WITH (SYSTEM_VERSIONING = ON (HISTORY_TABLE = dbo.acct_history, HISTORY_RETENTION_PERIOD = 3 MONTHS))`,
		`CREATE TABLE dbo.acct0 (id int PRIMARY KEY, bal int,
			vf datetime2(0) GENERATED ALWAYS AS ROW START HIDDEN NOT NULL,
			vt datetime2(0) GENERATED ALWAYS AS ROW END HIDDEN NOT NULL,
			PERIOD FOR SYSTEM_TIME (vf, vt))
			WITH (SYSTEM_VERSIONING = ON)`,
		"CREATE TABLE dbo.plain (id int PRIMARY KEY, bal int)",
		"CREATE LOGIN tt_reader WITH PASSWORD = '" + ttPass + "', CHECK_POLICY = OFF",
		"CREATE LOGIN tt_curonly WITH PASSWORD = '" + ttPass + "', CHECK_POLICY = OFF",
		"CREATE LOGIN tt_viewdef WITH PASSWORD = '" + ttPass + "', CHECK_POLICY = OFF",
	} {
		mustExec(t, db, q)
	}
	for _, q := range []string{
		"CREATE USER tt_reader FOR LOGIN tt_reader",
		"ALTER ROLE db_datareader ADD MEMBER tt_reader",
		"CREATE USER tt_curonly FOR LOGIN tt_curonly",
		"GRANT SELECT ON dbo.acct TO tt_curonly",
		"CREATE USER tt_viewdef FOR LOGIN tt_viewdef",
		"GRANT SELECT ON dbo.acct TO tt_viewdef",
		"GRANT VIEW DEFINITION ON dbo.acct_history TO tt_viewdef",
	} {
		mustExec(t, db, q)
	}
	var history0 string
	if err := db.QueryRowContext(ctx, "SELECT OBJECT_NAME(history_table_id) FROM sys.tables WHERE name = 'acct0'").Scan(&history0); err != nil {
		t.Fatal(err)
	}

	type flags map[string]*schema.TimeTravel
	everything := flags{
		"acct":         {Kind: "temporal", Available: true, Retention: "3 months"},
		"acct_history": {Kind: "history", Available: true, HistoryOf: "dbo.acct"},
		"acct0":        {Kind: "temporal", Available: true, Retention: "infinite"},
		history0:       {Kind: "history", Available: true, HistoryOf: "dbo.acct0"},
		"plain":        nil,
	}
	for _, c := range []struct {
		user string
		want flags
	}{
		{"sa", everything},
		{"tt_reader", everything},
		{"tt_curonly", flags{"acct": {Kind: "temporal", Available: false, Retention: "3 months",
			Note: "Needs SELECT on the history table, whose name this user cannot see"}}},
		{"tt_viewdef", flags{
			"acct": {Kind: "temporal", Available: false, Retention: "3 months",
				Note: "Needs SELECT on the history table dbo.acct_history"},
			"acct_history": {Kind: "history", Available: true, HistoryOf: "dbo.acct"},
		}},
	} {
		t.Run(c.user, func(t *testing.T) {
			conn := db
			if c.user != "sa" {
				u := p
				u.User = c.user
				if conn, err = Open(u, ttPass); err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
			}
			m, err := Introspect(ctx, conn, []string{"dbo"})
			if err != nil {
				t.Fatal(err)
			}
			if m.TimeTravel == nil || !m.TimeTravel.Supported || m.TimeTravel.Note != "" {
				t.Errorf("model time travel %+v", m.TimeTravel)
			}
			got := flags{}
			for _, r := range m.Relations {
				got[r.Name] = r.TimeTravel
			}
			if !reflect.DeepEqual(got, c.want) {
				for n, tt := range got {
					t.Logf("got %s: %+v", n, tt)
				}
				t.Errorf("time travel flags differ from %v", c.want)
			}
			// The flag must match what the server does with an AS OF read (a history table never takes one).
			for n, tt := range got {
				if tt == nil || tt.Kind != "temporal" {
					continue
				}
				var count int
				err := conn.QueryRowContext(ctx, "SELECT count(*) FROM dbo."+n+" FOR SYSTEM_TIME AS OF '2000-01-01'").Scan(&count)
				if (err == nil) != tt.Available {
					t.Errorf("%s AS OF: available %v but err %v", n, tt.Available, err)
				}
			}
		})
	}
}

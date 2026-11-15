package mssql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

func TestAsOf(t *testing.T) {
	for _, name := range []string{"2025", "2022"} {
		t.Run(name, func(t *testing.T) { asOfServer(t, name) })
	}
}

func asOfServer(t *testing.T, name string) {
	p := mytest.MSSQL(t, name)
	ctx := context.Background()
	admin, err := Open(p, mytest.MSSQLPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	mustExec(t, admin, "CREATE DATABASE tt")
	mustExec(t, admin, "CREATE LOGIN tt_reader WITH PASSWORD = '"+ttPass+"', CHECK_POLICY = OFF")
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
		`CREATE TABLE dbo.acct2 (id int PRIMARY KEY, tag varchar(20),
			vf datetime2 GENERATED ALWAYS AS ROW START HIDDEN NOT NULL,
			vt datetime2 GENERATED ALWAYS AS ROW END HIDDEN NOT NULL,
			PERIOD FOR SYSTEM_TIME (vf, vt))
			WITH (SYSTEM_VERSIONING = ON (HISTORY_TABLE = dbo.acct2_history))`,
		"CREATE TABLE dbo.plain (id int PRIMARY KEY, bal int)",
		"CREATE VIEW dbo.v_acct AS SELECT id, bal FROM dbo.acct",
		"INSERT INTO dbo.acct (id, bal) VALUES (1, 100)",
		"INSERT INTO dbo.acct2 (id, tag) VALUES (1, 'red')",
		"INSERT INTO dbo.plain VALUES (1, 1)",
		"CREATE USER tt_reader FOR LOGIN tt_reader",
		"ALTER ROLE db_datareader ADD MEMBER tt_reader",
		"GRANT SHOWPLAN TO tt_reader",
	} {
		mustExec(t, db, q)
	}
	u := p
	u.User = "tt_reader"
	rd, err := Open(u, ttPass)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()

	now := func() time.Time {
		var at time.Time
		if err := db.QueryRowContext(ctx, "SELECT SYSUTCDATETIME()").Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at.UTC()
	}
	time.Sleep(1500 * time.Millisecond)
	atA := now() // bal 100, tag red
	time.Sleep(1500 * time.Millisecond)
	mustExec(t, db, "UPDATE dbo.acct SET bal = 200 WHERE id = 1")
	mustExec(t, db, "UPDATE dbo.acct2 SET tag = 'blue' WHERE id = 1")
	time.Sleep(1500 * time.Millisecond)
	atB := now() // bal 200, tag blue
	time.Sleep(1500 * time.Millisecond)
	mustExec(t, db, "UPDATE dbo.acct SET bal = 300 WHERE id = 1")

	run := func(q string, at time.Time) (string, error) {
		rewritten, err := AsOf(q, at, Resolver(ctx, rd))
		if err != nil {
			return "", err
		}
		if err := Check(rewritten); err != nil {
			t.Fatalf("Check refused %s: %v", rewritten, err)
		}
		res, err := Run(ctx, rd, rewritten, 10, false, 10*time.Second)
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
	for _, c := range []struct {
		q    string
		at   time.Time
		want string
	}{
		{"SELECT bal FROM dbo.acct WHERE id = 1", atA, "100"},
		{"SELECT bal FROM acct WHERE id = 1", atB, "200"},
		{"SELECT a.bal, b.tag FROM dbo.acct AS a JOIN [dbo].[acct2] b ON b.id = a.id", atA, "100,red"},
		{"SELECT a.bal, b.tag FROM dbo.acct a, acct2 b WHERE b.id = a.id", atB, "200,blue"},
		{"SELECT bal FROM (SELECT TOP 1 bal FROM dbo.acct WITH (NOLOCK)) x", atA, "100"},
		{"SELECT bal FROM dbo.acct WHERE id = 1", now(), "300"},
	} {
		if got, err := run(c.q, c.at); err != nil || got != c.want {
			t.Errorf("%s as of %s: got %q, %v; want %q", c.q, c.at.Format(time.RFC3339Nano), got, err, c.want)
		}
	}
	for q, want := range map[string]string{
		"SELECT * FROM dbo.acct JOIN dbo.plain p ON p.id = acct.id": "dbo.plain is not a system-versioned temporal table",
		"SELECT * FROM dbo.v_acct":                                  "dbo.v_acct is a view",
		"SELECT * FROM dbo.acct_history":                            "is the history table of dbo.acct",
		"SELECT * FROM dbo.nope":                                    "dbo.nope is not a table or view this user can see",
	} {
		if _, err := run(q, atA); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", q, err, want)
		}
	}

	b, err := Bounds(ctx, rd, []schema.QualifiedName{{Schema: "dbo", Name: "acct"}, {Schema: "dbo", Name: "acct2"}, {Schema: "dbo", Name: "plain"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bounds %+v", b)
	acct, acct2, plain := b.Tables[0], b.Tables[1], b.Tables[2]
	want := b.Now.AddDate(0, -3, 0)
	if acct.Source != timetravel.SourceHistoryRetention || !acct.Available || acct.Oldest == nil ||
		acct.Oldest.Sub(want).Abs() > 3*24*time.Hour || acct.Kind == nil || *acct.Kind != "temporal" {
		t.Errorf("acct bounds %+v, want about %s", acct, want)
	}
	if acct2.Source != timetravel.SourceNone || !acct2.Available || acct2.Oldest != nil || acct2.Note != "History is kept until removed" {
		t.Errorf("acct2 bounds %+v", acct2)
	}
	if plain.Kind != nil || plain.Available || plain.Oldest != nil || !strings.Contains(plain.Note, "not a system-versioned") {
		t.Errorf("plain bounds %+v", plain)
	}
	// The server's DATEADD, not Go's AddDate, decides a month back.
	var exact time.Time
	db.QueryRowContext(ctx, "SELECT DATEADD(MONTH, -3, @p1)", b.Now).Scan(&exact)
	if d := acct.Oldest.Sub(exact.UTC()).Abs(); d > time.Second {
		t.Errorf("acct oldest %s, DATEADD gives %s", acct.Oldest, exact)
	}
}

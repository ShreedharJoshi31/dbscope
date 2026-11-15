package mysql

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

// Runs the flavors one after the other, so only one container is up at a time.
func TestAsOf(t *testing.T) {
	t.Run("mariadb", asOfMariaDB)
	t.Run("mysql", func(t *testing.T) {
		p := mytest.Server(t, "mysql")
		db, err := Open(p, mytest.Password, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ctx := context.Background()
		if _, err := db.ExecContext(ctx, "CREATE TABLE acct (id INT PRIMARY KEY, bal INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := AsOfOn(ctx, db, "SELECT * FROM acct FOR SYSTEM_TIME ALL", time.Now()); err == nil || err.Error() != NoTimeTravel {
			t.Errorf("AsOf on MySQL: %v", err)
		}
		if _, err := Bounds(ctx, db, []schema.QualifiedName{{Schema: "app", Name: "acct"}}); err == nil || err.Error() != NoTimeTravel {
			t.Errorf("Bounds on MySQL: %v", err)
		}
	})
}

func asOfMariaDB(t *testing.T) {
	p := mytest.Server(t, "mariadb")
	ctx := context.Background()
	db, err := Open(p, mytest.Password, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE TABLE acct (id INT PRIMARY KEY, bal INT) WITH SYSTEM VERSIONING",
		"CREATE TABLE acct2 (id INT PRIMARY KEY, tag VARCHAR(20)) WITH SYSTEM VERSIONING",
		"CREATE TABLE plain (id INT PRIMARY KEY, bal INT)",
		"CREATE VIEW v_acct AS SELECT id, bal FROM acct",
		"INSERT INTO acct VALUES (1, 100)",
		"INSERT INTO acct2 VALUES (1, 'red')",
		"INSERT INTO plain VALUES (1, 1)",
		"CREATE USER 'reader'@'%' IDENTIFIED BY 'reader-test'",
		"GRANT SELECT ON app.* TO 'reader'@'%'",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	reader := p
	reader.User = "reader"
	rd, err := Open(reader, "reader-test", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	// One connection whose session time zone is +05:30, so Run uses that session.
	ist, err := Open(reader, "reader-test", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer ist.Close()
	ist.SetMaxOpenConns(1)
	if _, err := ist.ExecContext(ctx, "SET time_zone = '+05:30'"); err != nil {
		t.Fatal(err)
	}

	epoch := func(q string) time.Time {
		var s string
		if err := db.QueryRowContext(ctx, q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		sec, frac, _ := strings.Cut(s, ".")
		n, _ := strconv.ParseInt(sec, 10, 64)
		us, _ := strconv.ParseInt((frac + "000000")[:6], 10, 64)
		return time.Unix(n, us*1000).UTC()
	}
	now := func() time.Time { return epoch("SELECT UNIX_TIMESTAMP(NOW(6))") }
	exec := func(q string) {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	atA := now() // bal 100, tag red
	time.Sleep(1500 * time.Millisecond)
	exec("UPDATE acct SET bal = 200 WHERE id = 1")
	exec("UPDATE acct2 SET tag = 'blue' WHERE id = 1")
	changed := epoch("SELECT UNIX_TIMESTAMP(ROW_START) FROM acct WHERE id = 1")
	time.Sleep(1500 * time.Millisecond)
	atB := now() // bal 200, tag blue
	time.Sleep(1500 * time.Millisecond)
	exec("UPDATE acct SET bal = 300 WHERE id = 1")

	run := func(db *sql.DB, q string, at time.Time) (string, error) {
		rewritten, err := AsOfOn(ctx, db, q, at)
		if err != nil {
			return "", err
		}
		if err := Check(rewritten); err != nil {
			t.Fatalf("Check refused %s: %v", rewritten, err)
		}
		res, err := Run(ctx, db, rewritten, 10, false, 10*time.Second)
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
	for name, conn := range map[string]*sql.DB{"session UTC": rd, "session +05:30": ist} {
		for _, c := range []struct {
			q    string
			at   time.Time
			want string
		}{
			{"SELECT bal FROM acct WHERE id = 1", atA, "100"},
			{"SELECT bal FROM app.acct WHERE id = 1", atB, "200"},
			// Six fraction digits: one microsecond before the update's row start, then at it.
			{"SELECT bal FROM acct WHERE id = 1", changed.Add(-time.Microsecond), "100"},
			{"SELECT bal FROM acct WHERE id = 1", changed, "200"},
			{"SELECT a.bal, b.tag FROM acct AS a JOIN `acct2` b ON b.id = a.id", atA, "100,red"},
			{"SELECT a.bal, b.tag FROM acct a, app.acct2 b WHERE b.id = a.id", atB, "200,blue"},
			{"SELECT bal FROM acct WHERE id = 1", now(), "300"},
		} {
			if got, err := run(conn, c.q, c.at); err != nil || got != c.want {
				t.Errorf("%s: %s as of %s: got %q, %v; want %q", name, c.q, c.at.Format(time.RFC3339Nano), got, err, c.want)
			}
		}
	}
	var tz string
	ist.QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&tz)
	if tz != "+05:30" {
		t.Errorf("session time zone %q", tz)
	}
	for q, want := range map[string]string{
		"SELECT * FROM acct JOIN plain p ON p.id = acct.id": "plain is not a system-versioned table",
		"SELECT * FROM v_acct":                              "v_acct is a view",
		"SELECT * FROM nope":                                "nope is not a table or view this user can see",
	} {
		if _, err := run(rd, q, atA); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", q, err, want)
		}
	}

	// A sql_mode that changes quoting is refused: 'x\' would end the string on the server only.
	if _, err := db.ExecContext(ctx, "SET GLOBAL sql_mode = CONCAT(@@GLOBAL.sql_mode, ',NO_BACKSLASH_ESCAPES')"); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(reader, "reader-test", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := AsOfOn(ctx, fresh, `SELECT 'x\' AS c, b.* FROM acct, plain b -- '`, atA); err == nil || !strings.Contains(err.Error(), "NO_BACKSLASH_ESCAPES") {
		t.Errorf("NO_BACKSLASH_ESCAPES: %v", err)
	}
	if _, err := db.ExecContext(ctx, "SET GLOBAL sql_mode = DEFAULT"); err != nil {
		t.Fatal(err)
	}

	b, err := Bounds(ctx, rd, []schema.QualifiedName{{Schema: "app", Name: "acct"}, {Schema: "app", Name: "plain"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bounds %+v", b)
	acct, plain := b.Tables[0], b.Tables[1]
	if acct.Source != timetravel.SourceNone || !acct.Available || acct.Oldest != nil || acct.Kind == nil || *acct.Kind != "system-versioned" {
		t.Errorf("acct bounds %+v", acct)
	}
	if plain.Kind != nil || plain.Available || plain.Oldest != nil {
		t.Errorf("plain bounds %+v", plain)
	}
	if !strings.Contains(b.Note, "DELETE HISTORY") || b.Now.Sub(time.Now()).Abs() > time.Minute {
		t.Errorf("bounds note %q, now %s", b.Note, b.Now)
	}
}

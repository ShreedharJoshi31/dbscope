package migrate

import (
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// Column-level view dependencies come only from the live catalog, so a DROP
// COLUMN that PostgreSQL will refuse can only be recognised with a live base.
func TestLiveDropColumnUnderViewWillFail(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	d := srv.NewDB(t)
	d.Exec(t, `CREATE TABLE films (id int PRIMARY KEY, title text, description text);
		INSERT INTO films VALUES (1, 'a', 'b');
		CREATE VIEW film_list AS SELECT id, description FROM films`)
	base, err := LiveBase(t.Context(), d.Admin, []string{"public"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sql      string
		willFail bool
	}{
		{"SET lock_timeout = '2s'; ALTER TABLE films DROP COLUMN description", true},
		{"SET lock_timeout = '2s'; ALTER TABLE films DROP COLUMN description CASCADE", false},
		{"SET lock_timeout = '2s'; ALTER TABLE films DROP COLUMN title", false},
	} {
		rep, err := Analyze(t.Context(), base, Request{Files: []ingest.Input{{Name: "m.sql", SQL: c.sql}}})
		if err != nil {
			t.Fatal(err)
		}
		s := rep.Statements[len(rep.Statements)-1]
		var msgs []string
		for _, f := range s.Findings {
			msgs = append(msgs, f.Message)
		}
		joined := strings.Join(msgs, "\n")
		if got := strings.Contains(joined, "will fail without CASCADE because view public.film_list"); got != c.willFail {
			t.Errorf("%s: will-fail %v, want %v:\n%s", c.sql, got, c.willFail, joined)
		}
	}
}

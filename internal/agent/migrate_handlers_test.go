package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
)

func analyzeBodyJSON(t *testing.T, files map[string]string, extra map[string]any) string {
	t.Helper()
	var list []map[string]string
	for name, sql := range files {
		list = append(list, map[string]string{"name": name, "sql": sql})
	}
	body := map[string]any{"files": list}
	for k, v := range extra {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSampleMigrationAnalyze(t *testing.T) {
	e, _ := fakeEnv(t)
	body := analyzeBodyJSON(t, map[string]string{
		"V1__orders.sql": "ALTER TABLE shop.orders ADD COLUMN uid uuid DEFAULT gen_random_uuid();\nCREATE INDEX CONCURRENTLY o_idx ON shop.orders (placed_at);",
	}, nil)
	w := e.do("POST", "/api/sample/migrations/analyze", body)
	expect(t, w, 200, "")
	rep := decodeBody[migrate.Report](t, w)
	if rep.PGVersion != 170000 || rep.Base != "sample" || len(rep.Statements) != 2 {
		t.Fatalf("report %+v", rep)
	}
	// Flyway runs a file holding CREATE INDEX CONCURRENTLY outside a transaction.
	if s := rep.Statements[0]; s.Work != "rewrite" || s.Score != 10 || s.Rating != "high" || s.Tx != "none" {
		t.Fatalf("first statement %+v", s)
	}
	if rep.Statements[1].Kind != "CREATE INDEX CONCURRENTLY" || len(rep.Impact["shop.orders"]) == 0 {
		t.Fatalf("second %+v impact %v", rep.Statements[1], rep.Impact)
	}
	for _, key := range []string{`"diff"`, `"impact"`, `"unanalyzable"`, `"lockTimeout"`, `"locks":[{"relation":"shop.orders","mode":"ACCESS EXCLUSIVE"}]`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("response lacks %s", key)
		}
	}

	// txMode file wraps it: CONCURRENTLY in a transaction is Critical.
	body = analyzeBodyJSON(t, map[string]string{"a.sql": "CREATE INDEX CONCURRENTLY o_idx ON shop.orders (placed_at);"},
		map[string]any{"txMode": "file", "assumeLockTimeout": true})
	rep = decodeBody[migrate.Report](t, e.do("POST", "/api/sample/migrations/analyze", body))
	if s := rep.Statements[0]; s.Rating != "critical" || s.Findings[0].RuleID != "DBS-TX-NOT-ALLOWED" || !s.LockTimeout {
		t.Fatalf("%+v", s)
	}
}

func TestMigrationAnalyzeValidation(t *testing.T) {
	e, _ := fakeEnv(t)
	big := strings.Repeat("x", maxMigrationFileBytes+1)
	many := map[string]string{}
	for i := range maxMigrationFiles + 1 {
		many["f"+strings.Repeat("a", i%5)+string(rune('a'+i%26))+strings.Repeat("b", i/26)+".sql"] = "SELECT 1"
	}
	for name, body := range map[string]string{
		"no files":     `{"files":[]}`,
		"missing":      `{}`,
		"too many":     analyzeBodyJSON(t, many, nil),
		"file too big": analyzeBodyJSON(t, map[string]string{"a.sql": big}, nil),
		"empty name":   `{"files":[{"name":" ","sql":"SELECT 1"}]}`,
		"bad txMode":   `{"files":[{"name":"a.sql","sql":"SELECT 1"}],"txMode":"sometimes"}`,
		"unknown key":  `{"files":[{"name":"a.sql","sql":"SELECT 1"}],"dryRun":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, e.do("POST", "/api/sample/migrations/analyze", body), 400, "validation")
		})
	}
	expect(t, e.do("POST", "/api/sample/migrations/analyze", `{"files":[{"name":"a.sql","sql":"`+strings.Repeat("y", maxMigrationBody)+`"}]}`), 413, "validation")
}

func TestProfileMigrationAnalyze(t *testing.T) {
	e, f := fakeEnv(t)
	p := e.createProfile(validBody)
	path := "/api/profiles/" + p.ID + "/migrations/analyze"
	body := analyzeBodyJSON(t, map[string]string{"1_init.up.sql": "ALTER TABLE public.posts DROP COLUMN user_id;"}, nil)
	w := e.do("POST", path, body)
	expect(t, w, 200, "")
	rep := decodeBody[migrate.Report](t, w)
	if rep.PGVersion != 170002 || rep.Statements[0].Findings[0].RuleID != "DBS-DROP-COLUMN" || rep.Statements[0].Rating != "critical" {
		t.Fatalf("%+v", rep)
	}
	if len(rep.Diff.Columns) != 1 || rep.Diff.Columns[0].Change != "dropped" {
		t.Fatalf("diff %+v", rep.Diff)
	}
	if len(f.passwords) != 1 || f.passwords[0] != "" {
		t.Fatalf("source calls %q", f.passwords)
	}

	expect(t, e.do("POST", "/api/profiles/nope/migrations/analyze", body), 404, "not_found")

	f.err = &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	expect(t, e.do("POST", path, body), 504, "timeout")
	f.err = errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	w = e.do("POST", path, body)
	expect(t, w, 502, "upstream")
}

func TestProfileMigrationAnalyzeLockedVault(t *testing.T) {
	e := lockedEnv(t, "pass")
	e.useSource(&fakeSource{})
	in, _ := conn.ProfileInput{Name: "secret", Host: "h", Database: "d", User: "u", Env: conn.EnvLocal}.Profile()
	in.HasPassword = true
	p, _ := e.store.CreateProfile(t.Context(), in)
	body := analyzeBodyJSON(t, map[string]string{"a.sql": "SELECT 1"}, nil)
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/migrations/analyze", body), 423, "secrets_locked")
	// The sample needs no secrets.
	expect(t, e.do("POST", "/api/sample/migrations/analyze", body), 200, "")
}

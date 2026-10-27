package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/store"
)

func TestShareExportImport(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "AppData"} {
		t.Setenv(k, home)
	}
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "dbscope.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := conn.ProfileInput{Name: "Orders", Host: "db.example", Database: "app", User: "reader", Env: conn.EnvStaging}
	pr, _ := in.Profile()
	p, err := st.CreateProfile(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	st.PutGlossary(ctx, p.ID, []store.GlossaryEntry{{Term: "mrr", Definition: "monthly"}})
	st.Close()

	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := shareCmd(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	file := filepath.Join(t.TempDir(), "orders.dbscope.json")
	if code, _, stderr := run("export", "--connection", "orders", "--out", file); code != exitOK {
		t.Fatalf("export: %d %s", code, stderr)
	}
	src, _ := os.ReadFile(file)
	if !strings.Contains(string(src), `"mrr": {`) {
		t.Fatalf("exported:\n%s", src)
	}

	os.WriteFile(file, bytes.Replace(src, []byte(`"monthly"`), []byte(`"monthly, net of refunds"`), 1), 0o644)
	code, stdout, stderr := run("import", "--connection", "Orders", "--dry-run", file)
	if code != exitOK || !strings.Contains(stdout, "Would import") || !strings.Contains(stdout, "glossary terms         1 changed") {
		t.Fatalf("dry run: %d\n%s%s", code, stdout, stderr)
	}
	if _, out, _ := run("export", "--connection", "Orders"); !strings.Contains(out, `"monthly"`) {
		t.Fatal("a dry run changed the glossary")
	}
	if code, stdout, _ := run("import", "--connection", "Orders", file); code != exitOK || !strings.Contains(stdout, "Imported") {
		t.Fatalf("import: %d %s", code, stdout)
	}
	if _, out, _ := run("export", "--connection", "Orders"); !strings.Contains(out, "net of refunds") {
		t.Fatalf("after import:\n%s", out)
	}

	if code, _, stderr := run("export", "--connection", "nope"); code != exitUnavailable || !strings.Contains(stderr, "saved: Orders") {
		t.Fatalf("unknown connection: %d %s", code, stderr)
	}
	os.WriteFile(file, []byte(`{"kind":"dbscope-shared-state","formatVersion":9}`), 0o644)
	if code, _, stderr := run("import", "--connection", "Orders", file); code != exitUsage || !strings.Contains(stderr, "newer dbscope") {
		t.Fatalf("newer format: %d %s", code, stderr)
	}
	if code, _, _ := run("import", "--connection", "Orders"); code != exitUsage {
		t.Fatalf("missing file argument: %d", code)
	}
}

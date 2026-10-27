package ingest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func names(set *Set) []string {
	var out []string
	for _, f := range set.Files {
		out = append(out, f.Name)
	}
	return out
}

func txs(f File) []Tx {
	var out []Tx
	for _, s := range f.Statements {
		out = append(out, s.Tx)
	}
	return out
}

func loadDir(t *testing.T, dir string, opts Options) *Set {
	t.Helper()
	inputs, err := ReadPaths([]string{filepath.Join("testdata", dir)}, mustAbs(t, "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	return Load(inputs, opts)
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFlyway(t *testing.T) {
	set := loadDir(t, "flyway", Options{})
	if set.Framework != Flyway {
		t.Fatalf("framework %s", set.Framework)
	}
	want := []string{"flyway/V1__init.sql", "flyway/V1.9__earlier.sql", "flyway/V1.10__later.sql", "flyway/V2__concurrent.sql",
		"flyway/V3__no_tx.sql", "flyway/R__views.sql"}
	if got := names(set); !slices.Equal(got, want) {
		t.Fatalf("order %v", got)
	}
	byName := map[string]File{}
	for _, f := range set.Files {
		byName[f.Name] = f
	}
	if f := byName["flyway/V1__init.sql"]; !f.Wrapped || !slices.Equal(txs(f), []Tx{TxFramework, TxFramework}) {
		t.Errorf("V1 %v %v", f.Wrapped, txs(f))
	}
	// Flyway runs a script with CONCURRENTLY outside a transaction.
	if f := byName["flyway/V2__concurrent.sql"]; f.Wrapped || txs(f)[0] != TxNone {
		t.Errorf("V2 %v %v", f.Wrapped, txs(f))
	}
	// executeInTransaction=false in the .sql.conf sidecar.
	if f := byName["flyway/V3__no_tx.sql"]; f.Wrapped || !slices.Equal(txs(f), []Tx{TxNone, TxNone}) {
		t.Errorf("V3 %v %v", f.Wrapped, txs(f))
	}
}

func TestGolangMigrate(t *testing.T) {
	set := loadDir(t, "golang-migrate", Options{})
	if set.Framework != GolangMigrate {
		t.Fatalf("framework %s", set.Framework)
	}
	want := []string{"golang-migrate/000001_init.up.sql", "golang-migrate/000002_index.up.sql", "golang-migrate/000010_explicit.up.sql"}
	if got := names(set); !slices.Equal(got, want) {
		t.Fatalf("order %v (.down.sql must be skipped)", got)
	}
	// Two statements in one Exec: one implicit transaction.
	if got := txs(set.Files[0]); !slices.Equal(got, []Tx{TxImplicit, TxImplicit}) {
		t.Errorf("init %v", got)
	}
	// A single statement runs bare.
	if got := txs(set.Files[1]); !slices.Equal(got, []Tx{TxNone}) {
		t.Errorf("index %v", got)
	}
	// Explicit BEGIN/COMMIT inside an implicit-transaction file.
	if got := txs(set.Files[2]); !slices.Equal(got, []Tx{TxExplicit, TxExplicit, TxExplicit, TxImplicit}) {
		t.Errorf("explicit %v", got)
	}
}

func TestPrisma(t *testing.T) {
	set := loadDir(t, "prisma", Options{})
	if set.Framework != Prisma {
		t.Fatalf("framework %s", set.Framework)
	}
	want := []string{"prisma/migrations/20240101000000_init/migration.sql", "prisma/migrations/20240102000000_users/migration.sql",
		"prisma/migrations/20240215093000_add_index/migration.sql"}
	if got := names(set); !slices.Equal(got, want) {
		t.Fatalf("order %v", got)
	}
	init := set.Files[0]
	if got := txs(init); !slices.Equal(got, []Tx{TxImplicit, TxImplicit}) {
		t.Errorf("init %v", got)
	}
	// CRLF normalised: the index statement is on line 5, column 1.
	if s := init.Statements[1]; s.Line != 5 || s.Column != 1 || strings.Contains(s.SQL, "\r") {
		t.Errorf("CRLF positions %d:%d %q", s.Line, s.Column, s.SQL)
	}
	// BOM stripped: column 1, not 2.
	if s := set.Files[1].Statements[0]; s.Column != 1 || s.Err != nil {
		t.Errorf("BOM %d %v", s.Column, s.Err)
	}
	if got := txs(set.Files[2]); !slices.Equal(got, []Tx{TxNone}) {
		t.Errorf("single statement %v", got)
	}
}

func TestAtlas(t *testing.T) {
	set := loadDir(t, "atlas", Options{})
	if set.Framework != Atlas {
		t.Fatalf("framework %s", set.Framework)
	}
	if got := names(set); !slices.Equal(got, []string{"atlas/20240101_init.sql", "atlas/20240102_index.sql"}) {
		t.Fatalf("order %v", got)
	}
	if got := txs(set.Files[0]); !slices.Equal(got, []Tx{TxFramework, TxFramework}) {
		t.Errorf("init %v", got)
	}
	if f := set.Files[1]; f.Wrapped || txs(f)[0] != TxNone {
		t.Errorf("-- atlas:txmode none: %v %v", f.Wrapped, txs(f))
	}
}

func TestPlainNaturalOrderAndExplicitTx(t *testing.T) {
	set := loadDir(t, "plain", Options{})
	if set.Framework != Plain {
		t.Fatalf("framework %s", set.Framework)
	}
	want := []string{"plain/1_first.sql", "plain/2_second.sql", "plain/10_tenth.sql", "plain/20_tx.sql"}
	if got := names(set); !slices.Equal(got, want) {
		t.Fatalf("order %v", got)
	}
	// START TRANSACTION ... END; then VACUUM outside.
	if got := txs(set.Files[3]); !slices.Equal(got, []Tx{TxExplicit, TxExplicit, TxExplicit, TxExplicit, TxExplicit, TxNone}) {
		t.Errorf("explicit %v", got)
	}
}

func TestModes(t *testing.T) {
	in := []Input{{Name: "b.sql", SQL: "SELECT 1; SELECT 2"}, {Name: "a.sql", SQL: "BEGIN; SELECT 3; COMMIT; SELECT 4"}}
	cases := map[Mode][2][]Tx{
		ModeFile:      {{TxFramework, TxFramework}, {TxExplicit, TxExplicit, TxExplicit, TxFramework}},
		ModeStatement: {{TxNone, TxNone}, {TxExplicit, TxExplicit, TxExplicit, TxNone}},
		ModeNone:      {{TxNone, TxNone}, {TxExplicit, TxExplicit, TxExplicit, TxNone}},
		ModeAuto:      {{TxNone, TxNone}, {TxExplicit, TxExplicit, TxExplicit, TxNone}},
	}
	for mode, want := range cases {
		set := Load(in, Options{Mode: mode, KeepOrder: true})
		if names(set)[0] != "b.sql" {
			t.Fatalf("%s: KeepOrder ignored: %v", mode, names(set))
		}
		for i := range 2 {
			if got := txs(set.Files[i]); !slices.Equal(got, want[i]) {
				t.Errorf("%s file %d: %v, want %v", mode, i, got, want[i])
			}
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Error("bad mode accepted")
	}
}

func TestCaseInsensitiveDuplicates(t *testing.T) {
	set := Load([]Input{{Name: "db/V1__Init.sql", SQL: "SELECT 1"}, {Name: "db/v1__init.SQL", SQL: "SELECT 1"}}, Options{})
	if len(set.Warnings) != 1 || !strings.Contains(set.Warnings[0], "differ only in letter case") {
		t.Fatalf("warnings %v", set.Warnings)
	}
}

func TestNatural(t *testing.T) {
	in := []string{"10_a", "2_a", "1_b", "1_a", "001_c", "a10", "a9", "b"}
	slices.SortFunc(in, Natural)
	if want := []string{"1_a", "1_b", "001_c", "2_a", "10_a", "a9", "a10", "b"}; !slices.Equal(in, want) {
		t.Fatalf("%v", in)
	}
}

func TestReadPathsSizeLimitAndRoot(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.sql")
	if err := os.WriteFile(big, make([]byte, MaxFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPaths([]string{big}, dir); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("err %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b")
	os.MkdirAll(sub, 0o755)
	if got := RepoRoot(sub); got != mustAbs(t, dir) {
		if r, _ := filepath.EvalSymlinks(got); r != func() string { r2, _ := filepath.EvalSymlinks(dir); return r2 }() {
			t.Fatalf("RepoRoot %s", got)
		}
	}
}

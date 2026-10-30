package usage_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/usage"
	"github.com/dbscope/dbscope/internal/sample"
	"github.com/dbscope/dbscope/internal/sqlite"
)

// PostgreSQL renames: a renamed table is dropped and added in the diff, a renamed column keeps its old name.
func TestPostgresRenames(t *testing.T) {
	m, err := sample.Model()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := migrate.Analyze(t.Context(), migrate.SampleBase(m), migrate.Request{Files: []ingest.Input{{Name: "1.sql",
		SQL: "ALTER TABLE shop.orders RENAME COLUMN placed_at TO ordered_at;\nALTER TABLE shop.payments RENAME TO charges;"}}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write(t, root, "app/models/order.rb", "class Order < ApplicationRecord\n  scope :recent, -> { where('placed_at > ?', 1.day.ago) }\nend\n")
	write(t, root, "lib/sql.go", "const q = `SELECT 1 FROM shop.payments`\n")
	usage.Apply(t.Context(), rep, root, nil)
	want := []string{
		"shop.payments lib/sql.go:1 const q = `SELECT 1 FROM shop.payments`",
		"shop.orders.placed_at app/models/order.rb:2 scope :recent, -> { where('placed_at > ?', 1.day.ago) }",
	}
	if got := places(rep.Usages); !slices.Equal(got, want) {
		t.Fatalf("usages %q from diff %+v", got, rep.Diff)
	}
	for i, name := range []string{"shop.orders.placed_at", "shop.payments"} {
		f := rep.Statements[i].Findings[len(rep.Statements[i].Findings)-1]
		if f.RuleID != usage.RuleID || !strings.HasPrefix(f.Message, name+" is still named in 1 place") {
			t.Fatalf("statement %d: %+v", i, rep.Statements[i].Findings)
		}
	}
}

const baseSchema = `CREATE TABLE customers (id INTEGER PRIMARY KEY, email TEXT, name TEXT);
CREATE TABLE invoices (id INTEGER PRIMARY KEY, email TEXT);
CREATE TABLE legacy_logs (id INTEGER PRIMARY KEY, msg TEXT);`

const migration = "ALTER TABLE customers DROP COLUMN email;\nDROP TABLE legacy_logs;\n"

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// project is an application tree with uses that must and must not be reported.
func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "app/models/customer.rb", "class Customer < ApplicationRecord\n  validates :email, presence: true\n  def contact = email_address\nend\n")
	write(t, root, "web/src/api.ts", "export async function load() {\n  await db.query('SELECT \"email\" FROM customers');\n  return prisma.customer.findMany({ select: { email: true } });\n}\n")
	write(t, root, "svc/report.py", "from app.models import Invoice\n\ndef send(invoice):\n    mail(invoice.email)\n")
	write(t, root, "cmd/main.go", "package main\n\nconst q = `SELECT id FROM public.legacy_logs`\nconst archive = \"legacy_logs_archive\"\n")
	write(t, root, "node_modules/pkg/index.js", "customers.email; legacy_logs\n")
	write(t, root, "assets/blob.bin", "legacy_logs\x00customers email\n")
	write(t, root, "db/migrate/001_drop.sql", migration)
	outside := t.TempDir()
	write(t, outside, "secret.rb", "LegacyLog = legacy_logs\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.rb"), filepath.Join(root, "app/linked.rb")); err != nil {
		t.Fatal(err)
	}
	return root
}

func analyze(t *testing.T) *migrate.Report {
	t.Helper()
	rep, err := sqlite.Analyze(t.Context(), sqlite.BaseFromDump(baseSchema), []ingest.Input{{Name: "001_drop.sql", SQL: migration}})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func places(us []migrate.Usage) []string {
	var out []string
	for _, u := range us {
		out = append(out, fmt.Sprintf("%s %s:%d %s", u.Name, u.File, u.Line, u.Text))
	}
	return out
}

func TestApply(t *testing.T) {
	root := project(t)
	rep := analyze(t)
	ts := usage.Targets(rep)
	if len(ts) != 2 || ts[0].Name != "main.legacy_logs" || ts[1].Name != "main.customers.email" || ts[1].Table != "customers" {
		t.Fatalf("targets %+v from diff %+v", ts, rep.Diff)
	}

	before := len(rep.Warnings)
	usage.Apply(t.Context(), rep, root, []string{filepath.Join(root, "db/migrate")})
	want := []string{
		"main.legacy_logs cmd/main.go:3 const q = `SELECT id FROM public.legacy_logs`",
		`main.customers.email app/models/customer.rb:2 validates :email, presence: true`,
		`main.customers.email web/src/api.ts:2 await db.query('SELECT "email" FROM customers');`,
		`main.customers.email web/src/api.ts:3 return prisma.customer.findMany({ select: { email: true } });`,
	}
	if got := places(rep.Usages); !slices.Equal(got, want) {
		t.Fatalf("usages\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(rep.Warnings) != before {
		t.Fatalf("warnings %q", rep.Warnings[before:])
	}
	for i, want := range []string{
		"main.customers.email is still named in 3 places in the project, e.g. app/models/customer.rb:2",
		"main.legacy_logs is still named in 1 place in the project, e.g. cmd/main.go:3",
	} {
		s := rep.Statements[i]
		f := s.Findings[len(s.Findings)-1]
		if f.RuleID != usage.RuleID || f.Rating != "high" || f.Message != want || s.Rating.Rank() < 2 ||
			f.SafeAlternative != "expand and contract: stop using it in one release, drop it in the next" {
			t.Fatalf("statement %d %q: %+v", i, s.SQL, f)
		}
	}
	if rep.Rating.Rank() < 2 {
		t.Fatalf("report rating %s", rep.Rating)
	}

	// The analysed file's name skips it wherever it sits; without any skip the migration itself is found.
	rep = analyze(t)
	usage.Apply(t.Context(), rep, root, []string{"001_drop.sql"})
	if len(rep.Usages) != 4 {
		t.Fatalf("skipped by name: %q", places(rep.Usages))
	}
	rep = analyze(t)
	usage.Apply(t.Context(), rep, root, nil)
	if got := places(rep.Usages); len(got) != 6 || !strings.Contains(got[1], "db/migrate/001_drop.sql:2") {
		t.Fatalf("unskipped: %q", got)
	}

	// No project folder, no search.
	rep = analyze(t)
	usage.Apply(t.Context(), rep, "", nil)
	for _, s := range rep.Statements {
		for _, f := range s.Findings {
			if f.RuleID == usage.RuleID || rep.Usages != nil {
				t.Fatalf("searched without a folder: %+v", rep)
			}
		}
	}
}

func TestModelNamesGateColumns(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.ts", "const x = order.status;\n")                       // no OrderItem in this file
	write(t, root, "b.ts", "import { OrderItem } from './m';\nitem.status\n") // CamelCase singular model
	write(t, root, "c.py", "class OrderItems: pass\nstatus = 1\n")
	write(t, root, "d.rb", "has_many :order_items\nstatuses\n") // a longer identifier
	ts := []usage.Target{{Name: "order_items.status", Ident: "status", Table: "order_items"}}
	us, warn := usage.Search(t.Context(), root, ts, usage.DefaultBudget)
	want := []string{"order_items.status b.ts:2 item.status", "order_items.status c.py:2 status = 1"}
	if got := places(us); !slices.Equal(got, want) || len(warn) != 0 || ts[0].Found != 2 {
		t.Fatalf("got %q warnings %q found %d", got, warn, ts[0].Found)
	}
}

func TestBudget(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "// "+strings.Repeat("x", 300)+" legacy_logs\n"+strings.Repeat("// legacy_logs\n", 25))
	write(t, root, "b.go", "legacy_logs\n")
	big := strings.Repeat("legacy_logs\n", (1<<20)/12+1)
	write(t, root, "c.go", big)

	ts := []usage.Target{{Name: "legacy_logs", Ident: "legacy_logs"}}
	us, warn := usage.Search(t.Context(), root, ts, usage.DefaultBudget)
	if len(us) != 20 || ts[0].Found != 27 || len(warn) != 0 {
		t.Fatalf("%d usages, found %d (c.go of %d bytes must be skipped), warnings %q", len(us), ts[0].Found, len(big), warn)
	}

	b := usage.DefaultBudget
	b.MaxFiles = 1
	us, warn = usage.Search(t.Context(), root, ts, b)
	if ts[0].Found != 26 || !slices.Equal(warn, []string{"the project search stopped at 1 files; files after them were not searched"}) {
		t.Fatalf("file cap: found %d, warnings %q", ts[0].Found, warn)
	}
	if u := us[0]; u.Name != "legacy_logs" || len([]rune(u.Text)) != 200 {
		t.Fatalf("text not clipped to 200: %d", len([]rune(u.Text)))
	}

	b = usage.DefaultBudget
	b.Timeout = time.Nanosecond
	_, warn = usage.Search(t.Context(), root, ts, b)
	if len(warn) != 1 || !strings.HasPrefix(warn[0], "the project search stopped after 1ns") {
		t.Fatalf("timeout warnings %q", warn)
	}

	_, warn = usage.Search(t.Context(), filepath.Join(root, "missing"), ts, usage.DefaultBudget)
	if len(warn) != 1 || !strings.Contains(warn[0], "was not searched") {
		t.Fatalf("missing folder warnings %q", warn)
	}
}

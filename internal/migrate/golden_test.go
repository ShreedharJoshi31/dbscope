package migrate

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/rules"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/migrate/stats"
	"github.com/dbscope/dbscope/internal/sample"
	"github.com/dbscope/dbscope/internal/schema"
)

// golden is one statement's expected outcome against the sample schema
// (PostgreSQL 17 unless pg is set). setup statements run first in the same
// file; the expectation is for the last statement.
type golden struct {
	name  string
	pg    int
	setup string
	sql   string
	tx    ingest.Mode
	// expected
	kind        string
	locks       string // "mode relation; ..." in order
	work        string
	lt          *bool
	rating      score.Rating
	score       int
	findings    []string // rule IDs, in order
	contains    string   // substring of the first finding's message
	notContains string   // must not appear in any finding's message
}

var yes, no = true, false

func analyzeOne(t *testing.T, g golden) Statement {
	t.Helper()
	m, err := sample.Model()
	if err != nil {
		t.Fatal(err)
	}
	sql := g.sql
	if g.setup != "" {
		sql = g.setup + ";\n" + g.sql
	}
	rep, err := Analyze(t.Context(), SampleBase(m), Request{Files: []ingest.Input{{Name: "m.sql", SQL: sql}}, TxMode: g.tx, PGVersion: g.pg})
	if err != nil {
		t.Fatal(err)
	}
	return rep.Statements[len(rep.Statements)-1]
}

func lockText(s Statement) string {
	var parts []string
	for _, l := range s.Locks {
		parts = append(parts, l.Mode+" "+l.Relation)
	}
	return strings.Join(parts, "; ")
}

func ids(s Statement) []string {
	out := []string{}
	for _, f := range s.Findings {
		out = append(out, f.RuleID)
	}
	return out
}

// The sample's shop.orders is 20072 pages (157 MiB, tier 1), app.events
// 307503 pages (2.3 GiB, tier 2), billing.plans 1 page (tier 0).
const lt = "SET lock_timeout = '2s'"

var goldens = []golden{
	// ---- indexes ----
	{name: "create index", setup: lt, sql: "CREATE INDEX o_idx ON shop.orders (placed_at)",
		kind: "CREATE INDEX", locks: "SHARE shop.orders", work: "index_build", lt: &yes, score: 3, rating: score.Medium,
		findings: []string{"DBS-INDEX-NOT-CONCURRENT"}},
	{name: "create index without timeout", sql: "CREATE INDEX o_idx ON shop.orders (placed_at)",
		score: 6, rating: score.Medium, findings: []string{"DBS-INDEX-NOT-CONCURRENT", "DBS-NO-LOCK-TIMEOUT"}},
	{name: "create index big table", setup: lt, sql: "CREATE INDEX e_idx ON app.events (kind)",
		score: 5, rating: score.Medium, findings: []string{"DBS-INDEX-NOT-CONCURRENT"}},
	{name: "create index concurrently", sql: "CREATE INDEX CONCURRENTLY o_idx ON shop.orders (placed_at)",
		kind: "CREATE INDEX CONCURRENTLY", locks: "SHARE UPDATE EXCLUSIVE shop.orders", work: "index_build", score: 1, rating: score.Low, findings: []string{}},
	{name: "OVERRIDE concurrently in a transaction is critical", sql: "BEGIN; CREATE INDEX CONCURRENTLY o_idx ON shop.orders (placed_at)",
		rating: score.Critical, findings: []string{"DBS-TX-NOT-ALLOWED"}, contains: "inside the BEGIN at line 1"},
	{name: "concurrently in a golang-migrate implicit transaction", setup: "SELECT 1", sql: "CREATE INDEX CONCURRENTLY o_idx ON shop.orders (placed_at)",
		tx: ingest.ModeFile, rating: score.Critical, findings: []string{"DBS-TX-NOT-ALLOWED"}},
	{name: "concurrently on a partitioned table fails", sql: "CREATE INDEX CONCURRENTLY a_idx ON app.audit_log (action)",
		rating: score.Critical, findings: []string{"DBS-PARTITIONED-UNSUPPORTED"}},
	{name: "create index on partitioned table locks partitions", setup: lt, sql: "CREATE INDEX a_idx ON app.audit_log (action)",
		locks:    "SHARE app.audit_log; SHARE app.audit_log_2026_08; SHARE app.audit_log_2026_09; SHARE app.audit_log_2026_10",
		findings: []string{"DBS-INDEX-NOT-CONCURRENT"}},
	{name: "index on a table created in the migration is quiet", setup: "CREATE TABLE shop.x (id int)", sql: "CREATE INDEX x_idx ON shop.x (id)",
		score: 0, rating: score.Low, findings: []string{}},
	{name: "drop index", setup: lt, sql: "DROP INDEX shop.orders_open_idx",
		kind: "DROP INDEX", locks: "ACCESS EXCLUSIVE shop.orders; ACCESS EXCLUSIVE shop.orders_open_idx", work: "none", score: 1, rating: score.Low,
		findings: []string{"DBS-DROP-INDEX-NOT-CONCURRENT"}},
	{name: "drop index concurrently", sql: "DROP INDEX CONCURRENTLY shop.orders_open_idx",
		locks: "SHARE UPDATE EXCLUSIVE shop.orders; SHARE UPDATE EXCLUSIVE shop.orders_open_idx", rating: score.Low, findings: []string{}},
	{name: "reindex table", setup: lt, sql: "REINDEX TABLE shop.orders", work: "index_build",
		findings: []string{"DBS-REINDEX-NOT-CONCURRENT"}},
	{name: "reindex concurrently in a transaction", sql: "BEGIN; REINDEX INDEX CONCURRENTLY shop.orders_open_idx",
		rating: score.Critical, findings: []string{"DBS-TX-NOT-ALLOWED"}},

	// ---- ADD COLUMN ----
	{name: "add column", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN note text",
		kind: "ALTER TABLE ADD COLUMN", locks: "ACCESS EXCLUSIVE shop.orders", work: "none", lt: &yes, score: 1, rating: score.Low, findings: []string{}},
	{name: "add column without timeout", sql: "ALTER TABLE shop.orders ADD COLUMN note text",
		lt: &no, score: 4, rating: score.Medium, findings: []string{"DBS-NO-LOCK-TIMEOUT"}},
	{name: "add column constant default", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN flag boolean NOT NULL DEFAULT false",
		work: "none", score: 1, rating: score.Low, findings: []string{}},
	{name: "now() is stable: no rewrite", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN seen timestamptz DEFAULT now()",
		work: "none", rating: score.Low, findings: []string{}},
	{name: "CURRENT_TIMESTAMP is stable", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN seen timestamptz DEFAULT CURRENT_TIMESTAMP",
		work: "none", rating: score.Low, findings: []string{}},
	{name: "clock_timestamp() is volatile", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN seen timestamptz DEFAULT clock_timestamp()",
		work: "rewrite", score: 7, rating: score.High, findings: []string{"DBS-ADD-COLUMN-REWRITE"}, contains: "clock_timestamp()"},
	{name: "gen_random_uuid() on a big table", setup: lt, sql: "ALTER TABLE app.events ADD COLUMN uid uuid DEFAULT gen_random_uuid()",
		work: "rewrite", score: 13, rating: score.Critical, findings: []string{"DBS-ADD-COLUMN-REWRITE"}},
	{name: "unknown function assumed volatile", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN k text DEFAULT app.make_key()",
		work: "rewrite", findings: []string{"DBS-ADD-COLUMN-REWRITE"}, contains: "assumes VOLATILE"},
	{name: "serial rewrites", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN n bigserial",
		work: "rewrite", findings: []string{"DBS-ADD-COLUMN-REWRITE"}, contains: "bigserial"},
	{name: "identity rewrites", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN n bigint GENERATED ALWAYS AS IDENTITY",
		work: "rewrite", findings: []string{"DBS-ADD-COLUMN-REWRITE"}},
	{name: "stored generated rewrites", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN d bigint GENERATED ALWAYS AS (total_cents / 100) STORED",
		work: "rewrite", findings: []string{"DBS-ADD-COLUMN-REWRITE"}},
	{name: "OVERRIDE NOT NULL without default on a non-empty table", setup: lt, sql: "ALTER TABLE shop.orders ADD COLUMN must text NOT NULL",
		rating: score.Critical, findings: []string{"DBS-WILL-FAIL"}, contains: "has rows"},
	{name: "NOT NULL without default on a new table is fine", setup: "CREATE TABLE shop.x (id int);" + lt, sql: "ALTER TABLE shop.x ADD COLUMN must text NOT NULL",
		rating: score.Low, findings: []string{}},
	{name: "drop column", setup: lt, sql: "ALTER TABLE shop.orders DROP COLUMN currency",
		rating: score.Critical, findings: []string{"DBS-DROP-COLUMN"}, contains: "depended on by"},
	{name: "drop referenced column needs CASCADE", setup: lt, sql: "ALTER TABLE shop.customers DROP COLUMN id",
		rating: score.Critical, findings: []string{"DBS-DROP-COLUMN", "DBS-WILL-FAIL"}},
	{name: "drop table a view reads: one will-fail finding, not two", setup: lt, sql: "DROP TABLE billing.plans",
		rating: score.Critical, findings: []string{"DBS-DROP-TABLE", "DBS-WILL-FAIL"}, notContains: "will fail without CASCADE because"},

	// ---- ALTER COLUMN TYPE: tool disagreement ----
	{name: "DISAGREEMENT varchar widening: AE lock, no rewrite", setup: lt + "; ALTER TABLE shop.customers ADD COLUMN nick varchar(20)",
		sql:  "ALTER TABLE shop.customers ALTER COLUMN nick TYPE varchar(40)",
		kind: "ALTER TABLE ALTER COLUMN TYPE", locks: "ACCESS EXCLUSIVE shop.customers", work: "none", score: 1, rating: score.Low,
		findings: []string{"DBS-TYPE-NO-REWRITE"}, contains: "does not rewrite"},
	{name: "varchar to text", setup: lt + "; ALTER TABLE shop.customers ADD COLUMN nick varchar(20)",
		sql: "ALTER TABLE shop.customers ALTER COLUMN nick TYPE text", work: "none", findings: []string{"DBS-TYPE-NO-REWRITE"}},
	{name: "text to varchar(n) rewrites and narrows", setup: lt, sql: "ALTER TABLE shop.customers ALTER COLUMN email TYPE varchar(100)",
		work: "rewrite", findings: []string{"DBS-TYPE-REWRITE", "DBS-TYPE-NARROWING"}, rating: score.High},
	{name: "int to bigint rewrites", setup: lt, sql: "ALTER TABLE app.roles ALTER COLUMN id TYPE bigint",
		work: "rewrite", findings: []string{"DBS-TYPE-REWRITE"}},
	{name: "USING forces a rewrite", setup: lt + "; ALTER TABLE shop.customers ADD COLUMN nick varchar(20)",
		sql: "ALTER TABLE shop.customers ALTER COLUMN nick TYPE varchar(40) USING upper(nick)", work: "rewrite", findings: []string{"DBS-TYPE-REWRITE"}},

	// ---- NOT NULL ----
	{name: "set not null scans", setup: lt, sql: "ALTER TABLE shop.customers ALTER COLUMN phone SET NOT NULL",
		work: "scan", findings: []string{"DBS-SET-NOT-NULL", "DBS-NEW-NOT-NULL"}, rating: score.High},
	{name: "set not null proven by a validated check", setup: lt + "; ALTER TABLE shop.customers ADD CONSTRAINT p_nn CHECK (phone IS NOT NULL) NOT VALID; ALTER TABLE shop.customers VALIDATE CONSTRAINT p_nn",
		sql: "ALTER TABLE shop.customers ALTER COLUMN phone SET NOT NULL", work: "none", findings: []string{"DBS-NEW-NOT-NULL"}},

	// ---- constraints ----
	{name: "add fk validates", setup: lt, sql: "ALTER TABLE app.events ADD CONSTRAINT ev_fk FOREIGN KEY (user_id) REFERENCES app.users (id)",
		kind: "ALTER TABLE ADD FOREIGN KEY", locks: "SHARE ROW EXCLUSIVE app.events; SHARE ROW EXCLUSIVE app.users", work: "scan",
		score: 5, rating: score.Medium, findings: []string{"DBS-VALIDATE-IN-ADD"}},
	{name: "add fk not valid", setup: lt, sql: "ALTER TABLE app.events ADD CONSTRAINT ev_fk FOREIGN KEY (user_id) REFERENCES app.users (id) NOT VALID",
		work: "none", score: 1, rating: score.Low, findings: []string{}},
	{name: "validate fk", sql: "ALTER TABLE shop.orders VALIDATE CONSTRAINT orders_customer_id_fkey",
		locks: "SHARE UPDATE EXCLUSIVE shop.orders; ROW SHARE shop.customers", work: "scan", score: 1, rating: score.Low, findings: []string{}},
	{name: "VERSION fk not valid on partitioned table fails on 17", setup: lt,
		sql:    "ALTER TABLE app.audit_log ADD CONSTRAINT al_user FOREIGN KEY (user_id) REFERENCES app.users (id) NOT VALID",
		rating: score.Critical, findings: []string{"DBS-PARTITIONED-UNSUPPORTED"}, contains: "PostgreSQL 17"},
	{name: "VERSION fk not valid on partitioned table works on 18", pg: 180000, setup: lt,
		sql:    "ALTER TABLE app.audit_log ADD CONSTRAINT al_user FOREIGN KEY (user_id) REFERENCES app.users (id) NOT VALID",
		locks:  "SHARE ROW EXCLUSIVE app.audit_log; SHARE ROW EXCLUSIVE app.users; SHARE ROW EXCLUSIVE app.audit_log_2026_08; SHARE ROW EXCLUSIVE app.audit_log_2026_09; SHARE ROW EXCLUSIVE app.audit_log_2026_10",
		rating: score.Low, findings: []string{}},
	{name: "VERSION attach partition locks referencing tables (14: ACCESS SHARE)", pg: 140000, setup: lt + "; CREATE TABLE shop.x (id bigint REFERENCES app.audit_log (id))",
		sql:   "ALTER TABLE app.audit_log ATTACH PARTITION app.audit_log_2026_11 FOR VALUES FROM ('2026-11-01') TO ('2026-12-01')",
		locks: "SHARE UPDATE EXCLUSIVE app.audit_log; ACCESS EXCLUSIVE app.audit_log_2026_11; ACCESS SHARE shop.x", findings: []string{"DBS-ATTACH-SCAN"}},
	{name: "VERSION attach partition locks referencing tables (18: SHARE ROW EXCLUSIVE)", pg: 180000, setup: lt + "; CREATE TABLE shop.x (id bigint REFERENCES app.audit_log (id))",
		sql:   "ALTER TABLE app.audit_log ATTACH PARTITION app.audit_log_2026_11 FOR VALUES FROM ('2026-11-01') TO ('2026-12-01')",
		locks: "SHARE UPDATE EXCLUSIVE app.audit_log; ACCESS EXCLUSIVE app.audit_log_2026_11; SHARE ROW EXCLUSIVE shop.x", findings: []string{"DBS-ATTACH-SCAN"}},
	{name: "add check validates", setup: lt, sql: "ALTER TABLE shop.orders ADD CONSTRAINT pos CHECK (total_cents > 0)",
		work: "scan", score: 4, rating: score.Medium, findings: []string{"DBS-VALIDATE-IN-ADD"}},
	{name: "add check not valid", setup: lt, sql: "ALTER TABLE shop.orders ADD CONSTRAINT pos CHECK (total_cents > 0) NOT VALID",
		work: "none", rating: score.Low, findings: []string{}},
	{name: "add unique", setup: lt, sql: "ALTER TABLE shop.orders ADD CONSTRAINT o_u UNIQUE (placed_at)",
		work: "index_build", findings: []string{"DBS-UNIQUE-CONSTRAINT"}, contains: "ACCESS EXCLUSIVE"},
	{name: "add unique using index", setup: lt + "; CREATE UNIQUE INDEX CONCURRENTLY o_u_idx ON shop.orders (placed_at)",
		sql: "ALTER TABLE shop.orders ADD CONSTRAINT o_u UNIQUE USING INDEX o_u_idx", work: "none", rating: score.Low, findings: []string{}},
	{name: "add exclude", setup: lt, sql: "ALTER TABLE shop.orders ADD CONSTRAINT o_x EXCLUDE USING gist (customer_id WITH =)",
		findings: []string{"DBS-EXCLUSION"}},

	// ---- renames, drops ----
	{name: "OVERRIDE rename column with dependents is high", setup: lt, sql: "ALTER TABLE shop.customers RENAME COLUMN email TO email_address",
		score: 1, rating: score.High, findings: []string{"DBS-RENAME"}, contains: "view shop.order_summaries"},
	{name: "rename column without dependents is medium", setup: lt, sql: "ALTER TABLE shop.warehouses RENAME COLUMN name TO display_name",
		rating: score.Medium, findings: []string{"DBS-RENAME"}},
	{name: "rename table", setup: lt, sql: "ALTER TABLE shop.reviews RENAME TO product_reviews", findings: []string{"DBS-RENAME"}},
	// DROP TABLE also takes ACCESS EXCLUSIVE on the tables its foreign keys reference (measured).
	{name: "OVERRIDE drop table is data loss", setup: lt, sql: "DROP TABLE shop.reviews",
		locks: "ACCESS EXCLUSIVE shop.reviews; ACCESS EXCLUSIVE shop.customers; ACCESS EXCLUSIVE shop.products", rating: score.Critical, findings: []string{"DBS-DROP-TABLE"}},
	{name: "drop referenced table fails without cascade", setup: lt, sql: "DROP TABLE shop.orders",
		rating: score.Critical, findings: []string{"DBS-DROP-TABLE", "DBS-WILL-FAIL"}, contains: "removes the table"},
	{name: "drop view with dependents", setup: lt + "; CREATE VIEW shop.v2 AS SELECT id FROM shop.order_summaries", sql: "DROP VIEW shop.order_summaries",
		rating: score.Critical, findings: []string{"DBS-DROP-TABLE", "DBS-WILL-FAIL"}},
	{name: "truncate", setup: lt, sql: "TRUNCATE shop.carts", rating: score.Critical, findings: []string{"DBS-TRUNCATE"}},

	// ---- maintenance ----
	{name: "vacuum full", setup: lt, sql: "VACUUM FULL shop.orders", kind: "VACUUM FULL", work: "rewrite", findings: []string{"DBS-REWRITE"}},
	{name: "vacuum full in a transaction", sql: "BEGIN; VACUUM FULL shop.orders", rating: score.Critical, findings: []string{"DBS-TX-NOT-ALLOWED", "DBS-REWRITE", "DBS-NO-LOCK-TIMEOUT"}},
	{name: "cluster", setup: lt, sql: "CLUSTER shop.orders USING orders_customer_idx", work: "rewrite", findings: []string{"DBS-REWRITE"}},
	{name: "refresh matview", setup: lt, sql: "REFRESH MATERIALIZED VIEW billing.mrr_by_plan",
		locks: "ACCESS EXCLUSIVE billing.mrr_by_plan; ACCESS SHARE billing.plans; ACCESS SHARE billing.subscriptions", findings: []string{"DBS-REFRESH-MATVIEW"}},
	{name: "detach partition", setup: lt, sql: "ALTER TABLE app.audit_log DETACH PARTITION app.audit_log_2026_08",
		findings: []string{"DBS-DETACH-NOT-CONCURRENT"}},
	{name: "detach concurrently in a transaction", sql: "BEGIN; ALTER TABLE app.audit_log DETACH PARTITION app.audit_log_2026_08 CONCURRENTLY",
		rating: score.Critical, findings: []string{"DBS-TX-NOT-ALLOWED"}},
	{name: "unbatched update", setup: lt, sql: "UPDATE shop.orders SET currency = 'EUR'", kind: "UPDATE",
		locks: "ROW EXCLUSIVE shop.orders", rating: score.Medium, findings: []string{"DBS-BACKFILL"}},
	{name: "lock table", setup: lt, sql: "BEGIN; LOCK TABLE shop.orders IN SHARE MODE", locks: "SHARE shop.orders", findings: []string{"DBS-LOCK-TABLE"}},

	// ---- transactions and timeouts ----
	{name: "AE already held in the transaction", setup: "BEGIN; " + lt + "; ALTER TABLE shop.orders ADD COLUMN a int",
		sql: "ALTER TABLE shop.customers ADD COLUMN b int", score: 3, rating: score.Medium, findings: []string{"DBS-AE-HELD"}},
	{name: "SET LOCAL outside a transaction", setup: "SET LOCAL lock_timeout = '2s'", sql: "ALTER TABLE shop.orders ADD COLUMN a int",
		lt: &no, findings: []string{"DBS-NO-LOCK-TIMEOUT"}},
	{name: "enum add value in a transaction", sql: "BEGIN; ALTER TYPE shop.order_status ADD VALUE 'on_hold'",
		kind: "ALTER TYPE ADD VALUE", locks: "", rating: score.Low, findings: []string{"DBS-ENUM-VALUE-TX"}},

	// ---- parser limit ----
	{name: "OVERRIDE PG18 syntax on 18 is high", pg: 180000, sql: "ALTER TABLE shop.orders ADD COLUMN d int GENERATED ALWAYS AS (total_cents / 100) VIRTUAL",
		kind: "unparseable", rating: score.High, findings: []string{"DBS-PARSE"}, contains: "PostgreSQL 17 grammar"},
	{name: "OVERRIDE PG18 syntax on 17 is a syntax error", sql: "ALTER TABLE shop.orders ADD CONSTRAINT c CHECK (total_cents > 0) NOT ENFORCED",
		rating: score.Critical, findings: []string{"DBS-PARSE"}, contains: "PostgreSQL 17 will reject"},
}

func TestGoldenRules(t *testing.T) {
	for _, g := range goldens {
		t.Run(g.name, func(t *testing.T) {
			s := analyzeOne(t, g)
			got := fmt.Sprintf("kind=%q locks=%q work=%s lt=%v score=%d rating=%s findings=%v", s.Kind, lockText(s), s.Work, s.LockTimeout, s.Score, s.Rating, ids(s))
			if g.kind != "" && s.Kind != g.kind {
				t.Errorf("kind %q, want %q\n%s", s.Kind, g.kind, got)
			}
			if g.locks != "" && lockText(s) != g.locks {
				t.Errorf("locks %q, want %q", lockText(s), g.locks)
			}
			if g.work != "" && s.Work != g.work {
				t.Errorf("work %s, want %s\n%s", s.Work, g.work, got)
			}
			if g.lt != nil && s.LockTimeout != *g.lt {
				t.Errorf("lockTimeout %v, want %v", s.LockTimeout, *g.lt)
			}
			if g.rating != "" && s.Rating != g.rating {
				t.Errorf("rating %s, want %s\n%s\n%+v", s.Rating, g.rating, got, s.Findings)
			}
			if g.score != 0 && s.Score != g.score || (g.score == 0 && g.rating == score.Low && len(g.findings) == 0 && s.Score > 2) {
				t.Errorf("score %d, want %d\n%s", s.Score, g.score, got)
			}
			if !slices.Equal(ids(s), g.findings) {
				t.Errorf("findings %v, want %v\n%+v", ids(s), g.findings, s.Findings)
			}
			if g.contains != "" && (len(s.Findings) == 0 || !strings.Contains(s.Findings[0].Message, g.contains)) {
				t.Errorf("first finding does not mention %q: %+v", g.contains, s.Findings)
			}
			for _, f := range s.Findings {
				if g.notContains != "" && strings.Contains(f.Message, g.notContains) {
					t.Errorf("finding %s mentions %q: %s", f.RuleID, g.notContains, f.Message)
				}
			}
			for _, f := range s.Findings {
				if _, ok := rules.Findings[f.RuleID]; !ok {
					t.Errorf("finding %s is not in the catalog", f.RuleID)
				}
				if f.Message == "" || strings.Contains(f.Message, "{") || strings.Contains(f.SafeAlternative, "{") {
					t.Errorf("unfilled text %+v", f)
				}
			}
		})
	}
}

// Every rule row is reachable by Lookup and every finding a row names exists.
func TestRuleTable(t *testing.T) {
	keys := map[string]bool{}
	for _, r := range rules.Table {
		keys[r.Key] = true
		if r.Finding != "" {
			if _, ok := rules.Findings[r.Finding]; !ok {
				t.Errorf("%s names unknown finding %s", r.Key, r.Finding)
			}
		}
	}
	for k := range keys {
		for _, major := range []int{14, 15, 16, 17, 18} {
			r, err := rules.Lookup(k, major)
			if err != nil && !strings.Contains(k, "generated_virtual") && !strings.Contains(k, "not_enforced") && !strings.Contains(k, "add_not_null") {
				t.Errorf("%s on %d: %v", k, major, err)
			}
			_ = r
		}
	}
	for id, d := range rules.Findings {
		if d.ID != id || d.Name == "" || d.Title == "" || d.Help == "" {
			t.Errorf("catalog entry %s incomplete: %+v", id, d)
		}
	}
}

// Scoring formula examples from docs/research-migrations.md section 4.
func TestScoreExamples(t *testing.T) {
	for _, c := range []struct {
		in   score.Inputs
		want int
		band score.Rating
	}{
		{score.Inputs{L: 3, W: 0, Z: 2, LockTimeout: true}, 1, score.Low},
		{score.Inputs{L: 3, W: 0, Z: 2}, 4, score.Medium},
		{score.Inputs{L: 3, W: 2, Z: 2, LockTimeout: true}, 13, score.Critical},
		{score.Inputs{L: 3, W: 1, Z: 3, LockTimeout: true}, 10, score.High},
		{score.Inputs{L: 1, W: 1, Z: 4}, 4, score.Medium},
		{score.Inputs{L: 0, W: 2, Z: 4}, 0, score.Low},
		{score.Inputs{L: 2, W: 1, Z: 1, LockTimeout: true, Held: true}, 5, score.Medium},
		{score.Inputs{L: 3, W: 0, Z: 2, LockTimeout: true, Hot: true}, 3, score.Medium},
		{score.Inputs{L: 1, W: 1, Z: 1, Hot: true}, 1, score.Low},
	} {
		if got := score.Score(c.in); got != c.want || score.Band(got) != c.band {
			t.Errorf("%+v: %d %s, want %d %s", c.in, got, score.Band(got), c.want, c.band)
		}
	}
	for _, c := range []struct {
		bytes int64
		known bool
		tier  int
	}{{0, false, 2}, {9 << 20, true, 0}, {10 << 20, true, 1}, {1 << 30, true, 2}, {49 << 30, true, 3}, {50 << 30, true, 4}} {
		if got := score.Tier(c.bytes, c.known); got != c.tier {
			t.Errorf("tier(%d, %v) = %d, want %d", c.bytes, c.known, got, c.tier)
		}
	}
}

func TestReportBasics(t *testing.T) {
	m, _ := sample.Model()
	rep, err := Analyze(t.Context(), SampleBase(m), Request{Files: []ingest.Input{
		{Name: "a.sql", SQL: "\\connect app\nALTER TABLE nowhere.t ADD COLUMN a int;"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unanalyzable) != 1 || !strings.Contains(rep.Unanalyzable[0].Message, `\connect`) {
		t.Errorf("meta-command %+v", rep.Unanalyzable)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "nowhere.t is not in the base schema") {
		t.Errorf("warnings %v", rep.Warnings)
	}
	if !rep.Exceeds(score.Medium) || rep.Exceeds(score.High) {
		t.Errorf("rating %s", rep.Rating)
	}
}

func TestHotTableRaisesTheScore(t *testing.T) {
	m, err := sample.Model()
	if err != nil {
		t.Fatal(err)
	}
	rel := m.Relations[0].QName()
	run := func(scans, writes map[string]int64, dbWide bool, sql string) Statement {
		t.Helper()
		b := SampleBase(m)
		b.Scans, b.Writes, b.DatabaseTotals = scans, writes, dbWide
		for _, n := range scans {
			b.TotalScans += n
		}
		for _, n := range writes {
			b.TotalWrites += n
		}
		rep, err := Analyze(t.Context(), b, Request{Files: []ingest.Input{{Name: "m.sql", SQL: sql}}})
		if err != nil {
			t.Fatal(err)
		}
		return rep.Statements[len(rep.Statements)-1]
	}
	message := func(s Statement) string {
		for _, f := range s.Findings {
			if f.RuleID == "DBS-HOT-TABLE" {
				return f.Message
			}
		}
		return ""
	}
	busyReads := map[string]int64{rel.String(): 6000, "other.table": 6000}
	alter := fmt.Sprintf("SET lock_timeout = '2s';\nALTER TABLE %s ADD COLUMN flag int", rel)
	quiet := run(nil, nil, false, alter)
	hot := run(busyReads, nil, true, alter)
	if hot.Score != quiet.Score+2 || message(quiet) != "" {
		t.Fatalf("quiet %d %v, hot %d %v", quiet.Score, ids(quiet), hot.Score, ids(hot))
	}
	if got := message(hot); !strings.Contains(got, "has 50% of this database's table scans (6000 scans since the statistics were last reset); reads and writes queue") {
		t.Fatalf("message %q", got)
	}
	if got := message(run(busyReads, nil, false, alter)); !strings.Contains(got, "of the table scans in the schemas read") {
		t.Fatalf("schemas-only message %q", got)
	}
	// Few scans in absolute terms, or a lock that stops nothing: not hot.
	if s := run(map[string]int64{rel.String(): 900}, nil, true, alter); message(s) != "" {
		t.Fatal("900 scans made the table hot")
	}
	if s := run(busyReads, nil, true, fmt.Sprintf("SELECT 1 FROM %s", rel)); message(s) != "" {
		t.Fatal("a read was flagged")
	}
	// A SHARE lock (CREATE INDEX) stops writes only: busy reads do not make it hot, busy writes do.
	index := fmt.Sprintf("SET lock_timeout = '2s';\nCREATE INDEX hot_ix ON %s (id)", rel)
	if s := run(busyReads, nil, true, index); message(s) != "" {
		t.Fatalf("reads made a SHARE lock hot: %q", message(s))
	}
	busyWrites := map[string]int64{rel.String(): 3000, "other.table": 1000}
	if got := message(run(nil, busyWrites, true, index)); !strings.Contains(got, "75% of this database's row writes (3000 rows written") || !strings.Contains(got, "writes queue behind the lock (reads continue)") {
		t.Fatalf("SHARE lock on a write-heavy table: %q", got)
	}
}

func TestPartitionedTablesSumTheirPartitions(t *testing.T) {
	q := func(n string) schema.QualifiedName { return schema.QualifiedName{Schema: "app", Name: n} }
	m := &schema.Model{Schemas: []string{"app"}, Relations: []schema.Relation{
		{Schema: "app", Name: "events", Kind: schema.KindPartitionedTable, Partitions: []schema.QualifiedName{q("events_2025"), q("events_2026")}},
		{Schema: "app", Name: "events_2025", Kind: schema.KindTable},
		{Schema: "app", Name: "events_2026", Kind: schema.KindPartitionedTable, Partitions: []schema.QualifiedName{q("events_2026_h1")}},
		{Schema: "app", Name: "events_2026_h1", Kind: schema.KindTable},
	}}
	b := FromStats(m, &stats.File{BlockSize: 8192, Relations: []stats.Relation{
		{Schema: "app", Name: "events_2025", Kind: "r", Scans: 3000, RowsWritten: 10, Partition: true},
		{Schema: "app", Name: "events_2026_h1", Kind: "r", Scans: 4000, RowsWritten: 20, Partition: true},
	}})
	if b.Scans["app.events"] != 7000 || b.Writes["app.events"] != 30 || b.Scans["app.events_2026"] != 4000 {
		t.Fatalf("scans %v writes %v", b.Scans, b.Writes)
	}
}

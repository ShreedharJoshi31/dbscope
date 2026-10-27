package groundtruth

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/parse"
	"github.com/dbscope/dbscope/internal/migrate/rules"
	"github.com/dbscope/dbscope/internal/migrate/stats"
	"github.com/dbscope/dbscope/internal/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

var only = flag.String("fixture", "", "run only fixtures whose name contains this")

// Lock is a measured lock on one relation (the strongest mode held or requested).
type Lock struct {
	Relation string `json:"relation"`
	Kind     string `json:"kind"` // pg_class.relkind
	Mode     string `json:"mode"`
	Granted  bool   `json:"granted"`
}

// Prediction is what the rules table says.
type Prediction struct {
	Rules []string          `json:"rules"`
	Locks map[string]string `json:"locks"`
	Work  string            `json:"work"`
	InTx  bool              `json:"txAllowed"`
	Fails bool              `json:"fails"`
	// NewStorage: the rules expect a new relfilenode for the target.
	NewStorage bool `json:"newStorage"`
	// Parsed is false for PostgreSQL 18 syntax the 17 grammar rejects.
	Parsed bool `json:"parsed"`

	indexLocks map[string]bool
}

// Measurement is what the server did.
type Measurement struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
	// Observed is "transaction" (locks read from a second session before
	// ROLLBACK) or "blocked_request" (statement that cannot run in a
	// transaction, caught waiting behind a third session's lock).
	Observed  string `json:"observed"`
	TxAllowed bool   `json:"txAllowed"`
	// ImplicitTxAllowed is measured for statements refused in a transaction
	// block: whether they also fail inside a multi-statement simple query.
	ImplicitTxAllowed *bool  `json:"implicitTxAllowed,omitempty"`
	Error             string `json:"error,omitempty"` // SQLSTATE
	ErrorMessage      string `json:"errorMessage,omitempty"`
	Locks             []Lock `json:"locks"`
	// ObjectLocks are non-relation locks (e.g. on a type).
	ObjectLocks []string `json:"objectLocks,omitempty"`
	Target      string   `json:"target"`
	// Rewritten are relations (tables and indexes) with a new relfilenode.
	Rewritten []string `json:"rewritten"`
	// Scanned: the target was sequentially scanned (pg_stat_xact_user_tables);
	// only measured in a transaction.
	Scanned   *bool      `json:"scanned,omitempty"`
	Predicted Prediction `json:"predicted"`
	Agrees    bool       `json:"agrees"`
	Problems  []string   `json:"problems,omitempty"`
}

// File is one server's ground truth.
type File struct {
	Server     string        `json:"server"`
	Major      int           `json:"major"`
	Fixtures   []Measurement `json:"fixtures"`
	Agreements int           `json:"agreements"`
}

func TestGroundTruth(t *testing.T) {
	servers := pgtest.Servers(t, "14", "17", "18")
	for _, srv := range servers {
		t.Run(srv.String(), func(t *testing.T) {
			t.Parallel()
			runServer(t, srv)
		})
	}
}

func runServer(t *testing.T, srv *pgtest.Server) {
	ctx := context.Background()
	tmpl := srv.NewDB(t)
	tmpl.Exec(t, schemaSQL)
	var version string
	if err := tmpl.Admin.QueryRow(ctx, "SELECT current_setting('server_version')").Scan(&version); err != nil {
		t.Fatal(err)
	}
	version, _, _ = strings.Cut(version, " ")
	tmpl.CloseAdmin()

	tsName := ""
	if srv.Major >= 15 {
		ts := srv.NewDB(t)
		tsName = fmt.Sprintf("%s_%d_%d", tablespace, srv.Major, time.Now().UnixNano()%1_000_000)
		ts.Exec(t, "SET allow_in_place_tablespaces = on")
		ts.Exec(t, "CREATE TABLESPACE "+tsName+" LOCATION ''")
		t.Cleanup(func() {
			if _, err := ts.Admin.Exec(ctx, "DROP TABLESPACE IF EXISTS "+tsName); err != nil {
				t.Errorf("drop tablespace: %v", err)
			}
		})
	}

	out := File{Server: version, Major: srv.Major, Fixtures: []Measurement{}}
	for _, f := range fixtures {
		if *only != "" && !strings.Contains(f.Name, *only) {
			continue
		}
		if f.MinMajor > srv.Major || (f.Tablespace && tsName == "") {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			m := measure(t, srv, tmpl.Name, f, tsName)
			m.Problems = compare(m)
			m.Agrees = len(m.Problems) == 0
			if !m.Agrees {
				t.Errorf("%s on PostgreSQL %s: rules disagree with the server:\n  %s\n  measured locks %v rewritten %v scanned %v txAllowed %v error %s %s\n  predicted %+v",
					f.Name, version, strings.Join(m.Problems, "\n  "), m.Locks, m.Rewritten, deref(m.Scanned), m.TxAllowed, m.Error, m.ErrorMessage, m.Predicted)
			}
			out.Fixtures = append(out.Fixtures, m)
		})
	}
	for _, m := range out.Fixtures {
		if m.Agrees {
			out.Agreements++
		}
	}
	logAnswers(t, out)
	if *only != "" || srv.Name == "dsn" {
		return // partial runs and shared servers do not overwrite the committed files
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", fmt.Sprintf("groundtruth-pg%d.json", srv.Major))
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func deref(b *bool) any {
	if b == nil {
		return "n/a"
	}
	return *b
}

type relInfo struct {
	name, kind string
	filenode   uint32
}

const relsQuery = `SELECT c.oid, n.nspname || '.' || c.relname, c.relkind::text, c.relfilenode
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'gt' AND c.relkind IN ('r','p','i','I','m','v')`

func relations(t *testing.T, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) map[uint32]relInfo {
	t.Helper()
	rows, err := q.Query(context.Background(), relsQuery)
	if err != nil {
		t.Fatal(err)
	}
	out := map[uint32]relInfo{}
	var oid uint32
	var r relInfo
	if _, err := pgx.ForEachRow(rows, []any{&oid, &r.name, &r.kind, &r.filenode}, func() error {
		out[oid] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func rewritten(before, after map[uint32]relInfo) []string {
	out := []string{}
	for oid, b := range before {
		if a, ok := after[oid]; ok && a.filenode != b.filenode && b.filenode != 0 {
			out = append(out, b.name)
		}
	}
	sort.Strings(out)
	return out
}

func oids(m map[uint32]relInfo) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

const locksQuery = `SELECT l.relation, l.mode, l.granted FROM pg_locks l
WHERE l.pid = $1 AND l.locktype = 'relation' AND l.relation = ANY($2::oid[])`

// objectLocksQuery names type locks by type (when the observer can see the
// type) and every other object lock by its catalog only, so the recorded
// ground truth does not depend on OIDs.
const objectLocksQuery = `SELECT DISTINCT CASE WHEN t.oid IS NOT NULL THEN 'type ' || format_type(t.oid, NULL)
            ELSE l.classid::regclass::text END || ': ' || l.mode
FROM pg_locks l
LEFT JOIN pg_type t ON l.classid = 'pg_type'::regclass AND t.oid = l.objid
WHERE l.pid = $1 AND l.locktype = 'object' AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY 1`

// locks reads pid's locks on relations that existed before the statement,
// keeping the strongest mode per relation.
func locks(t *testing.T, obs *pgx.Conn, pid int, before map[uint32]relInfo) ([]Lock, []string) {
	t.Helper()
	ctx := context.Background()
	rows, err := obs.Query(ctx, locksQuery, pid, oids(before))
	if err != nil {
		t.Fatal(err)
	}
	strongest := map[uint32]Lock{}
	var oid uint32
	var mode string
	var granted bool
	if _, err := pgx.ForEachRow(rows, []any{&oid, &mode, &granted}, func() error {
		cur, ok := strongest[oid]
		if !ok || rules.ModeFromLockName(mode) > rules.ModeFromLockName(cur.Mode) {
			strongest[oid] = Lock{Relation: before[oid].name, Kind: before[oid].kind, Mode: mode, Granted: granted}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out := make([]Lock, 0, len(strongest))
	for _, l := range strongest {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Relation < out[j].Relation })
	var objs []string
	rows, err = obs.Query(ctx, objectLocksQuery, pid)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if _, err := pgx.ForEachRow(rows, []any{&s}, func() error { objs = append(objs, s); return nil }); err != nil {
		t.Fatal(err)
	}
	return out, objs
}

func sqlstate(err error) (string, string) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code, pe.Message
	}
	if err != nil {
		return "XX000", err.Error()
	}
	return "", ""
}

func measure(t *testing.T, srv *pgtest.Server, template string, f fixture, tsName string) Measurement {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d := srv.NewDBFrom(t, template)
	prof, pw := d.Superuser(t)
	a := d.Connect(t, prof.User, pw) // runs the statement
	c := d.Connect(t, prof.User, pw) // holds the blocking lock
	obs := d.Admin                   // reads pg_locks
	sql := f.SQL
	if tsName != "" {
		sql = strings.ReplaceAll(sql, tablespace, tsName)
	}
	if f.Setup != "" {
		if _, err := a.Exec(ctx, f.Setup); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	target := f.Target
	if target == "" {
		target = "gt.t"
	}
	block := f.Block
	if block == "" {
		block = target
	}
	m := Measurement{Name: f.Name, SQL: f.SQL, Target: target, Locks: []Lock{}, Rewritten: []string{}}
	m.Predicted = predict(t, a, f, sql, srv.Major)
	before := relations(t, obs)
	var pid int
	if err := a.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}

	tx, err := a.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Scan counts not yet flushed from earlier transactions also show in
	// pg_stat_xact_user_tables, so the statement's scans are a delta.
	scansBefore := seqScans(t, tx, target)
	_, err = tx.Exec(ctx, sql)
	code, msg := sqlstate(err)
	switch code {
	case "":
		m.Observed, m.TxAllowed = "transaction", true
		m.Locks, m.ObjectLocks = locks(t, obs, pid, before)
		m.Rewritten = rewritten(before, relations(t, tx))
		scanned := seqScans(t, tx, target) > scansBefore
		m.Scanned = &scanned
		tx.Rollback(ctx)
	case "25001":
		tx.Rollback(ctx)
		m.Observed = "blocked_request"
		measureBlocked(t, ctx, a, c, obs, pid, sql, block, before, &m)
	default:
		tx.Rollback(ctx)
		m.Observed, m.TxAllowed, m.Error, m.ErrorMessage = "transaction", true, code, msg
	}
	return m
}

func seqScans(t *testing.T, tx pgx.Tx, rel string) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(context.Background(), `SELECT coalesce((SELECT seq_scan FROM pg_stat_xact_user_tables WHERE relid = to_regclass($1)), 0)`, rel).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func measureBlocked(t *testing.T, ctx context.Context, a, c, obs *pgx.Conn, pid int, sql, block string, before map[uint32]relInfo, m *Measurement) {
	// A multi-statement simple query is an implicit transaction block.
	_, err := a.Exec(ctx, "SELECT 1; "+sql)
	code, _ := sqlstate(err)
	implicit := code != "25001"
	m.ImplicitTxAllowed = &implicit
	if implicit {
		m.Error, m.ErrorMessage = sqlstate(err)
		return
	}
	hold, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// EXCLUSIVE conflicts with every mode except ACCESS SHARE, so the
	// statement is caught at its real lock rather than at the ACCESS SHARE
	// lock some commands (VACUUM, CLUSTER) take first to look the table up.
	if _, err := hold.Exec(ctx, "LOCK TABLE "+block+" IN EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Exec(ctx, sql)
		done <- err
	}()
	var finished bool
	var runErr error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case runErr = <-done:
			finished = true
		default:
		}
		if finished {
			break
		}
		ls, objs := locks(t, obs, pid, before)
		if slices.ContainsFunc(ls, func(l Lock) bool { return !l.Granted }) {
			m.Locks, m.ObjectLocks = ls, objs
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	hold.Rollback(ctx)
	if !finished {
		select {
		case runErr = <-done:
		case <-time.After(2 * time.Minute):
			t.Fatal("statement did not finish after the blocking lock was released")
		}
	}
	m.Error, m.ErrorMessage = sqlstate(runErr)
	m.Rewritten = rewritten(before, relations(t, obs))
}

// predict classifies the fixture against the introspected copy of the schema.
func predict(t *testing.T, conn *pgx.Conn, f fixture, sql string, major int) Prediction {
	t.Helper()
	ctx := context.Background()
	p := Prediction{Locks: map[string]string{}, indexLocks: map[string]bool{}}
	pf := parse.Split(sql)
	if len(pf.Statements) != 1 {
		t.Fatalf("fixture has %d statements", len(pf.Statements))
	}
	s := pf.Statements[0]
	if len(f.Rules) > 0 {
		if s.Err == nil {
			t.Fatalf("%s: expected the PostgreSQL 17 grammar to reject it, but it parsed", f.Name)
		}
		p.Rules, p.InTx, p.Work = f.Rules, true, rules.None.String()
		for _, key := range f.Rules {
			r, err := rules.Lookup(key, major)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Grammar18 {
				t.Fatalf("%s: rule %s is not marked Grammar18", f.Name, key)
			}
			p.InTx = p.InTx && r.InTx
			p.Work = max(r.Work, rules.None).String()
			for _, l := range r.Locks {
				if l.Role != rules.Target {
					t.Fatalf("%s: unparseable rule %s has a %s lock the test cannot resolve", f.Name, key, l.Role)
				}
				p.Locks["gt.t"] = l.Mode.LockName()
			}
		}
		return p
	}
	if s.Err != nil {
		t.Fatalf("%s does not parse: %v", f.Name, s.Err)
	}
	p.Parsed = true
	model, err := introspect.Introspect(ctx, conn, []string{"gt"}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	sf, _, err := stats.Read(ctx, tx, []string{"gt"})
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st := apply.New(model, &sf.Catalog)
	eff := rules.Classify(s.Node, st, major)
	p.Rules, p.InTx, p.Work = eff.Keys, eff.InTx, eff.Work.String()
	for _, l := range eff.Locks {
		p.Locks[l.Relation.String()] = l.Mode.LockName()
		if l.Index {
			p.indexLocks[l.Relation.String()] = true
		}
	}
	for _, k := range eff.Keys {
		if r, _ := rules.Lookup(k, major); r.NewStorage {
			p.NewStorage = true
		}
	}
	for _, fd := range eff.Findings {
		if fd.Class == rules.Fail {
			p.Fails = true
		}
	}
	return p
}

// compare lists every disagreement between the measurement and the prediction.
func compare(m Measurement) []string {
	p := m.Predicted
	var probs []string
	if (m.Error != "") != p.Fails {
		if m.Error != "" {
			return []string{fmt.Sprintf("PostgreSQL rejected it (%s %s) but the rules expect success", m.Error, m.ErrorMessage)}
		}
		return []string{"the rules expect PostgreSQL to reject it, but it succeeded"}
	}
	if m.Observed == "transaction" && !m.TxAllowed == p.InTx {
		probs = append(probs, fmt.Sprintf("txAllowed: measured %v, rules %v", m.TxAllowed, p.InTx))
	}
	if m.Observed == "blocked_request" && p.InTx {
		probs = append(probs, "txAllowed: measured false (25001), rules true")
	}
	if m.Error != "" {
		return probs // a failing statement's locks and storage are not compared
	}
	isIndex := func(kind string) bool { return kind == "i" || kind == "I" }
	switch m.Observed {
	case "transaction":
		measured := map[string]string{}
		for _, l := range m.Locks {
			if !isIndex(l.Kind) {
				measured[l.Relation] = l.Mode
			}
		}
		for rel, mode := range p.Locks {
			if p.indexLocks[rel] {
				if !slices.ContainsFunc(m.Locks, func(l Lock) bool { return l.Relation == rel && l.Mode == mode }) {
					probs = append(probs, fmt.Sprintf("index %s: rules %s, not measured", rel, mode))
				}
				continue
			}
			if got, ok := measured[rel]; !ok {
				probs = append(probs, fmt.Sprintf("%s: rules %s, measured no lock", rel, mode))
			} else if got != mode {
				probs = append(probs, fmt.Sprintf("%s: rules %s, measured %s", rel, mode, got))
			}
		}
		for rel, mode := range measured {
			if _, ok := p.Locks[rel]; !ok {
				probs = append(probs, fmt.Sprintf("%s: measured %s, rules have no lock", rel, mode))
			}
		}
	case "blocked_request":
		if len(m.Locks) == 0 {
			probs = append(probs, "no lock request was observed behind the blocking lock")
		}
		for _, l := range m.Locks {
			want, ok := p.Locks[l.Relation]
			switch {
			case !ok && isIndex(l.Kind):
				probs = append(probs, fmt.Sprintf("index %s: measured %s, rules have no lock", l.Relation, l.Mode))
			case !ok:
				probs = append(probs, fmt.Sprintf("%s: measured %s, rules have no lock", l.Relation, l.Mode))
			case want != l.Mode:
				probs = append(probs, fmt.Sprintf("%s: rules %s, measured %s (granted %v)", l.Relation, want, l.Mode, l.Granted))
			}
		}
	}
	gotNew := slices.Contains(m.Rewritten, m.Target)
	wantNew := p.Work == rules.Rewrite.String() || p.NewStorage
	if gotNew != wantNew {
		probs = append(probs, fmt.Sprintf("new storage for %s: measured %v, rules %v (work %s)", m.Target, gotNew, wantNew, p.Work))
	}
	if m.Scanned != nil {
		switch p.Work {
		case rules.None.String():
			if *m.Scanned && !p.NewStorage {
				probs = append(probs, fmt.Sprintf("%s was scanned, rules say work none", m.Target))
			}
		case rules.Scan.String(), rules.IndexBuild.String():
			if !*m.Scanned {
				probs = append(probs, fmt.Sprintf("%s was not scanned, rules say work %s", m.Target, p.Work))
			}
		}
	}
	return probs
}

// answers are the research items marked "Unverified", by fixture.
var answers = []struct{ question, fixture string }{
	{"text -> varchar(n) rewrites?", "type_text_to_varchar_n"},
	{"varchar(n) widening rewrites?", "type_varchar_widen"},
	{"numeric precision increase (same scale) rewrites?", "type_numeric_precision_up"},
	{"DROP INDEX CONCURRENTLY lock", "drop_index_concurrently"},
	{"ALTER TYPE ... ADD VALUE lock", "alter_type_add_value"},
	{"DROP TABLE locks on FK-referenced tables", "drop_table_fk_child"},
	{"ADD COLUMN serial rewrites?", "add_column_serial"},
	{"FK NOT VALID on a partitioned table", "add_fk_not_valid_partitioned"},
}

func logAnswers(t *testing.T, f File) {
	for _, a := range answers {
		for _, m := range f.Fixtures {
			if m.Name != a.fixture {
				continue
			}
			var locks []string
			for _, l := range m.Locks {
				locks = append(locks, fmt.Sprintf("%s %s%s", l.Relation, l.Mode, map[bool]string{false: " (waiting)"}[l.Granted]))
			}
			t.Logf("PG %s | %s: rewritten=%v locks=%v objectLocks=%v error=%s %s", f.Server, a.question, m.Rewritten, locks, m.ObjectLocks, m.Error, m.ErrorMessage)
		}
	}
	t.Logf("PG %s: %d of %d fixtures agree with the rules", f.Server, f.Agreements, len(f.Fixtures))
}

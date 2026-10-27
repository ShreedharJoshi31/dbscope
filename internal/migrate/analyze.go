package migrate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/rules"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/migrate/timeouts"
)

// DefaultVersion is used when neither the request nor the base names a
// server version: the parser's grammar version.
const DefaultVersion = 170000

// Request is one analysis.
type Request struct {
	Files             []ingest.Input
	TxMode            ingest.Mode
	AssumeLockTimeout bool
	// PGVersion overrides the base's server_version_num (e.g. 180000).
	PGVersion int
	// KeepOrder keeps the given file order when no framework is detected.
	KeepOrder bool
}

// pending is a statement whose rating waits for impact results.
type pending struct {
	rep      *Statement
	findings []rules.Finding
	band     score.Rating
}

type held struct {
	rel  apply.QName
	line int
}

// Analyze runs the pipeline. It only fails when live impact queries fail
// in a way that is not a skipped optional source.
func Analyze(ctx context.Context, b *Base, req Request) (*Report, error) {
	set := ingest.Load(req.Files, ingest.Options{Mode: req.TxMode, KeepOrder: req.KeepOrder})
	version := req.PGVersion
	if version == 0 && b.Model != nil {
		version = b.Model.ServerVersion
	}
	if version == 0 {
		version = DefaultVersion
	}
	major := version / 10000
	st := apply.New(b.Model, b.Catalog)
	tm := timeouts.New(timeouts.External{LockTimeout: b.LockTimeout, Assume: req.AssumeLockTimeout})
	rep := &Report{PGVersion: version, Rating: score.Low, Statements: []Statement{}, Impact: map[string][]impact.Item{},
		Unanalyzable: []Unanalyzable{}, Warnings: append(slices.Clone(b.Warnings), set.Warnings...), Base: b.Source}
	var work []*pending
	for _, f := range set.Files {
		tm.StartFile(f.Wrapped)
		for _, m := range f.Meta {
			rep.Unanalyzable = append(rep.Unanalyzable, Unanalyzable{File: f.Name, Line: m.Line,
				Message: fmt.Sprintf("psql meta-command %q is not SQL and was skipped", firstWord(m.Command))})
		}
		var hold *held
		beginLine := 0
		for i := range f.Statements {
			s := &f.Statements[i]
			p := analyzeStatement(st, tm, b, f, s, major, hold, beginLine, rep)
			work = append(work, p)
			if s.Err != nil {
				continue
			}
			if t := s.Node.GetTransactionStmt(); t != nil {
				switch p.rep.Kind {
				case "BEGIN":
					beginLine = s.Line
				case "COMMIT", "ROLLBACK":
					hold, beginLine = nil, 0
				}
				continue
			}
			if s.Tx == ingest.TxNone {
				hold = nil
				continue
			}
			if hold == nil {
				for _, l := range p.rep.Locks {
					q := splitKey(l.Relation)
					if l.Mode == rules.AccessExclusive.String() && !st.Created(q) && b.Model.Relation(q) != nil {
						hold = &held{rel: q, line: s.Line}
						break
					}
				}
			}
		}
		tm.EndFile()
	}
	rep.Diff = st.Diff()
	if err := resolveImpact(ctx, b, st, work, rep); err != nil {
		return nil, err
	}
	for _, p := range work {
		rate(p, rep.Impact)
		rep.Rating = score.Max(rep.Rating, p.rep.Rating)
		rep.Statements = append(rep.Statements, *p.rep)
	}
	return rep, nil
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

func splitKey(k string) apply.QName {
	nsp, name, _ := strings.Cut(k, ".")
	return apply.QName{Schema: nsp, Name: name}
}

var txWhy = map[ingest.Framework]string{
	ingest.Flyway:        "Flyway runs each migration in a transaction",
	ingest.Atlas:         "Atlas runs each file in a transaction unless it has `-- atlas:txmode none`",
	ingest.GolangMigrate: "golang-migrate sends the file as one multi-statement query, which PostgreSQL runs as one implicit transaction",
	ingest.Prisma:        "Prisma runs a multi-statement migration.sql in one transaction",
}

func analyzeStatement(st *apply.State, tm *timeouts.Machine, b *Base, f ingest.File, s *ingest.Statement, major int,
	hold *held, beginLine int, rep *Report) *pending {
	r := &Statement{File: f.Name, Line: s.Line, Column: s.Column, EndLine: s.EndLine, SQL: s.SQL, Tx: string(s.Tx),
		Locks: []Lock{}, Work: rules.None.String(), Findings: []Finding{}}
	p := &pending{rep: r, band: score.Low}
	if s.Err != nil {
		r.Kind = "unparseable"
		r.LockTimeout = tm.Current().LockTimeout
		msg := fmt.Sprintf("cannot be analyzed with the PostgreSQL 17 grammar (%s); review it by hand", s.Err.Message)
		class := rules.Fail
		if major >= 18 {
			class = rules.Info // rated High below
		} else {
			msg = fmt.Sprintf("syntax error: %s; PostgreSQL %d will reject this statement", s.Err.Message, major)
		}
		p.findings = append(p.findings, rules.Finding{ID: "DBS-PARSE", Class: class, Message: msg})
		rep.Unanalyzable = append(rep.Unanalyzable, Unanalyzable{File: f.Name, Line: s.Err.Line, Message: s.Err.Message})
		return p
	}
	inTx := s.Tx != ingest.TxNone
	eff := rules.Classify(s.Node, st, major)
	ts, tf := tm.Step(s.Node)
	r.Kind = eff.Kind
	r.Work = eff.Work.String()
	r.LockTimeout = ts.LockTimeout

	bytes, known := b.size(eff.Size.Rel, eff.Size.Heap, eff.Size.Indexes, eff.Size.Index, st)
	z := score.Tier(bytes, known)
	w := eff.Work.W()
	isHeld := hold != nil && inTx && (len(eff.Locks) > 0 || eff.Work != rules.None)
	best := score.Score(score.Inputs{Held: isHeld})
	var exposed []string
	var largest int64 = -1
	for _, l := range eff.Locks {
		r.Locks = append(r.Locks, Lock{Relation: l.Relation.String(), Mode: l.Mode.String()})
		if st.Created(l.Relation) {
			continue // created by this migration: nobody else uses it yet
		}
		L := l.Mode.Class()
		best = max(best, score.Score(score.Inputs{L: L, W: w, Z: z, LockTimeout: ts.LockTimeout, Held: isHeld}))
		if L >= 2 && !ts.LockTimeout {
			exposed = append(exposed, fmt.Sprintf("%s on %s", l.Mode, l.Relation))
		}
		if !l.Index {
			if n, ok := b.size(l.Relation, true, false, "", st); ok && n > largest {
				largest = n
			}
		}
	}
	r.Score = best
	p.band = score.Band(best)
	switch {
	case known && w > 0:
		r.SizeBytes = &bytes
	case largest >= 0:
		r.SizeBytes = &largest
	}

	p.findings = append(p.findings, eff.Findings...)
	p.findings = append(p.findings, tf...)
	if !eff.InTx && inTx {
		why := "the file runs in a transaction"
		switch s.Tx {
		case ingest.TxExplicit:
			why = "it is inside BEGIN"
			if beginLine > 0 {
				why = fmt.Sprintf("it is inside the BEGIN at line %d", beginLine)
			}
		case ingest.TxImplicit, ingest.TxFramework:
			if w, ok := txWhy[f.Framework]; ok {
				why = w
			} else {
				why = "--tx-mode file runs each file in a transaction"
			}
		}
		p.findings = append([]rules.Finding{{ID: "DBS-TX-NOT-ALLOWED", Class: rules.Fail,
			Message: fmt.Sprintf("%s cannot run inside a transaction block, and %s", r.Kind, why),
			Safe:    "Put it in its own migration that runs outside a transaction (Flyway executeInTransaction=false, a separate golang-migrate file, `-- atlas:txmode none`)"}},
			p.findings...)
	}
	if eff.AddsEnumValue && inTx {
		p.findings = append(p.findings, rules.Finding{ID: "DBS-ENUM-VALUE-TX", Class: rules.Info,
			Message: "the new enum value cannot be used by later statements in the same transaction",
			Safe:    "Use the new value in a later migration"})
	}
	if isHeld {
		p.findings = append(p.findings, rules.Finding{ID: "DBS-AE-HELD", Class: rules.Risk,
			Message: fmt.Sprintf("runs while ACCESS EXCLUSIVE on %s (taken at line %d) is still held, so its run time adds to the outage", hold.rel, hold.line),
			Safe:    "Commit right after the statement that takes ACCESS EXCLUSIVE, or run this statement first"})
	}
	if len(exposed) > 0 {
		p.findings = append(p.findings, rules.Finding{ID: "DBS-NO-LOCK-TIMEOUT", Class: rules.Risk,
			Message: fmt.Sprintf("requests %s without a lock_timeout; while it waits, later queries on the table queue behind it", strings.Join(exposed, ", ")),
			Safe:    "SET lock_timeout = '2s' before it (SET LOCAL inside the transaction) and retry the migration on timeout"})
	}
	for _, q := range eff.Unknown {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s:%d: %s is not in the base schema; its size, rows and dependents are unknown", f.Name, s.Line, q))
	}
	st.Apply(s.Node)
	return p
}

// resolveImpact collects every changed relation and column and looks up
// their dependents.
func resolveImpact(ctx context.Context, b *Base, st *apply.State, work []*pending, rep *Report) error {
	var targets []impact.Target
	add := func(t impact.Target) {
		if b.Model.Relation(t.Relation) == nil {
			return
		}
		if t.Column != "" {
			if r := b.Model.Relation(t.Relation); r.Column(t.Column) == nil {
				return
			}
		}
		if !slices.Contains(targets, t) {
			targets = append(targets, t)
		}
	}
	for _, p := range work {
		for _, f := range p.findings {
			if f.Subject != nil {
				add(impact.Target{Relation: f.Subject.Relation, Column: f.Subject.Column})
			}
		}
	}
	for _, k := range append(slices.Clone(rep.Diff.Dropped), rep.Diff.Altered...) {
		add(impact.Target{Relation: splitKey(k)})
	}
	for _, c := range rep.Diff.Columns {
		if c.Change != "added" {
			add(impact.Target{Relation: splitKey(c.Relation), Column: c.Column})
		}
	}
	var res impact.Result
	if b.Impact != nil {
		r, warnings, err := b.Impact.Impact(ctx, targets)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			rep.Warnings = append(rep.Warnings, "live impact queries failed, so dependents come from the schema model only: "+err.Error())
		} else {
			res = r
			rep.Warnings = append(rep.Warnings, warnings...)
		}
	}
	if res == nil {
		res = impact.FromModel(b.Model, targets)
	}
	for k, v := range res {
		if len(v) > 0 {
			rep.Impact[k] = v
		}
	}
	return nil
}

// rate applies the overrides of docs/MIGRATIONS.md to a statement.
func rate(p *pending, imp map[string][]impact.Item) {
	r := p.rep
	r.Rating = p.band
	for _, f := range p.findings {
		var fr score.Rating
		msg := f.Message
		switch f.Class {
		case rules.Fail, rules.DataLoss:
			fr = score.Critical
		case rules.Breaking:
			fr = score.Medium
			if f.Subject != nil {
				key := f.Subject.Relation.String()
				if f.Subject.Column != "" {
					key += "." + f.Subject.Column
				}
				if deps := imp[key]; len(deps) > 0 {
					fr = score.High
					if !blockedDrop(f, deps) {
						msg += "; depended on by " + describe(deps)
					}
				}
			}
		case rules.Info:
			fr = score.Low
			if f.ID == "DBS-PARSE" {
				fr = score.High
			}
		case rules.Backfill:
			fr = score.Max(p.band, score.Medium)
		default:
			fr = p.band
		}
		if f.Class == rules.DataLoss && f.Subject != nil {
			key := f.Subject.Relation.String()
			if f.Subject.Column != "" {
				key += "." + f.Subject.Column
			}
			if deps := imp[key]; len(deps) > 0 && !blockedDrop(f, deps) {
				msg += "; depended on by " + describe(deps)
			}
		}
		if f.Subject != nil && f.Subject.Restrict {
			key := f.Subject.Relation.String()
			if f.Subject.Column != "" {
				key += "." + f.Subject.Column
			}
			if blockers := blocking(imp[key]); len(blockers) > 0 {
				fr = score.Critical
				msg += "; it will fail without CASCADE because " + describe(blockers) + " depend on it (CASCADE would drop them too)"
			}
		}
		r.Rating = score.Max(r.Rating, fr)
		r.Findings = append(r.Findings, Finding{RuleID: f.ID, Rating: fr, Message: msg, SafeAlternative: f.Safe})
	}
}

// blockedDrop reports whether the finding is a DROP that PostgreSQL will
// refuse; its message then names the blocking dependents instead.
func blockedDrop(f rules.Finding, deps []impact.Item) bool {
	return f.Subject != nil && f.Subject.Restrict && len(blocking(deps)) > 0
}

// blocking keeps the dependents PostgreSQL refuses to drop around without
// CASCADE; functions, triggers and indexes do not block a drop.
func blocking(items []impact.Item) []impact.Item {
	var out []impact.Item
	for _, it := range items {
		switch it.Kind {
		case "view", "matview", "policy", "fk_child":
			if it.Certainty == impact.Catalog {
				out = append(out, it)
			}
		}
	}
	return out
}

func describe(items []impact.Item) string {
	const show = 5
	parts := make([]string, 0, show)
	for i, it := range items {
		if i == show {
			break
		}
		parts = append(parts, strings.ReplaceAll(it.Kind, "_", " ")+" "+it.Name)
	}
	s := strings.Join(parts, ", ")
	if len(items) > show {
		s += fmt.Sprintf(" and %d more", len(items)-show)
	}
	return s
}

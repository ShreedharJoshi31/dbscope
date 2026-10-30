package migrate

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/down"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/parse"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/schema"
)

var defaultCast = regexp.MustCompile(`::[a-z_][a-z0-9_ ."]*(\([0-9, ]+\))?(\[\])?`)

// rollback is one up file's down check, attached after the statements are rated.
type rollback struct {
	file     string
	findings []down.Finding
}

// checkDown replays f and then its down on a copy of the schema as it is
// before f, and compares the result with that schema. Column order is
// ignored: a down that drops and re-adds a column puts it last, which
// PostgreSQL cannot avoid.
func checkDown(st *apply.State, f ingest.File, d *ingest.Down) ([]down.Finding, string) {
	if d.Missing {
		return down.Findings(d, down.Result{}), ""
	}
	before := st.Model.Clone()
	normalizeModel(before)
	chk := apply.New(before, st.Catalog)
	chk.SearchPath = slices.Clone(st.SearchPath)
	var r down.Result
	for _, s := range f.Statements {
		if s.Err != nil {
			r.Unchecked = "it has a statement that cannot be parsed"
			continue
		}
		r.Drops = append(r.Drops, droppedData(chk, before, s.Node)...)
		chk.Apply(s.Node)
	}
	for _, s := range parse.Split(ingest.Normalize(d.Text)).Statements {
		if s.Err != nil {
			r.Unchecked = fmt.Sprintf("its down migration has a statement that cannot be parsed at %s:%d", d.Name, s.Err.Line)
			break
		}
		chk.Apply(s.Node)
	}
	normalizeModel(chk.Model)
	r.Left = append(down.Describe(chk.Diff()), enumChanges(before.Enums, chk.Model.Enums)...)
	warn := ""
	if r.Unchecked != "" {
		warn = fmt.Sprintf("%s: the down migration was not checked because %s", f.Name, r.Unchecked)
	}
	return down.Findings(d, r), warn
}

// droppedData names the tables and columns that existed before the up
// and that n drops with their data. Renames are not drops.
func droppedData(st *apply.State, before *schema.Model, n *pg_query.Node) []string {
	var out []string
	if d := n.GetDropStmt(); d != nil && d.GetRemoveType() == pg_query.ObjectType_OBJECT_TABLE {
		for _, obj := range d.GetObjects() {
			nsp, name := apply.QualifiedFromList(obj.GetList().GetItems())
			q := st.Resolve(&pg_query.RangeVar{Schemaname: nsp, Relname: name})
			if st.Lookup(q) != nil && before.Relation(q) != nil {
				out = append(out, "table "+q.String())
			}
		}
	}
	if a := n.GetAlterTableStmt(); a != nil && a.GetRelation() != nil {
		q := st.Resolve(a.GetRelation())
		b, cur := before.Relation(q), st.Lookup(q)
		if b == nil || cur == nil {
			return out
		}
		for _, c := range a.GetCmds() {
			cmd := c.GetAlterTableCmd()
			if cmd.GetSubtype() != pg_query.AlterTableType_AT_DropColumn || cur.Column(cmd.GetName()) == nil {
				continue
			}
			if orig := st.OriginalColumn(q, cmd.GetName()); b.Column(orig) != nil {
				out = append(out, "column "+q.String()+"."+orig)
			}
		}
	}
	return out
}

// enumChanges describes enums that differ between before and after.
func enumChanges(before, after []schema.Enum) []string {
	var out []string
	find := func(es []schema.Enum, e schema.Enum) *schema.Enum {
		for i := range es {
			if es[i].Schema == e.Schema && es[i].Name == e.Name {
				return &es[i]
			}
		}
		return nil
	}
	for _, e := range after {
		switch b := find(before, e); {
		case b == nil:
			out = append(out, "type "+e.Schema+"."+e.Name+" is still there")
		case !slices.Equal(b.Values, e.Values):
			out = append(out, "type "+e.Schema+"."+e.Name+" has different values")
		}
	}
	for _, e := range before {
		if find(after, e) == nil {
			out = append(out, "type "+e.Schema+"."+e.Name+" is not recreated")
		}
	}
	return out
}

// normalizeModel sorts what a down legitimately recreates in another order
// and drops casts from defaults: the catalog shows 'a'::character varying
// where the statement said 'a'.
func normalizeModel(m *schema.Model) {
	for i := range m.Relations {
		r := &m.Relations[i]
		for j := range r.Columns {
			c := &r.Columns[j]
			c.Default = strings.TrimSpace(defaultCast.ReplaceAllString(c.Default, ""))
			for len(c.Default) > 1 && c.Default[0] == '(' && c.Default[len(c.Default)-1] == ')' {
				c.Default = c.Default[1 : len(c.Default)-1]
			}
		}
		for j := range r.Indexes {
			if len(r.Indexes[j].Include) == 0 {
				r.Indexes[j].Include = nil // the catalog gives [], a replayed index nil
			}
		}
		slices.SortStableFunc(r.Columns, func(a, b schema.Column) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortStableFunc(r.Indexes, func(a, b schema.Index) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortStableFunc(r.ForeignKeys, func(a, b schema.ForeignKey) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortStableFunc(r.Checks, func(a, b schema.Check) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortStableFunc(r.Uniques, func(a, b []string) int { return cmp.Compare(strings.Join(a, ","), strings.Join(b, ",")) })
	}
}

// AttachDown adds rollback findings to the first statement of file and
// raises the statement's and the report's rating. With no statement in the
// file, the findings become warnings.
func AttachDown(rep *Report, file string, fs []down.Finding) {
	for i := range rep.Statements {
		s := &rep.Statements[i]
		if s.File != file {
			continue
		}
		for _, f := range fs {
			s.Findings = append(s.Findings, Finding{RuleID: f.RuleID, Rating: f.Rating, Message: f.Message, SafeAlternative: f.Safe})
			s.Rating = score.Max(s.Rating, f.Rating)
			rep.Rating = score.Max(rep.Rating, f.Rating)
		}
		return
	}
	for _, f := range fs {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: %s %s", file, f.RuleID, f.Message))
	}
}

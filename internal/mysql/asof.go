package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

// NoTimeTravel is the refusal on MySQL, which has no system versioning (measured on 8.4, docs/TIME.md).
const NoTimeTravel = "MySQL has no system-versioned tables, so it cannot read as of a past time"

var dialect = timetravel.Dialect{
	Ident:     quotedName,
	NotTables: map[string]bool{"dual": true},
	AfterName: map[string]bool{"partition": true},
	EndsFrom:  map[string]bool{"limit": true},
	// In a WITH without RECURSIVE, a query's own name in its body is a table.
	SelfNeedsRecursive: true,
}

// quotedName reads a `backquoted` name. A "double-quoted" one is a string
// unless ANSI_QUOTES is on, so it is never taken for a table.
func quotedName(s string) (string, bool) {
	if len(s) < 2 || s[0] != '`' || s[len(s)-1] != '`' {
		return "", false
	}
	return strings.ReplaceAll(s[1:len(s)-1], "``", "`"), true
}

// AsOfRefs finds the base tables a SELECT reads (internal/timetravel).
// Executable comments are refused: whether the server runs one depends on
// its version, so the tables inside cannot be accounted for.
func AsOfRefs(sqlText string) ([]timetravel.Ref, error) {
	toks := Tokens(sqlText)
	tt := make([]timetravel.Token, len(toks))
	prev := 0
	for i, t := range toks {
		if gap := sqlText[prev:t.Pos]; strings.Contains(gap, "/*!") || strings.Contains(gap, "/*M!") {
			return nil, timetravel.Refuse("Remove the /*! ... */ comment to read as of a past time")
		}
		prev = t.Pos + len(t.Text)
		tt[i] = timetravel.Token{Text: t.Text, Word: t.Word, Pos: t.Pos}
	}
	if gap := sqlText[prev:]; strings.Contains(gap, "/*!") || strings.Contains(gap, "/*M!") {
		return nil, timetravel.Refuse("Remove the /*! ... */ comment to read as of a past time")
	}
	return timetravel.Refs(tt, dialect)
}

// AsOfOn rewrites sqlText to read as of at on db's server. It refuses MySQL,
// a sql_mode under which Tokens would read quotes differently from the server
// (ANSI_QUOTES, NO_BACKSLASH_ESCAPES), and an instant the session time zone
// cannot map one to one, such as one in the repeated hour when a zone's clocks go back.
func AsOfOn(ctx context.Context, db *sql.DB, sqlText string, at time.Time) (string, error) {
	srv, err := Describe(ctx, db)
	if err != nil {
		return "", err
	}
	if srv.Flavor != "mariadb" {
		return "", timetravel.Refuse(NoTimeTravel)
	}
	if at.Unix() >= 1 {
		epoch := strings.TrimSuffix(strings.TrimPrefix(AsOfClause(at), "FOR SYSTEM_TIME AS OF FROM_UNIXTIME("), ")")
		var mode, back string
		// epoch is digits built above, never user text.
		if err := db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode, CAST(UNIX_TIMESTAMP(FROM_UNIXTIME("+epoch+")) AS CHAR)").Scan(&mode, &back); err != nil {
			return "", err
		}
		for _, m := range []string{"ANSI_QUOTES", "NO_BACKSLASH_ESCAPES"} {
			if strings.Contains(mode, m) {
				return "", timetravel.Refuse("The server's sql_mode includes %s, which changes how quotes are read; dbscope cannot read as of a past time with it", m)
			}
		}
		if back != epoch {
			return "", timetravel.Refuse("The session time zone gives that time twice (clocks going back); choose a time outside that hour")
		}
	}
	return AsOf(sqlText, at, Resolver(ctx, db))
}

// AsOfClause is the system-versioning clause for an instant. FROM_UNIXTIME
// turns the epoch into the session time zone, the zone AS OF then reads its
// argument in, so the instant holds under any session time zone.
func AsOfClause(at time.Time) string {
	us := at.UnixMicro()
	return fmt.Sprintf("FOR SYSTEM_TIME AS OF FROM_UNIXTIME(%d.%06d)", us/1e6, us%1e6)
}

// AsOf rewrites a SELECT to read every table as of at. Each must be a
// system-versioned table; a view is refused because it can mix versioned
// and current tables.
func AsOf(sqlText string, at time.Time, resolve func([]timetravel.Ref) ([]timetravel.Table, error)) (string, error) {
	if at.Unix() < 1 {
		return "", timetravel.Refuse("MariaDB cannot read as of a time before 1970-01-01T00:00:01Z")
	}
	refs, err := AsOfRefs(sqlText)
	if err != nil {
		return "", err
	}
	tables, err := resolve(refs)
	if err != nil {
		return "", err
	}
	return timetravel.Rewrite(sqlText, tables, AsOfClause(at), allowed)
}

func allowed(r *schema.Relation) string {
	switch {
	case r.Kind == schema.KindView:
		return "is a view, which can mix versioned and current tables; query its tables instead"
	case r.TimeTravel == nil || r.TimeTravel.Kind != schema.TimeTravelSystemVersioned:
		return "is not a system-versioned table"
	}
	return ""
}

// Resolver resolves references against the catalog as this user sees it,
// and refuses on MySQL.
func Resolver(ctx context.Context, db *sql.DB) func([]timetravel.Ref) ([]timetravel.Table, error) {
	return func(refs []timetravel.Ref) ([]timetravel.Table, error) { return resolve(ctx, db, refs) }
}

// resolve finds the table or view each reference names; an unqualified name
// is in the connection's default database, as the query will read it.
func resolve(ctx context.Context, db *sql.DB, refs []timetravel.Ref) ([]timetravel.Table, error) {
	srv, err := Describe(ctx, db)
	if err != nil {
		return nil, err
	}
	if srv.Flavor != "mariadb" {
		return nil, timetravel.Refuse(NoTimeTravel)
	}
	tables := make([]timetravel.Table, len(refs))
	if len(refs) == 0 {
		return tables, nil
	}
	var current sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&current); err != nil {
		return nil, err
	}
	names := make([]schema.QualifiedName, len(refs))
	var conds []string
	var args []any
	for i, r := range refs {
		tables[i].Ref = r
		switch {
		case len(r.Parts) > 2:
			tables[i].Why = "has more than two name parts"
			continue
		case len(r.Parts) == 1 && !current.Valid:
			tables[i].Why = "has no database and the connection has no default database"
			continue
		case len(r.Parts) == 1:
			names[i] = schema.QualifiedName{Schema: current.String, Name: r.Parts[0].Name}
		default:
			names[i] = schema.QualifiedName{Schema: r.Parts[0].Name, Name: r.Parts[1].Name}
		}
		conds = append(conds, "(TABLE_SCHEMA = ? AND TABLE_NAME = ?)")
		args = append(args, names[i].Schema, names[i].Name)
	}
	if len(conds) == 0 {
		return tables, nil
	}
	// information_schema compares names without case; an exact match wins where both spellings exist.
	type found struct {
		rel   schema.Relation
		exact bool
	}
	byKey := map[schema.QualifiedName][]schema.Relation{}
	rows, err := db.QueryContext(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE `+
		strings.Join(conds, " OR "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x schema.Relation
		var typ string
		if err := rows.Scan(&x.Schema, &x.Name, &typ); err != nil {
			return nil, err
		}
		x.Kind = schema.KindTable
		switch typ {
		case "VIEW", "SYSTEM VIEW":
			x.Kind = schema.KindView
		case "SYSTEM VERSIONED":
			x.TimeTravel = &schema.TimeTravel{Kind: schema.TimeTravelSystemVersioned, Available: true}
		}
		k := schema.QualifiedName{Schema: strings.ToLower(x.Schema), Name: strings.ToLower(x.Name)}
		byKey[k] = append(byKey[k], x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range tables {
		if tables[i].Why != "" {
			continue
		}
		q := names[i]
		cands := byKey[schema.QualifiedName{Schema: strings.ToLower(q.Schema), Name: strings.ToLower(q.Name)}]
		var pick *found
		for _, c := range cands {
			f := found{rel: c, exact: c.Schema == q.Schema && c.Name == q.Name}
			if pick == nil || f.exact {
				pick = &f
			}
		}
		if pick == nil {
			tables[i].Why = "is not a table or view this user can see"
			continue
		}
		tables[i].Rel = &pick.rel
	}
	return tables, nil
}

// Bounds says how far back each table can be read. MariaDB keeps no
// retention setting to bound it by.
func Bounds(ctx context.Context, db *sql.DB, names []schema.QualifiedName) (*timetravel.Bounds, error) {
	tables, err := resolve(ctx, db, timetravel.ExactRefs(names))
	if err != nil {
		return nil, err
	}
	b := &timetravel.Bounds{Tables: make([]timetravel.TableBounds, len(tables)),
		Note: "MariaDB sets no lower limit: a time before a table's history began, a dropped history partition, " +
			"or history removed with DELETE HISTORY all read as empty, not as an error."}
	var now string
	if err := db.QueryRowContext(ctx, "SELECT DATE_FORMAT(UTC_TIMESTAMP(6), '%Y-%m-%dT%H:%i:%s.%fZ')").Scan(&now); err != nil {
		return nil, err
	}
	if b.Now, err = time.Parse(time.RFC3339Nano, now); err != nil {
		return nil, err
	}
	for i, t := range tables {
		tb := timetravel.TableBounds{Table: names[i].Schema + "." + names[i].Name, Kind: timetravel.Kind(t.Rel), Source: timetravel.SourceNone}
		switch {
		case t.Rel == nil:
			tb.Note = "This name " + t.Why
		case allowed(t.Rel) != "":
			tb.Note = "This table " + allowed(t.Rel)
		default:
			tb.Available = true
			tb.Note = "History is kept until removed; removed history reads as empty"
		}
		b.Tables[i] = tb
	}
	return b, nil
}

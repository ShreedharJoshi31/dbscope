package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mssql"
	"github.com/dbscope/dbscope/internal/mysql"
	"github.com/dbscope/dbscope/internal/oracle"
	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/timetravel"
)

// asOfSource is a source that reads tables as of a past time (docs/TIME.md).
type asOfSource interface {
	// AsOf rewrites sqlText to read every table as of at, or refuses with a *timetravel.Error.
	AsOf(ctx context.Context, p conn.Profile, password, sqlText string, at time.Time) (string, error)
	TimeTravelBounds(ctx context.Context, p conn.Profile, password string, tables []schema.QualifiedName) (*timetravel.Bounds, error)
}

// asOfChecks are the guards an as-of statement must pass before it is rewritten, so
// that a statement the guard refuses gets the same refusal with or without asOf.
var asOfChecks = map[conn.Engine]func(string) error{
	conn.EngineOracle: oracle.Check, conn.EngineSQLServer: mssql.Check, conn.EngineMySQL: mysql.Check,
}

// parseAsOf reads an RFC 3339 instant with an offset or Z, refusing one more
// than a second after now.
func parseAsOf(s string, now time.Time) (time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, errors.New("asOf must be an RFC 3339 time with an offset or Z, such as 2026-11-15T09:30:00Z")
	}
	if at.After(now.Add(time.Second)) {
		return time.Time{}, fmt.Errorf("asOf %s is in the future; choose a time up to now", s)
	}
	return at.UTC(), nil
}

// rewriteAsOf checks the statement as written, then rewrites it to read as
// of at. The caller checks the rewritten text again before running it.
func (s *server) rewriteAsOf(ctx context.Context, p conn.Profile, password, sqlText string, at time.Time) (string, *runError) {
	check := asOfChecks[p.Engine]
	src, _ := s.source.(engineSource)[p.Engine].(asOfSource)
	if check == nil {
		return "", mapRunError(ctx, errUnsupported)
	}
	if err := check(sqlText); err != nil {
		return "", mapRunError(ctx, err)
	}
	if src == nil {
		return "", mapRunError(ctx, errUnsupported)
	}
	out, err := src.AsOf(ctx, p, password, sqlText, at)
	if err != nil {
		return "", mapRunError(ctx, err)
	}
	return out, nil
}

// asOfRunError explains the errors a read as of a past time can end in.
func asOfRunError(r *runError) {
	switch r.histCode {
	case "ora_1555", "ora_8180", "ora_1466":
		r.message = fmt.Sprintf("This table cannot be read that far back (ORA-%05s): choose a later time", r.histCode[4:])
	case "ora_41900":
		r.message = "Blocked: reading as of a past time needs the FLASHBACK privilege on every table and view the query reads, " +
			"or FLASHBACK ANY TABLE (" + r.message + ")"
	case "ora_8186":
		r.message = "The time is in the future by the database's clock (ORA-08186): choose an earlier time"
	}
}

// timeTravelBounds says how far back each named table can be read.
func (s *server) timeTravelBounds(w http.ResponseWriter, r *http.Request) {
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.TimeTravel, "Reading as of a past time") {
		return
	}
	names, err := timetravel.ParseTables(r.URL.Query().Get("tables"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	src, _ := s.source.(engineSource)[p.Engine].(asOfSource)
	if src == nil {
		s.upstreamError(w, r, errUnsupported)
		return
	}
	b, err := src.TimeTravelBounds(r.Context(), p, password, names)
	var ttErr *timetravel.Error
	switch {
	case errors.As(err, &ttErr):
		writeError(w, http.StatusBadRequest, "validation", ttErr.Message)
	case err != nil:
		s.upstreamError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, b)
	}
}

func (ms *oracleSource) AsOf(ctx context.Context, p conn.Profile, password, sqlText string, at time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return "", err
	}
	return oracle.AsOf(sqlText, at, oracle.Resolver(ctx, db))
}

func (ms *oracleSource) TimeTravelBounds(ctx context.Context, p conn.Profile, password string, tables []schema.QualifiedName) (*timetravel.Bounds, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return oracle.Bounds(ctx, db, tables)
}

func (ms *mssqlSource) AsOf(ctx context.Context, p conn.Profile, password, sqlText string, at time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return "", err
	}
	return mssql.AsOf(sqlText, at, mssql.Resolver(ctx, db))
}

func (ms *mssqlSource) TimeTravelBounds(ctx context.Context, p conn.Profile, password string, tables []schema.QualifiedName) (*timetravel.Bounds, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mssql.Bounds(ctx, db, tables)
}

func (ms *mysqlSource) AsOf(ctx context.Context, p conn.Profile, password, sqlText string, at time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return "", err
	}
	return mysql.AsOfOn(ctx, db, sqlText, at)
}

func (ms *mysqlSource) TimeTravelBounds(ctx context.Context, p conn.Profile, password string, tables []schema.QualifiedName) (*timetravel.Bounds, error) {
	ctx, cancel := context.WithTimeout(ctx, statementTimeout(p)+connectSlack)
	defer cancel()
	db, err := ms.get(p, password)
	if err != nil {
		return nil, err
	}
	return mysql.Bounds(ctx, db, tables)
}

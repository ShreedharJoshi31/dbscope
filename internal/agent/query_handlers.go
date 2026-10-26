package agent

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/query/exec"
	qguard "github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/store"
)

const (
	defaultMaxRows = 500
	maxMaxRows     = 5000
	defaultHistory = 50
)

type queryBody struct {
	SQL         string `json:"sql"`
	MaxRows     *int   `json:"maxRows"`
	Analyze     bool   `json:"analyze"`
	ConfirmCost bool   `json:"confirmCost"`
}

// runError is a failed run as the API reports it: the envelope plus the
// Phase 2 extras, and how the history records it.
type runError struct {
	status   int
	code     string
	message  string
	detail   string
	sqlstate string
	position int
	cost     *exec.CostError
	// history status and errorCode
	histStatus string
	histCode   string
}

func (e *runError) write(w http.ResponseWriter) {
	body := map[string]any{"code": e.code, "message": e.message}
	if e.detail != "" {
		body["detail"] = e.detail
	}
	if e.sqlstate != "" {
		body["sqlstate"] = e.sqlstate
	}
	if e.position > 0 {
		body["position"] = e.position
	}
	if e.cost != nil {
		body["estimatedCost"] = e.cost.EstimatedCost
		body["estimatedRows"] = e.cost.EstimatedRows
	}
	writeJSON(w, e.status, map[string]any{"error": body})
}

func (s *server) runQuery(w http.ResponseWriter, r *http.Request) {
	var body queryBody
	if !decode(w, r, &body) {
		return
	}
	maxRows := defaultMaxRows
	if body.MaxRows != nil {
		maxRows = *body.MaxRows
	}
	if maxRows < 1 || maxRows > maxMaxRows {
		writeError(w, http.StatusBadRequest, "validation", "maxRows must be 1-5000")
		return
	}
	s.run(w, r, body, maxRows, false)
}

func (s *server) runExplain(w http.ResponseWriter, r *http.Request) {
	var body queryBody
	if !decode(w, r, &body) {
		return
	}
	if body.MaxRows != nil {
		writeError(w, http.StatusBadRequest, "validation", "maxRows does not apply to explain")
		return
	}
	s.run(w, r, body, 0, true)
}

func (s *server) run(w http.ResponseWriter, r *http.Request, body queryBody, maxRows int, explainOnly bool) {
	if strings.TrimSpace(body.SQL) == "" {
		writeError(w, http.StatusBadRequest, "validation", "sql is required")
		return
	}
	p, password, ok := s.profileWithSecret(w, r)
	if !ok {
		return
	}
	start := time.Now()
	res, rerr := s.execute(r.Context(), p, password, body, maxRows, explainOnly)
	if !explainOnly {
		s.recordHistory(r, p.ID, body.SQL, start, res, rerr)
	}
	if rerr != nil {
		if rerr.histStatus == store.HistoryCancelled {
			return // the client is gone; nobody reads the response
		}
		if rerr.status == http.StatusInternalServerError {
			s.log.Error("query failed", "path", r.URL.Path, "err", rerr.message)
			rerr.message = "internal error"
		}
		rerr.write(w)
		return
	}
	if explainOnly {
		writeJSON(w, http.StatusOK, map[string]any{
			"executedSql": res.ExecutedSQL, "plan": res.Plan, "overlay": res.Overlay, "warnings": res.Warnings,
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) execute(ctx context.Context, p conn.Profile, password string, body queryBody, maxRows int, explainOnly bool) (*exec.Result, *runError) {
	parsed, err := qguard.Check(body.SQL)
	if err != nil {
		return nil, mapRunError(ctx, err)
	}
	res, err := s.source.Query(ctx, p, password, exec.Request{
		Parsed: parsed, Schemas: p.Schemas, StatementTimeout: statementTimeout(p),
		MaxRows: maxRows, ExplainOnly: explainOnly, Analyze: body.Analyze,
		MaxCost: p.CostLimit(), ConfirmCost: body.ConfirmCost,
	})
	if err != nil {
		return nil, mapRunError(ctx, err)
	}
	return res, nil
}

// recordHistory stores the run even when the client went away.
func (s *server) recordHistory(r *http.Request, profileID, sql string, start time.Time, res *exec.Result, rerr *runError) {
	e := store.HistoryEntry{
		SQL: sql, StartedAt: start, Status: store.HistoryOK,
		DurationMs: math.Round(float64(time.Since(start).Microseconds())/10) / 100,
	}
	if rerr != nil {
		e.Status, e.ErrorCode = rerr.histStatus, rerr.histCode
	} else {
		e.RowCount = res.RowCount
	}
	if _, err := s.store.AddHistory(context.WithoutCancel(r.Context()), profileID, e); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("record query history", "id", profileID, "err", err)
	}
}

// mapRunError turns a guard, exec or pgx error into the API response.
// ctx is the request context: a cancelled request reports "cancelled"
// whatever error the cancellation produced.
func mapRunError(ctx context.Context, err error) *runError {
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// Never written: the client is gone.
		return &runError{histStatus: store.HistoryCancelled, histCode: "cancelled"}
	}
	var ge *qguard.Error
	var ce *exec.CostError
	var de *exec.DBError
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &ge):
		return &runError{status: http.StatusUnprocessableEntity, code: "rejected", message: ge.Message, detail: string(ge.Reason),
			position: ge.Position, histStatus: store.HistoryRejected, histCode: string(ge.Reason)}
	case errors.As(err, &ce):
		return &runError{status: http.StatusConflict, code: "cost_confirmation_required",
			message: ce.Error() + "; re-send with confirmCost to run it anyway", cost: ce,
			histStatus: store.HistoryRejected, histCode: "cost_confirmation_required"}
	case errors.Is(err, errClosed):
		return &runError{status: http.StatusServiceUnavailable, code: "internal", message: err.Error(),
			histStatus: store.HistoryError, histCode: "internal"}
	case !errors.Is(err, exec.ErrConnect) && errors.As(err, &pgErr):
		e := databaseError(pgErr)
		if errors.As(err, &de) && e.status == http.StatusBadRequest {
			e.position = de.Position
		}
		return e
	case !errors.Is(err, exec.ErrConnect) && errors.Is(err, context.DeadlineExceeded):
		return &runError{status: http.StatusGatewayTimeout, code: "timeout",
			message:    "the query did not finish within the profile's statement timeout",
			histStatus: store.HistoryError, histCode: "timeout"}
	}
	te := conn.Classify(err)
	if te.Kind == "other" && !errors.Is(err, exec.ErrConnect) {
		return &runError{status: http.StatusInternalServerError, code: "internal", message: err.Error(),
			histStatus: store.HistoryError, histCode: "internal"}
	}
	return &runError{status: http.StatusBadGateway, code: "upstream", message: te.Message, detail: te.Kind,
		sqlstate: te.SQLState, histStatus: store.HistoryError, histCode: "upstream"}
}

// databaseError maps PostgreSQL errors per docs/QUERY-SAFETY.md.
func databaseError(pgErr *pgconn.PgError) *runError {
	e := &runError{status: http.StatusBadRequest, code: "database", message: pgErr.Message, sqlstate: pgErr.Code,
		histStatus: store.HistoryError, histCode: pgErr.Code}
	switch {
	case pgErr.Code == "57014":
		e.status, e.code = http.StatusGatewayTimeout, "timeout"
		e.message = "Stopped after the profile's statement timeout"
	case pgErr.Code == "40001" && strings.Contains(pgErr.Message, "conflict with recovery"):
		e.status, e.code = http.StatusServiceUnavailable, "recovery_conflict"
		e.message = "The replica removed rows this query still needed (conflict with recovery). " +
			"It is safe to retry once; if it keeps happening, ask the DBA about hot_standby_feedback."
	case pgErr.Code == "25006":
		e.message = "Blocked: this would modify data (" + pgErr.Message + ")"
	case pgErr.Code == "25P03" || pgErr.Code == "25P04":
		e.status, e.code = http.StatusGatewayTimeout, "timeout"
		e.message = "The database ended the session after a transaction timeout (" + pgErr.Message + "); run the query again to reconnect"
	case pgErr.Code == "57P04":
		e.status, e.code, e.detail = http.StatusBadGateway, "upstream", "database"
		e.message = "The database was dropped or moved on the primary (" + pgErr.Message + "); do not retry"
	case pgErr.Severity == "FATAL" || pgErr.Severity == "PANIC" || strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57P"):
		e.status, e.code, e.detail = http.StatusBadGateway, "upstream", "database"
	}
	return e
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	limit := defaultHistory
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxHistory {
			writeError(w, http.StatusBadRequest, "validation", "limit must be 1-200")
			return
		}
		limit = n
	}
	entries, err := s.store.ListHistory(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

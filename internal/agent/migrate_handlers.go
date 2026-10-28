package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/sample"
)

const (
	maxMigrationFiles     = 200
	maxMigrationFileBytes = 1 << 20
	// maxMigrationBody allows many files of up to 1 MiB each in one request.
	maxMigrationBody = 32 << 20
)

type analyzeBody struct {
	Files []struct {
		Name string `json:"name"`
		SQL  string `json:"sql"`
	} `json:"files"`
	TxMode            string `json:"txMode"`
	AssumeLockTimeout bool   `json:"assumeLockTimeout"`
}

// analyzeRequest decodes and validates the body, writing the error itself.
func analyzeRequest(w http.ResponseWriter, r *http.Request) (migrate.Request, bool) {
	var body analyzeBody
	if !decodeLimit(w, r, &body, maxMigrationBody) {
		return migrate.Request{}, false
	}
	bad := func(msg string) (migrate.Request, bool) {
		writeError(w, http.StatusBadRequest, "validation", msg)
		return migrate.Request{}, false
	}
	if len(body.Files) == 0 || len(body.Files) > maxMigrationFiles {
		return bad("files must hold 1-200 files")
	}
	mode, err := ingest.ParseMode(body.TxMode)
	if err != nil {
		return bad(err.Error())
	}
	req := migrate.Request{TxMode: mode, AssumeLockTimeout: body.AssumeLockTimeout, KeepOrder: true}
	for i, f := range body.Files {
		switch {
		case strings.TrimSpace(f.Name) == "" || len(f.Name) > 512 || strings.ContainsRune(f.Name, 0):
			return bad(fmt.Sprintf("files[%d].name must be 1-512 characters", i))
		case len(f.SQL) > maxMigrationFileBytes:
			return bad(fmt.Sprintf("files[%d] (%s) is larger than 1 MiB", i, f.Name))
		}
		req.Files = append(req.Files, ingest.Input{Name: f.Name, SQL: f.SQL})
	}
	return req, true
}

func (s *server) analyzeMigrations(w http.ResponseWriter, r *http.Request) {
	req, ok := analyzeRequest(w, r)
	if !ok {
		return
	}
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.Migrations, "Migration analysis") {
		return
	}
	// Introspection, sizes and impact queries can each take a statement timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 3*statementTimeout(p)+connectSlack)
	defer cancel()
	if p.Engine == conn.EngineMySQL {
		src, _ := s.source.(engineSource)[conn.EngineMySQL].(*mysqlSource)
		if src == nil {
			s.upstreamError(w, r, errUnsupported)
			return
		}
		rep, err := src.Analyze(ctx, p, password, req.Files)
		if err != nil {
			s.upstreamError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
		return
	}
	if p.Engine == conn.EngineClickHouse {
		src, _ := s.source.(engineSource)[conn.EngineClickHouse].(*clickhouseSource)
		if src == nil {
			s.upstreamError(w, r, errUnsupported)
			return
		}
		rep, err := src.Analyze(ctx, p, password, req.Files)
		if err != nil {
			s.upstreamError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
		return
	}
	if p.Engine == conn.EngineSQLite {
		rep, err := sqliteSource{}.Analyze(ctx, p, req.Files)
		if err != nil {
			s.upstreamError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
		return
	}
	base, err := s.source.MigrationBase(ctx, p, password)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	rep, err := migrate.Analyze(ctx, base, req)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *server) analyzeSampleMigrations(w http.ResponseWriter, r *http.Request) {
	req, ok := analyzeRequest(w, r)
	if !ok {
		return
	}
	m, err := sample.Model()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rep, err := migrate.Analyze(r.Context(), migrate.SampleBase(m), req)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (ps *pools) MigrationBase(ctx context.Context, p conn.Profile, password string) (*migrate.Base, error) {
	pool, err := ps.get(ctx, p, password)
	if err != nil {
		return nil, err
	}
	return migrate.LiveBase(ctx, pool, p.Schemas, statementTimeout(p))
}

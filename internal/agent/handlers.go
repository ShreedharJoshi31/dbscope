package agent

import (
	"context"
	"errors"
	"net/http"
	"runtime"

	"github.com/dbscope/dbscope/internal/cassandra"
	"github.com/dbscope/dbscope/internal/clickhouse"
	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/dynamo"
	"github.com/dbscope/dbscope/internal/elastic"
	"github.com/dbscope/dbscope/internal/mongo"
	"github.com/dbscope/dbscope/internal/mssql"
	"github.com/dbscope/dbscope/internal/mysql"
	"github.com/dbscope/dbscope/internal/neo4j"
	"github.com/dbscope/dbscope/internal/redis"
	"github.com/dbscope/dbscope/internal/secrets"
	"github.com/dbscope/dbscope/internal/sqlite"
	"github.com/dbscope/dbscope/internal/store"
)

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": Version, "os": runtime.GOOS, "arch": runtime.GOARCH})
}

func (s *server) listProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProfiles(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

// profileInput decodes and validates a profile body, writing the error
// response itself when it returns ok=false.
func profileInput(w http.ResponseWriter, r *http.Request) (conn.ProfileInput, conn.Profile, bool) {
	var in conn.ProfileInput
	if !decode(w, r, &in) {
		return in, conn.Profile{}, false
	}
	p, err := in.Profile()
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return in, conn.Profile{}, false
	}
	return in, p, true
}

func (s *server) locked(w http.ResponseWriter) bool {
	if s.vault.Status().Locked {
		writeLocked(w)
		return true
	}
	return false
}

func writeLocked(w http.ResponseWriter) {
	writeError(w, http.StatusLocked, "secrets_locked", "unlock the secrets vault first")
}

func (s *server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "profile not found")
	case errors.Is(err, store.ErrNameTaken):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *server) createProfile(w http.ResponseWriter, r *http.Request) {
	in, p, ok := profileInput(w, r)
	// Only a password needs the vault; a SQLite file or a passwordless role does not.
	p.HasPassword = in.Password != nil && *in.Password != ""
	if !ok || (p.HasPassword && s.locked(w)) {
		return
	}
	p, err := s.store.CreateProfile(r.Context(), p)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if p.HasPassword {
		if err := s.vault.Set(p.ID, *in.Password); err != nil {
			// Don't leave a profile that claims a password it doesn't have.
			if delErr := s.store.DeleteProfile(context.WithoutCancel(r.Context()), p.ID); delErr != nil {
				s.log.Error("roll back profile create", "id", p.ID, "err", delErr)
			}
			s.secretError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) updateProfile(w http.ResponseWriter, r *http.Request) {
	in, p, ok := profileInput(w, r)
	if !ok || (in.Password != nil && s.locked(w)) {
		return
	}
	old, err := s.store.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	p.ID = old.ID
	p.HasPassword = old.HasPassword
	if in.Password != nil {
		p.HasPassword = *in.Password != ""
	}
	// A file-based engine has no password: a switch to one drops the stored secret.
	if p.Engine == conn.EngineSQLite && old.HasPassword {
		if s.locked(w) {
			return
		}
		empty := ""
		in.Password, p.HasPassword = &empty, false
	}
	updated, err := s.store.UpdateProfile(r.Context(), p)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if in.Password != nil {
		if *in.Password == "" {
			err = s.vault.Delete(p.ID)
		} else {
			err = s.vault.Set(p.ID, *in.Password)
		}
		if err != nil {
			if _, rbErr := s.store.UpdateProfile(context.WithoutCancel(r.Context()), old); rbErr != nil {
				s.log.Error("roll back profile update", "id", p.ID, "err", rbErr)
			}
			s.secretError(w, r, err)
			return
		}
	}
	s.source.Forget(p.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	// A profile without a password can be deleted while the vault is locked.
	if err := s.vault.Delete(p.ID); err != nil && !(errors.Is(err, secrets.ErrLocked) && !p.HasPassword) {
		s.secretError(w, r, err)
		return
	}
	if err := s.store.DeleteProfile(r.Context(), p.ID); err != nil {
		s.storeError(w, r, err)
		return
	}
	s.source.Forget(p.ID)
	s.convs.Forget(p.ID)
	s.rowsConfirmed.Delete(p.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) secretError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, secrets.ErrLocked) {
		writeLocked(w)
		return
	}
	s.internalError(w, r, err)
}

func (s *server) parseURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.URL == "" {
		writeError(w, http.StatusBadRequest, "validation", "url is required")
		return
	}
	parsed, err := conn.ParseURL(body.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, parsed)
}

func (s *server) testUnsaved(w http.ResponseWriter, r *http.Request) {
	in, p, ok := profileInput(w, r)
	if !ok {
		return
	}
	var password string
	if in.Password != nil {
		password = *in.Password
	}
	writeJSON(w, http.StatusOK, testProfile(r.Context(), p, password))
}

func (s *server) testSaved(w http.ResponseWriter, r *http.Request) {
	p, password, ok := s.profileWithSecret(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, testProfile(r.Context(), p, password))
}

func testProfile(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	switch p.Engine {
	case conn.EngineSQLite:
		return sqlite.Test(ctx, p.Path)
	case conn.EngineMySQL:
		return mysql.Test(ctx, p, password)
	case conn.EngineMongoDB:
		return mongo.Test(ctx, p, password)
	case conn.EngineRedis:
		return redis.Test(ctx, p, password)
	case conn.EngineDynamoDB:
		return dynamo.Test(ctx, p)
	case conn.EngineCassandra:
		return cassandra.Test(ctx, p, password)
	case conn.EngineNeo4j:
		return neo4j.Test(ctx, p, password)
	case conn.EngineElasticsearch:
		return elastic.Test(ctx, p, password)
	case conn.EngineClickHouse:
		return clickhouse.Test(ctx, p, password)
	case conn.EngineSQLServer:
		return mssql.Test(ctx, p, password)
	}
	return conn.Test(ctx, p, password)
}

func (s *server) secretsStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.vault.Status())
}

func (s *server) unlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch err := s.vault.Unlock(body.Passphrase); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, secrets.ErrEmptyPassphrase):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, secrets.ErrBadPassphrase):
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

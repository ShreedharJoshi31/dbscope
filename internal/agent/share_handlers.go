package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/infer"
	"github.com/dbscope/dbscope/internal/share"
	"github.com/dbscope/dbscope/internal/store"
)

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *server) exportShared(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	sh, err := s.store.Shared(r.Context(), p.ID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	name := strings.Trim(unsafeFileChars.ReplaceAllString(p.Name, "-"), "-.")
	if name == "" {
		name = "dbscope"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.dbscope.json"`, name))
	w.Write(share.FromStore(sh).Marshal())
}

func (s *server) previewShared(w http.ResponseWriter, r *http.Request) {
	s.planShared(w, r, false)
}

func (s *server) importShared(w http.ResponseWriter, r *http.Request) {
	s.planShared(w, r, true)
}

// planShared previews, or with apply also performs, an import of the
// shared-state file in the request body.
func (s *server) planShared(w http.ResponseWriter, r *http.Request, apply bool) {
	mode := share.Mode(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = share.Merge
	}
	if mode != share.Merge && mode != share.Replace {
		writeError(w, http.StatusBadRequest, "validation", "mode must be merge or replace")
		return
	}
	p, err := s.store.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, share.MaxBytes))
	if err != nil {
		if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "validation", fmt.Sprintf("the file must be at most %d MiB", share.MaxBytes>>20))
			return
		}
		writeError(w, http.StatusBadRequest, "validation", "could not read the request body")
		return
	}
	incoming, err := share.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	// Orphans are shown only in the preview, and introspecting can take seconds,
	// which an import must not spend holding the store's write lock.
	var ex *share.Existing
	if !apply {
		ex = s.existing(r.Context(), p)
	}
	var preview share.Preview
	plan := func(current store.Shared) (store.Shared, error) {
		var result store.Shared
		preview, result = share.Plan(share.FromStore(current), incoming, mode, ex)
		return result, share.Check(result)
	}
	if apply {
		err = s.store.UpdateShared(r.Context(), p.ID, plan)
	} else {
		var current store.Shared
		if current, err = s.store.Shared(r.Context(), p.ID); err == nil {
			_, err = plan(current)
		}
	}
	var rejected *share.CheckError
	switch {
	case errors.As(err, &rejected):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	case err != nil:
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// existing reads the tables and inferred foreign keys the connection's
// database has, to flag entries that name something it does not. It gives
// up quietly (nil) when the vault is locked or the database is unreachable:
// the import itself never needs the database.
func (s *server) existing(ctx context.Context, p conn.Profile) *share.Existing {
	password, passphrase, err := s.secrets(p)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if p, err = s.tunnels.reach(ctx, p, passphrase); err != nil {
		s.log.Debug("shared-state preview without schema", "err", err)
		return nil
	}
	m, err := s.source.Introspect(ctx, p, password)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Debug("shared-state preview without schema", "err", err)
		}
		return nil
	}
	ex := &share.Existing{Schemas: map[string]bool{}, Tables: map[string]bool{}, InferredFKs: map[string]bool{}}
	for _, sc := range m.Schemas {
		ex.Schemas[sc] = true
	}
	for _, rel := range m.Relations {
		ex.Tables[rel.QName().String()] = true
	}
	for _, c := range infer.Infer(m) {
		ex.InferredFKs[c.FK.Name] = true
	}
	return ex
}

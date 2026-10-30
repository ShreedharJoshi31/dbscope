package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/history"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/sqlite"
)

// maxFolderWalk caps the files a migrations folder walk reads, as the web's walk does (MAX_WALK).
const maxFolderWalk = 5000

// maxFolderBytes bounds what one folder read holds in memory, applied files included.
const maxFolderBytes = 64 << 20

type analyzeFolderBody struct {
	PendingOnly       bool   `json:"pendingOnly"`
	TxMode            string `json:"txMode"`
	AssumeLockTimeout bool   `json:"assumeLockTimeout"`
}

// migrationSource is one file the folder analysis read, for the UI to show.
type migrationSource struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

type folderReport struct {
	Report  *migrate.Report   `json:"report"`
	Sources []migrationSource `json:"sources"`
}

// analyzeMigrationFolder analyzes the files in the profile's migrations
// folder, with pendingOnly leaving out those the migration tool's history
// shows applied.
func (s *server) analyzeMigrationFolder(w http.ResponseWriter, r *http.Request) {
	var body analyzeFolderBody
	if !decode(w, r, &body) {
		return
	}
	mode, err := ingest.ParseMode(body.TxMode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	p, password, ok := s.profileWithSecret(w, r)
	if !ok || !supports(w, p, p.Capabilities.Migrations, "Migration analysis") {
		return
	}
	if p.MigrationsDir == "" {
		writeError(w, http.StatusBadRequest, "validation", "this connection has no migrations folder; set one in its settings")
		return
	}
	files, err := readMigrationFolder(p.MigrationsDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*statementTimeout(p)+connectSlack)
	defer cancel()
	var hist history.Result
	if body.PendingOnly {
		hist = s.migrationHistory(ctx, p, password, files)
		files = hist.Keep(files)
	}
	if len(files) > maxMigrationFiles {
		writeError(w, http.StatusBadRequest, "validation", fmt.Sprintf("%d migration files to analyze; at most %d can be analyzed at once", len(files), maxMigrationFiles))
		return
	}
	if len(files) == 0 && hist.Pending == nil {
		writeError(w, http.StatusBadRequest, "validation", "no migration files in "+p.MigrationsDir)
		return
	}
	if len(files) == 0 {
		engine := string(p.Engine)
		if p.Engine == conn.EnginePostgres {
			engine = "" // as migrate.Report names PostgreSQL
		}
		writeJSON(w, http.StatusOK, folderReport{Report: &migrate.Report{Engine: engine, Rating: score.Low, Statements: []migrate.Statement{},
			Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Base: "live", Pending: hist.Pending,
			Warnings: append(hist.Warnings, "every migration file is applied; there is nothing pending to check")}, Sources: []migrationSource{}})
		return
	}
	// analyzeFor filters its files in place.
	rep := s.analyzeFor(ctx, w, r, p, password, migrate.Request{Files: slices.Clone(files), TxMode: mode, AssumeLockTimeout: body.AssumeLockTimeout, KeepOrder: true})
	if rep == nil {
		return
	}
	rep.Pending = hist.Pending
	rep.Warnings = append(rep.Warnings, hist.Warnings...)
	out := folderReport{Report: rep, Sources: make([]migrationSource, 0, len(files))}
	for _, f := range files {
		out.Sources = append(out.Sources, migrationSource{Name: f.Name, SQL: f.SQL})
	}
	writeJSON(w, http.StatusOK, out)
}

// isMigrationFile is what the web sends from a dropped folder
// (web/src/features/analyze-migration/model/read-files.ts) plus neo4j-migrations' .cypher.
func isMigrationFile(name string) bool {
	name = strings.ToLower(name)
	for _, ext := range []string{".sql", ".cql", ".cypher", ".http", ".js", ".sql.conf"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return name == "atlas.sum"
}

// readMigrationFolder walks dir like the web walks a picked folder: names
// sorted naturally per directory, hidden directories and node_modules
// skipped, and each file named by its path under the folder, prefixed with
// the folder's own name (so prisma/migrations yields migrations/<dir>/migration.sql).
func readMigrationFolder(dir string) ([]ingest.Input, error) {
	var out []ingest.Input
	var total int64
	var walk func(abs, rel string) error
	walk = func(abs, rel string) error {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		names := make([]fs.DirEntry, len(entries))
		copy(names, entries)
		sortNatural(names)
		for _, e := range names {
			child, name := filepath.Join(abs, e.Name()), path.Join(rel, e.Name())
			switch {
			case e.IsDir():
				if strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" {
					continue
				}
				if err := walk(child, name); err != nil {
					return err
				}
			case e.Type().IsRegular() && isMigrationFile(e.Name()):
				if len(out) >= maxFolderWalk {
					return fmt.Errorf("the migrations folder holds more than %d migration files", maxFolderWalk)
				}
				info, err := e.Info()
				if err != nil {
					return err
				}
				if info.Size() > maxMigrationFileBytes {
					return fmt.Errorf("%s is larger than 1 MiB", name)
				}
				if total += info.Size(); total > maxFolderBytes {
					return fmt.Errorf("the migrations folder holds more than %d MiB of migration files", maxFolderBytes>>20)
				}
				b, err := os.ReadFile(child)
				if err != nil {
					return err
				}
				out = append(out, ingest.Input{Name: name, SQL: string(b)})
			}
		}
		return nil
	}
	if err := walk(dir, filepath.Base(dir)); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return nil, fmt.Errorf("could not read the migrations folder: %v", err)
		}
		return nil, err
	}
	return out, nil
}

// sortNatural orders like the web's Intl.Collator("en", {numeric: true, sensitivity: "base"}).
func sortNatural(entries []fs.DirEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && ingest.Natural(strings.ToLower(entries[j].Name()), strings.ToLower(entries[j-1].Name())) < 0; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// migrationHistory reads which files the profile's migration tool has applied.
func (s *server) migrationHistory(ctx context.Context, p conn.Profile, password string, files []ingest.Input) history.Result {
	failed := func(err error) history.Result {
		return history.Result{Warnings: []string{"could not read the migration history: " + conn.Classify(err).Message + "; every file is checked"}}
	}
	src, _ := s.source.(engineSource)
	switch p.Engine {
	case conn.EnginePostgres:
		ps, ok := src[conn.EnginePostgres].(*pools)
		if !ok {
			return failed(errUnsupported)
		}
		pool, err := ps.get(ctx, p, password)
		if err != nil {
			return failed(err)
		}
		return history.SQL(ctx, history.Pgx(pool), history.Postgres, files)
	case conn.EngineSQLite:
		db, err := sqlite.Open(p.Path, statementTimeout(p))
		if err != nil {
			return failed(err)
		}
		defer db.Close()
		return history.SQL(ctx, history.DB(db.DB), history.SQLite, files)
	case conn.EngineMySQL:
		ms, ok := src[conn.EngineMySQL].(*mysqlSource)
		if !ok {
			return failed(errUnsupported)
		}
		db, err := ms.get(p, password)
		if err != nil {
			return failed(err)
		}
		return history.SQL(ctx, history.DB(db), history.MySQL, files)
	case conn.EngineClickHouse:
		cs, ok := src[conn.EngineClickHouse].(*clickhouseSource)
		if !ok {
			return failed(errUnsupported)
		}
		c, err := cs.get(p, password)
		if err != nil {
			return failed(err)
		}
		return history.SQL(ctx, history.ClickHouseConn(c.Conn), history.ClickHouse, files)
	case conn.EngineMongoDB:
		ms, ok := src[conn.EngineMongoDB].(*mongoSource)
		if !ok {
			return failed(errUnsupported)
		}
		c, err := ms.get(p, password)
		if err != nil {
			return failed(err)
		}
		return history.Mongo(ctx, c.Database(p.Database), files)
	case conn.EngineNeo4j:
		ns, ok := src[conn.EngineNeo4j].(*neo4jSource)
		if !ok {
			return failed(errUnsupported)
		}
		c, err := ns.get(p, password)
		if err != nil {
			return failed(err)
		}
		return history.Neo4j(ctx, c.Driver, c.Database, files)
	case conn.EngineElasticsearch:
		es, ok := src[conn.EngineElasticsearch].(*elasticSource)
		if !ok {
			return failed(errUnsupported)
		}
		c, err := es.get(p, password)
		if err != nil {
			return failed(err)
		}
		return history.Elastic(ctx, c, files)
	}
	return history.Result{Warnings: []string{"dbscope reads no migration history for " + string(p.Engine) + "; every file is checked"}}
}

// migrationFolderState answers whether the profile's migrations folder
// changed, for the web's watch mode: a fingerprint of the migration files'
// names, sizes and modification times, read without opening any file.
func (s *server) migrationFolderState(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if p.MigrationsDir == "" {
		writeError(w, http.StatusBadRequest, "validation", "this connection has no migrations folder; set one in its settings")
		return
	}
	fp, n, err := folderFingerprint(p.MigrationsDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp, "files": n})
}

// folderFingerprint walks dir as readMigrationFolder does, hashing each
// migration file's path, size and modification time.
func folderFingerprint(dir string) (string, int, error) {
	h := sha256.New()
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !isMigrationFile(d.Name()) {
			return nil
		}
		if n++; n > maxFolderWalk {
			return fmt.Errorf("the migrations folder holds more than %d migration files", maxFolderWalk)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("could not read the migrations folder: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

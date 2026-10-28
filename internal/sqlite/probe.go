package sqlite

import (
	"context"
	"errors"
	"time"

	"modernc.org/sqlite"

	"github.com/dbscope/dbscope/internal/conn"
)

// Test opens the file read-only and reads its catalog, reporting failures in
// the result the way conn.Test does for PostgreSQL.
func Test(ctx context.Context, path string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineSQLite, Warnings: []string{}}
	start := time.Now()
	db, err := Open(path, 2*time.Second)
	if err != nil {
		kind := "other"
		if se := (*sqlite.Error)(nil); errors.As(err, &se) {
			kind = "database" // e.g. not a database, or a corrupt file
		}
		res.Error = &conn.TestError{Kind: kind, Message: err.Error()}
		return res
	}
	defer db.Close()
	if db.Immutable {
		res.Warnings = append(res.Warnings, "This WAL database is in a folder dbscope cannot write, so it is opened as immutable: "+
			"changes not yet checkpointed from the -wal file are not shown, and a write by another program during a read can give inconsistent results.")
	}
	v, n, err := Version(ctx, db.DB)
	if err != nil {
		res.Error = &conn.TestError{Kind: "database", Message: err.Error()}
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs, res.ServerVersion, res.ServerVersionNum = true, &ms, v, n
	return res
}

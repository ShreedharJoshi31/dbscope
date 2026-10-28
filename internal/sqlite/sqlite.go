// Package sqlite reads the structure of a SQLite database file for the ERD
// (docs/ENGINES.md). Files are opened read-only and query_only, so nothing
// dbscope runs can change them.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DB is an open database file.
type DB struct {
	*sql.DB
	// Immutable is set when the file could only be opened with SQLite's
	// immutable flag: a WAL database whose folder dbscope cannot write, so
	// SQLite cannot create the -shm file readers need. SQLite then ignores the
	// -wal file and assumes nothing else writes while it reads.
	Immutable bool
}

// Open opens an existing database file read-only. A missing file is an
// error rather than a new empty database. Like any SQLite reader, opening a
// WAL database in a writable folder may create its -wal and -shm files.
func Open(path string, busy time.Duration) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := open(path, busy, false)
	if err != nil {
		return nil, err
	}
	// The first read is where SQLite reports a file that is not a database or a WAL index it cannot create.
	_, err = db.Exec(`SELECT count(*) FROM sqlite_schema`)
	var se *sqlite.Error
	if errors.As(err, &se) && (se.Code() == sqlite3.SQLITE_READONLY_DIRECTORY || se.Code() == sqlite3.SQLITE_READONLY_CANTINIT) {
		db.Close()
		if db, err = open(path, busy, true); err != nil {
			return nil, err
		}
		return &DB{DB: db, Immutable: true}, nil
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{DB: db}, nil
}

func open(path string, busy time.Duration, immutable bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path // a Windows drive path: file:///C:/...
	}
	u.RawQuery = fmt.Sprintf("mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(%d)", busy.Milliseconds())
	if immutable {
		u.RawQuery += "&immutable=1"
	}
	c, err := sqlite.NewConnector(u.String())
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(readOnly{c})
	db.SetMaxOpenConns(2)
	return db, nil
}

// readOnly installs authorize on every connection it opens.
type readOnly struct{ driver.Connector }

func (r readOnly) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := r.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	reg, ok := c.(sqlite.AuthorizerRegisterer)
	if !ok {
		c.Close()
		return nil, errors.New("sqlite driver has no authorizer")
	}
	if err := reg.RegisterAuthorizer(authorize); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// authorize lets statements read and nothing else. A read-only open still
// allows ATTACH, which reaches other files, so SQLite itself refuses it here
// along with every write, schema change and extension load.
func authorize(action sqlite.AuthorizerActionCode, arg1, arg2, _, _ string) sqlite.AuthorizerReturnCode {
	switch action {
	case sqlite.AuthSelect, sqlite.AuthRead, sqlite.AuthRecursive, sqlite.AuthTransaction:
		return sqlite.AuthorizerOK
	case sqlite.AuthFunction:
		if strings.EqualFold(arg2, "load_extension") {
			return sqlite.AuthorizerDeny
		}
		return sqlite.AuthorizerOK
	case sqlite.AuthPragma:
		// Table-valued pragmas (pragma_table_xinfo) read the catalog; settings that
		// would lift query_only or edit the schema text are refused.
		switch strings.ToLower(arg1) {
		case "query_only", "writable_schema", "trusted_schema":
			if arg2 != "" {
				return sqlite.AuthorizerDeny
			}
		}
		return sqlite.AuthorizerOK
	}
	return sqlite.AuthorizerDeny
}

// Version returns sqlite_version() and its SQLITE_VERSION_NUMBER form, e.g. "3.46.1" and 3046001.
func Version(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (string, int, error) {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&v); err != nil {
		return "", 0, err
	}
	var major, minor, patch int
	fmt.Sscanf(v, "%d.%d.%d", &major, &minor, &patch)
	return v, major*1_000_000 + minor*1_000 + patch, nil
}

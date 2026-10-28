# Database engines

dbscope started as a PostgreSQL tool and kept "Postgres only, no driver
abstraction" as a fixed decision (D5) through Phase 5. In October 2026 the user
chose to grow it per engine instead: each engine gets the modes that are useful
for it, built on that engine's own semantics, rather than one generic layer that
fits every engine poorly. This document records the shape that takes.

## Principles

- **Depth per engine, not breadth.** An engine ships a mode only when that mode
  is right for it. A "safe" verdict on a migration that is in fact dangerous for
  that engine is worse than no verdict, so migration rules for an engine come
  from its documentation and are tested against real servers of the versions
  they claim.
- **The engine enforces safety, the parser explains it.** Every engine opens
  its connection in a mode the engine itself keeps read-only. Statement checks in
  dbscope exist to refuse early with a clear message, not as the barrier.
- **Capabilities, not engine checks in the UI.** A profile carries
  `capabilities` (query, ask, migrations, workload); the ERD is always there.
  The agent answers `501 unsupported` for a mode outside them before doing any
  work, and the UI hides what an engine lacks.

## Layout

```
internal/conn      Profile.Engine, Profile.Path, Capabilities; Postgres connections
internal/agent     engineSource routes each SchemaSource call by profile engine
  pools.go         PostgreSQL (pgx pools)
  sqlite_source.go SQLite (one read-only open per call)
internal/sqlite    SQLite: Open, Introspect, Check, Run, Test
internal/schema    the shared relational model; Model.Engine is empty for PostgreSQL
```

Relational engines map onto `schema.Model`. Non-relational engines will get
models of their own rather than being forced into tables and columns.

## Engines

| Engine | ERD | Query | Ask | Migrations | Workload |
|---|---|---|---|---|---|
| PostgreSQL | yes | yes | yes | yes | yes |
| SQLite | yes | yes | not yet | yes | n/a |

### SQLite

A profile names an absolute file path; there is no host, user or password, so
saving one never needs the secrets vault. Research with sources:
[research-sqlite.md](research-sqlite.md).

**Opening.** `sqlite.Open` refuses a missing file (it never creates one) and
opens `file:...?mode=ro` with `query_only` and a busy timeout. Every connection
also gets an SQLite authorizer that allows reads only: a read-only open still
permits `ATTACH`, which reaches other files on the machine, so the authorizer
denies it along with every write, schema change, pragma assignment to
`query_only`/`writable_schema`/`trusted_schema`, and `load_extension`.

**WAL files.** Readers of a WAL database need its `-shm` file. In a writable
folder SQLite creates `-wal` and `-shm` next to the database, as it does for
any reader; the database file itself is never written. In a folder dbscope
cannot write, the read-only open fails with `SQLITE_READONLY_DIRECTORY`, so
`Open` retries with `immutable=1` and the connection test warns: SQLite then
ignores the `-wal` file (changes not yet checkpointed are not shown) and
assumes no other program writes during a read.

**ERD.** Read in one read transaction from `pragma_table_list` (which marks
virtual and shadow tables, skipped, and `WITHOUT ROWID`), `pragma_table_xinfo`
(hidden 2 and 3 are virtual and stored generated columns),
`pragma_foreign_key_list`, `pragma_index_list` and `pragma_index_xinfo`.

- `REFERENCES parent` without columns resolves to the parent's primary key.
- SQLite keeps foreign key names only in the CREATE text, so they are numbered (`orders_fk0`).
- A rowid alias is a single-column `INTEGER` primary key of a rowid table with no
  primary-key index (`INTEGER PRIMARY KEY DESC` has one and is not an alias).
- Row estimates come from `sqlite_stat1` after `ANALYZE`; without it they are
  unknown, since counting would read the whole file.
- Not read yet: CHECK constraints and generated-column expressions (they exist only in the CREATE text), triggers, attached databases.

**Queries.** `sqlite.Check` accepts one `SELECT`, `WITH`, `VALUES` or
`EXPLAIN` statement and refuses `load_extension`. The plan is
`EXPLAIN QUERY PLAN` as a tree of steps; SQLite gives no costs, so the UI shows
steps only and hides "with ANALYZE". The profile's statement timeout interrupts
the statement. Values follow the PostgreSQL mapping: integers beyond 2^53 and
non-finite floats stay strings, and blobs are `\x` hex.

**Migrations.** `sqlite.Analyze` replays the base schema (from the profile's
file, or a `.schema` script for `migrate check --sqlite-schema`) into a private
in-memory database and runs each migration statement there, in file order, so
SQLite's own grammar decides what is valid: a rejected statement becomes
`DBS-SQLITE-REJECTED` with the 12-step rebuild as the alternative. Statements
are split with a port of `sqlite3_complete`, tested against the real function,
so trigger bodies stay whole. The scratch database has no rows, so rules cover
what depends on data:

| Rule | When |
|---|---|
| `DBS-SQLITE-NOT-NULL-NO-DEFAULT` | ADD COLUMN NOT NULL without a non-NULL default on a table with rows (SQLite checks it row by row) |
| `DBS-SQLITE-TABLE-WORK` | DROP COLUMN, an index build, a table copy (`INSERT ... SELECT`) or an unfiltered UPDATE/DELETE, rated by the table's rows: 100,000 medium, 1,000,000 high, unknown medium |
| `DBS-SQLITE-VERSION` | RENAME COLUMN (3.25.0), DROP COLUMN (3.35.0, 3.35.5 for the corruption fixes), ALTER COLUMN and ADD/DROP CONSTRAINT (3.53.0) |
| `DBS-SQLITE-FK-PRAGMA-IN-TX` | `PRAGMA foreign_keys=OFF` inside BEGIN (high: a silent no-op), or outside one (medium: a no-op if the runner wraps files) |
| `DBS-SQLITE-DROP-CASCADE` / `-DROP-REFERENCED` | DROP TABLE of a parent while foreign keys may be on |
| `DBS-SQLITE-RENAME-FIRST` | a table renamed away and created again in the same file |
| `DBS-SQLITE-UNSAFE-PRAGMA` | `writable_schema`, `schema_version=`, `journal_mode=OFF/MEMORY`, `defer_foreign_keys=0` |
| `DBS-SQLITE-VACUUM` | VACUUM (twice the file size in free disk, no transaction, may renumber rowids) |
| `DBS-SQLITE-UNIQUE-DATA` | a unique index on a table with rows, which duplicates would make fail |

Every write holds SQLite's database-wide write lock (`DATABASE WRITE` in the
report). `ATTACH`, `VACUUM INTO` and `load_extension` are never run on the
scratch database (`DBS-SQLITE-NOT-RUN`), and its authorizer refuses attaching
any named file. Rules are checked with dbscope's SQLite (3.54.1); the version
an application links may accept fewer ALTER forms, which the version rule
calls out.

## Next engines

MySQL and MariaDB next (research in [research-mysql.md](research-mysql.md)),
then MongoDB or Redis.

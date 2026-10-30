# Migration analysis

Research: `docs/research-migrations.md`. Deterministic: libpg_query AST, no LLM.

## Pipeline

```
files → ingest (order, framework, transaction context per statement)
      → parse (go-pgquery, PG17 grammar; positions → line/column)
      → apply each statement to a clone of the base schema.Model (the diff)
      → classify (rule table: lock level per table, work, transaction rules, version deltas)
      → timeout state (SET/SET LOCAL/RESET/set_config/BEGIN/COMMIT tracked per statement)
      → score + findings (safe alternative per rule)
      → impact (dependents of every changed relation/column)
      → report: JSON | text | SARIF 2.1.0 | GitHub annotations
```

Base schema, in order of preference: a live profile (introspection + `pg_class` sizes +
`pg_db_role_setting` timeouts), a `dbscope stats export` JSON file plus a `pg_dump --schema-only`
file applied through the same engine, or the dump alone (sizes unknown → size tier 2).

## ORM migrations

Rails, Django and Alembic migrations are code; `dbscope migrate capture` records the SQL they run
and writes plain files with the observed `BEGIN`/`COMMIT`, which ingest reads as explicit
transactions ([ORM-CAPTURE.md](ORM-CAPTURE.md)). Prisma files are read directly, but the transaction
rule for a multi-statement `migration.sql` depends on the Prisma version: Prisma 6 sends it as one
query (one implicit transaction, which ingest assumes), Prisma 7 sends each statement on its own. For
Prisma 7 use `--tx-mode statement`, or capture.

## Parser limit

The only CGO-free parser (go-pgquery, verified 2026-10-27 at its newest build) uses the PG17 grammar.
PG18-only syntax (`GENERATED … VIRTUAL`, `ADD CONSTRAINT … NOT NULL … NOT VALID`, `NOT ENFORCED`)
fails to parse. Such a statement becomes a `DBS-PARSE` finding ("cannot be analyzed with the PostgreSQL 17
grammar") rated **High** so CI does not silently pass it; the rest of the file is still analyzed.
Revisit when a PG18 libpg_query binding exists.

## Ground truth

Rule data (lock level per affected table, rewrite vs scan vs none, allowed in a transaction) must
agree with what PostgreSQL actually does. `internal/migrate/groundtruth` runs every fixture DDL on
PostgreSQL 14, 17 and 18 and records:

- locks: in a transaction, run the DDL, then from a second session read `pg_locks` for that backend
  (`locktype='relation'`, mode, relation) before `ROLLBACK`; for statements that cannot run in a
  transaction, hold a conflicting lock from a third session and read the waiting request;
- rewrite: `pg_class.relfilenode` of the table and its indexes before vs after;
- transaction: whether `BEGIN; <ddl>` errors with 25001.

The test fails if `rules` data disagrees with the recorded ground truth for any version.

## Scoring (per statement, maximum over every table it locks)

- L lock class: 0 none/ACCESS SHARE/ROW SHARE · 1 SHARE UPDATE EXCLUSIVE · 2 SHARE / SHARE ROW EXCLUSIVE / EXCLUSIVE · 3 ACCESS EXCLUSIVE
- W work: 0 catalog only · 1 scan or index build · 2 rewrite
- Z size tier (pages × block size): 0 < 10 MB · 1 < 1 GB · 2 < 10 GB · 3 < 50 GB · 4 ≥ 50 GB; unknown → 2
- Q = 1 if L ≥ 2 (lock-queue exposure); T = 1 if L ≥ 2 and no lock_timeout in effect; H = 1 if ACCESS EXCLUSIVE is already held earlier in the same transaction
- K = 1 if the lock stops traffic on a busy table. ACCESS EXCLUSIVE stops reads and writes, so the
  table counts as busy with at least 5% of the database's table scans (sequential plus index); the
  SHARE-class locks stop only writes, so it counts as busy with at least 5% of the database's row
  writes. Either way at least 1,000 of them, and a partitioned table counts its partitions'. The
  finding is `DBS-HOT-TABLE`, giving the share and since when the counters run. Counters come from a
  live base or a `dbscope stats export` file (`scans`, `rowsWritten`, `totalScans`,
  `totalRowsWritten`, `activitySince`, `inRecovery`); without database totals the share is of the
  schemas read, and the finding says so. A schema dump alone has no counters, so K is 0. On a replica
  they count only that replica's reads and no writes.
- score = L·W·Z + Q + 3T + 2H + 2K → Low 0-2 · Medium 3-6 · High 7-11 · Critical ≥ 12

Query dependents found in `pg_stat_statements` carry their call counts. Workload on the canvas is
described in [WORKLOAD.md](WORKLOAD.md).

Overrides: statements that will fail (CONCURRENTLY in a transaction, ADD … NOT NULL without default
on a non-empty table, unparseable) → Critical. Breaking changes (drop/rename column or table, type
narrowing, new NOT NULL on an existing column) → at least High when anything depends on the object
(impact), else Medium. Dropping a foreign key, check or unique constraint (`DBS-DROP-CONSTRAINT`) is
Medium: the statement is cheap, but the database stops enforcing the rule, and the finding says which. Data loss (DROP TABLE/COLUMN, TRUNCATE on a non-empty table) → Critical.
Lock and rewrite are reported separately (squawk and strong_migrations disagree on varchar widening
because one looks at the lock and the other at the rewrite; both facts are shown).

## SARIF / CI

`dbscope migrate check` exits 1 when any statement's rating ≥ `--fail-on` (default `high`).
SARIF levels: Critical/High → error, Medium → warning, Low → note. Columns are UTF-16 code units.
One rule per finding type (`DBS-…`), repo-relative URIs, stable `partialFingerprints`.

## Code that still uses what a migration drops

When a connection has a project folder, or the CLI is given `--code DIR`, dbscope reads that folder after the
analysis and lists every line that still names a table, collection, index or column the migration drops or renames
(`Report.Usages`), for every engine. It only reads the folder: it follows no symlinks and runs nothing found there. It
skips .git, node_modules, vendor, dist, build, target, .next, .venv, __pycache__, coverage, the migrations folder and
the analyzed files, binary files and files over 1 MiB, and stops after 50,000 files or 5 seconds (a warning says so).
Names match case-insensitively and only as whole identifiers, so `email` matches `user.email` but not
`email_address`. Common column names appear everywhere, so a column is reported only in files that also name its
table or model (`customers`, `customer`, `Customer`). The statement that drops or renames the name gets
`DBS-STILL-USED` (high) with up to 20 example lines and the total count. Generated schema files kept in the project,
such as Rails' `db/schema.rb`, are reported too. The safe path is expand and contract: stop using the name in one
release and drop it in the next.

## Rollback (down) check

When the migration tool supports rollbacks, dbscope pairs each up migration with its down: golang-migrate
`.down.sql` files, Flyway `U` undo files (only when the folder has any), goose `-- +goose Down` and dbmate
`-- migrate:down` sections, and the `down` function of migrate-mongo files. Down code is read only for this check and
is never rated as a forward migration (ClickHouse and Cassandra skip it too). An up with no down gets
`DBS-DOWN-MISSING` (medium). dbscope then applies the up and the down to a copy of the schema as it was before the up:
the apply model on PostgreSQL, a scratch SQLite database, and a name-level replay of tables, columns, indexes and
collections on MySQL, MariaDB and MongoDB. If the result differs from the starting schema, or SQLite rejects a down
statement, the up gets `DBS-DOWN-INCOMPLETE` (high). If the up drops a table, column, collection or field, it gets
`DBS-DOWN-LOSES-DATA` (medium): the down can recreate the structure but not the rows. The findings go on the up
file's first statement, so they count toward its rating and the CI exit code. Limits: column order is ignored on
PostgreSQL (a re-added column always goes last); MySQL compares column types and nullability but not defaults or
charsets; an up file sent alone from the browser, without its down beside it, is reported as missing a down.

## Pending migrations and the migrations folder

A connection can name its migrations folder (Advanced settings). dbscope then reads it directly, read only
(`POST /api/profiles/{id}/migrations/analyze-folder`, the palette's "Check the migrations folder"), and with "only
pending files" leaves out the files the migration tool has already applied. It finds the tool from the files' names
and from which history it can read: Flyway's `flyway_schema_history`, golang-migrate's `schema_migrations` (a dirty
version counts as not applied), goose's `goose_db_version` (the latest row per version), dbmate's or Rails'
`schema_migrations`, Prisma's `_prisma_migrations` (finished and not rolled back), migrate-mongo's `changelog`
collection, neo4j-migrations' `__Neo4jMigration` nodes and elasticsearch-evolution's `es_evolution` index, on
PostgreSQL, MySQL and SQLite, and golang-migrate and goose also on ClickHouse. Applied files whose checksum no longer
matches the history (Flyway, Prisma, migrate-mongo with file hashes, neo4j-migrations) are listed as changed and
checked again. Only golang-migrate's table was produced by running the real tool in the tests; the other tables are
created in the tests from each tool's source, which the tests cite. When no history is found, or it cannot be read,
the report says so and every file is checked. A folder read holds at most 5,000 migration files and 64 MiB; at most
200 files are analyzed at once, counted after applied files are left out.

"Re-check on save" polls `GET /api/profiles/{id}/migrations/folder-state` every 2 seconds, a fingerprint of the
migration files' names, sizes and modification times (no file is opened), and analyzes the folder again when it
changes.

On the command line, `dbscope migrate check --pending` does the same against the live base given with `--dsn`,
`--mysql` or `--sqlite` and lists the skipped files on stderr, and `--since GIT-REF` checks only the files that differ
from GIT-REF in the working tree or are untracked.

## Safer rewrites

When dbscope can write a safer form of a statement mechanically, the review shows a "Safer rewrite" with the exact
replacement text and a note (`Statement.Rewrite`). Each kind was run on real servers in the tests and analyzed
again: it removes the finding it fixes without raising the rating, and leaves the same schema or rows as the
original. PostgreSQL: CREATE INDEX and DROP INDEX become CONCURRENTLY when the statement runs outside a transaction
(inside one, the note says to move it to its own migration); ADD CONSTRAINT ... FOREIGN KEY or CHECK becomes
ADD ... NOT VALID followed by VALIDATE CONSTRAINT, naming the constraint as PostgreSQL would; a statement requesting a
strong lock without lock_timeout gets `SET lock_timeout = '2s'` (SET LOCAL inside a transaction). MySQL and MariaDB:
DDL without lock_wait_timeout gets `SET SESSION lock_wait_timeout = 5`, and when dbscope predicts the algorithm it names
it (ALGORITHM=INSTANT, or INPLACE/NOCOPY with LOCK=NONE), so the server refuses the statement instead of silently
copying the table. Neo4j: an unbatched MATCH ... SET, REMOVE or DELETE over a large label moves its writes into
`CALL (vars) { ... } IN TRANSACTIONS OF 10000 ROWS` (5.23 or later; it must run as an auto-commit statement).
ClickHouse: an ALTER that starts a mutation gets `SETTINGS mutations_sync = 2`, and ALTER TABLE ... DELETE WHERE becomes
a lightweight DELETE FROM on MergeTree tables without projections (23.3 or later). No rewrite is offered when dbscope
is unsure, or when it would change which rows or objects are affected. dbscope never edits the files: the rewrite is
copied from the review.

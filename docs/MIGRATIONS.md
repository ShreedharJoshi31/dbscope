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
- score = L·W·Z + Q + 3T + 2H → Low 0-2 · Medium 3-6 · High 7-11 · Critical ≥ 12

Overrides: statements that will fail (CONCURRENTLY in a transaction, ADD … NOT NULL without default
on a non-empty table, unparseable) → Critical. Breaking changes (drop/rename column or table, type
narrowing, new NOT NULL on an existing column) → at least High when anything depends on the object
(impact), else Medium. Data loss (DROP TABLE/COLUMN, TRUNCATE on a non-empty table) → Critical.
Lock and rewrite are reported separately (squawk and strong_migrations disagree on varchar widening
because one looks at the lock and the other at the rewrite; both facts are shown).

## SARIF / CI

`dbscope migrate check` exits 1 when any statement's rating ≥ `--fail-on` (default `high`).
SARIF levels: Critical/High → error, Medium → warning, Low → note. Columns are UTF-16 code units.
One rule per finding type (`DBS-…`), repo-relative URIs, stable `partialFingerprints`.

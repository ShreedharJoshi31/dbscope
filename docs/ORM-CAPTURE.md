# Capturing ORM migrations

Rails, Django and Alembic migrations are code, not SQL, so `migrate check` cannot
read them. Drizzle writes SQL files but its migrator runs every pending file in
one transaction, which the files do not say. `dbscope migrate capture` runs the
project's own migrate command against a throwaway database and records the SQL
it actually sends, then writes it as plain `.sql` files that `migrate check` and
the migration review UI already understand.

```bash
createdb dbscope_shadow            # empty, throwaway
dbscope migrate capture --shadow postgres://me@localhost/dbscope_shadow --out captured -- bin/rails db:migrate
dbscope migrate check captured --dsn "$STAGING_REPLICA_URL"
```

## How it works

```
 migrate command ──DATABASE_URL──▶ dbscope proxy (127.0.0.1:random) ──▶ shadow database
                                        │ records every statement, its result
                                        ▼ and the transaction status
                         split by bookkeeping writes ──▶ captured/0001_<name>.sql …
```

1. Check, before anything runs: `--out` is empty; the shadow is reachable and
   has no tables of its own (unless `--allow-nonempty-shadow`); with `--dsn`,
   the two are not the same database (compared by server start time and
   database OID, so two host names for one server still match); and no
   variable in the environment names a database URL that dbscope would not
   redirect (for example `CACHE_DATABASE_URL`; pass `--url-env` for it).
2. Start a recording proxy on `127.0.0.1` in front of the shadow.
3. Run the command with `DATABASE_URL`, `DIRECT_URL`, `DATABASE_URL_UNPOOLED`,
   `POSTGRES_URL`, `POSTGRES_URL_NON_POOLING`, `POSTGRES_PRISMA_URL`, every
   `--url-env` name and the libpq `PG*` variables pointed at the proxy
   (`sslmode=disable`; the proxy may use TLS to the shadow). Tools that read a
   `.env` file keep these values, because dotenv does not override the
   environment. `PGSERVICE`, `PGHOSTADDR` and `DBSCOPE_DSN` are removed.
   - Rails: `SCHEMA` is set to a scratch file. Rails 8 loads `db/schema.rb`
     into a database that has no `schema_migrations` instead of migrating it,
     and rewrites the schema file afterwards; with `SCHEMA` elsewhere the
     migrations run and the project's file is left alone.
   - Django: a generated settings module imports the project's settings and
     points every entry in `DATABASES` at the proxy.
4. Fail if the command never connected through the proxy: it probably migrated
   whatever database the project is configured for, and nothing was captured.
5. Keep the statements that changed something, split them into migrations at
   each framework's bookkeeping write, and write one file per migration (or per
   transaction when one transaction spans several migrations).
6. With `--dsn`, drop migrations the target database has already applied, so
   only pending ones are checked.

## Wire facts the design relies on

Each row was run through `dbscope migrate capture` and compared with the
server's `log_statement=all` output on PostgreSQL 17.11 (2026-10-28); sources
for the rest are in [research-orm-capture.md](research-orm-capture.md).

| Framework (tested) | Pointed at the proxy by | Bookkeeping write (split marker) | Transactions on the wire |
|---|---|---|---|
| Rails 8.1.4, `bin/rails db:migrate` | `DATABASE_URL` | `INSERT INTO "schema_migrations" ("version") VALUES ('…') RETURNING "version"`, inside the migration's transaction | `BEGIN`/`COMMIT` per migration; `disable_ddl_transaction!` runs bare |
| Django 6.1 + psycopg 3.3, `manage.py migrate` | a generated settings module (Django reads no URL variable) | `INSERT INTO "django_migrations" … VALUES ('app', '0001_initial', …)`; after `COMMIT` when the migration had deferred SQL | `BEGIN`/`COMMIT` per migration; `atomic = False` runs bare |
| Alembic 1.20 + SQLAlchemy 2.1, `alembic upgrade head` | `DATABASE_URL`, if `env.py` reads it | `INSERT INTO alembic_version …`, then `UPDATE alembic_version SET version_num='a2' …` | one transaction for the whole run by default, or `BEGIN ISOLATION LEVEL READ COMMITTED` per migration with `transaction_per_migration`; `autocommit_block()` commits, runs bare, and opens the next |
| drizzle-orm 0.44 migrator | `DATABASE_URL`, if the project passes it | `insert into "drizzle"."__drizzle_migrations" ("hash", "created_at") values($1, $2)`; `created_at` is the journal's `when` | every pending migration in one `begin`…`commit` |
| Prisma 7.10, `prisma migrate deploy` | `DATABASE_URL` via `prisma.config.ts` | `INSERT INTO "_prisma_migrations"` before the script, `UPDATE … SET "finished_at"` after it (binary parameters) | each statement of `migration.sql` on its own, no transaction |
| Prisma 6.19, `prisma migrate deploy` | `DATABASE_URL` | as Prisma 7 | the whole `migration.sql` as one query, so one implicit transaction |

The two Prisma rows are why capture beats reading files: the same
`migration.sql` holds its locks across statements on Prisma 6 and not on
Prisma 7, and nothing in the file says which.

A statement belongs to the migration whose marker follows it; the migration
closes at the next point the connection is idle, so a `COMMIT` after the marker
stays with it. Data-migration code (Django `RunPython`, Rails `execute`) is
captured as the SQL it ran, which offline modes such as `sqlmigrate` cannot do.

## The proxy

- Client leg is plaintext: `'N'` to SSLRequest and GSSENCRequest, and a
  connection that opens with a TLS record (direct SSL) is closed with an error
  telling the user to use `sslmode=disable`. The shadow leg uses TLS when the
  shadow URL asks for it.
- The one message the proxy changes: when the shadow offers
  `SCRAM-SHA-256-PLUS` (it does over TLS), the offer is reduced to plain
  `SCRAM-SHA-256`. libpq (psycopg, Ruby `pg`) refuses a `-PLUS` offer on a
  connection it did not encrypt ("server offered SCRAM-SHA-256-PLUS
  authentication over a non-SSL connection", seen with psycopg 3.3 on
  2026-10-28); plain SCRAM then authenticates normally over the shadow's TLS.
- All other frames are forwarded unchanged; only `Q P B E S` (client) and
  `Z C E I s` (server) are decoded, with pgproto3's per-message `Decode`.
  Password and SASL messages from the client are never decoded or recorded.
- Results are matched to `Query` and `Execute` in send order; after an error,
  everything up to the next `Sync` was skipped by the server.
- CancelRequest is forwarded verbatim to the shadow.
- Bound text parameters are rendered as quoted literals; binary ones stay as
  `$n` and the statement is marked.

## What is kept

- Kept: DDL; DML on application tables; `SELECT`s without `FROM` that call a
  function with side effects (`setval`, `create_hypertable`, …); `SET` and
  `set_config` of `lock_timeout`, `statement_timeout` and `search_path` when a
  statement follows them; and the transaction boundaries, with empty
  transactions dropped.
- Left out and counted: queries, `SHOW`, other `SET`s (connection setup),
  advisory locks, comment-only pings, and anything that touches a bookkeeping
  table.
- Statements that did not take effect are left out: failed ones (reported),
  rolled-back transactions and savepoints, a `COMMIT` the server answered with
  `ROLLBACK`, a multi-statement query or pipeline whose implicit transaction
  failed (including at its commit, for deferred constraints), and a
  transaction left open when the connection closed.
- Each connection is split on its own, so two connections' transactions never mix.

## Limits

- Rails with several databases (`cache`, `queue`, …) migrates each, and only
  `primary` follows `DATABASE_URL`; the others come from `database.yml` unless
  `<NAME>_DATABASE_URL` is set. Run `bin/rails db:migrate:primary`, or add
  `--url-env CACHE_DATABASE_URL` (and so on) to send them through dbscope too.
- A session-level `SET lock_timeout` in one migration still applies to later
  ones on the same connection, but only the migration that ran it shows it, so
  later files may be flagged for a missing timeout they had.
- A named prepared statement that fails to re-parse keeps its new text in the
  capture (ORMs do not re-prepare names, so this has not been seen).
- Prisma 8 (release candidate, untested) runs `ops.json` plans instead of SQL files and
  keeps its bookkeeping in another schema; capture records its SQL but cannot
  yet name its migrations, so everything lands in one unrecorded file.

- The project must take its database URL from `DATABASE_URL` (Rails, Prisma
  and most Drizzle setups do; Alembic only if `env.py` reads it; Django is
  handled with a generated settings module).
  dbscope cannot stop a command that ignores it; step 4 detects it afterwards.
- The shadow must be the same major version as production for the captured
  SQL to match (ORMs can emit version-specific SQL).
- Running every migration from scratch can be slow on old projects; the shadow
  may be prepared to a recent state first (`--allow-nonempty-shadow`).

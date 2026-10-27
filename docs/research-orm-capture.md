# Research: ORM migration capture

Collected 2026-10-28 by two research workers from primary sources, then checked against real runs of Django 6.1, Alembic 1.20 and drizzle-orm with `log_statement=all` (results in docs/ORM-CAPTURE.md). Claims the workers could not verify are listed at the end of each part.

**Correction found in testing:** Part 2 says a plaintext client leg keeps SCRAM working byte for byte when the shadow uses TLS. That holds for pgx and node-postgres, but libpq rejects the server's `SCRAM-SHA-256-PLUS` offer on an unencrypted socket, so the proxy removes `-PLUS` from the offer (docs/ORM-CAPTURE.md).

# Part 1: frameworks


Sources were fetched on 2026-10-28. Rails is cited at tag `v8.1.0` and Django at tag `6.0`. Alembic is cited at `rel_1_20_1` or `main`; PyPI lists 1.20.1 as latest, uploaded 2026-10-22. Drizzle and prisma-engines are cited at `main` (and Drizzle's `beta` branch) as of today, because I could not pin a commit. I did not run any framework.

**Main findings**
- **Prisma 8 changes the picture.** The `latest` tag for the npm package `prisma` now points to `8.0.0-rc.27`, a TypeScript rewrite. Its migrations are `migration.ts` and `ops.json` files, not `migration.sql`. `migrate deploy` becomes `prisma db migrate`. The file-based ingest will not work for Prisma 8 projects.
- **Django can record a migration after COMMIT.** If a migration leaves deferred SQL, the `django_migrations` INSERT is sent after the transaction commits. Splitting on "INSERT inside the transaction" would misattribute it.
- **Drizzle runs all pending migrations in one transaction.** This holds on both the 0.x and v1 lines, so transaction boundaries cannot split Drizzle runs. Its parameterized INSERT is the only usable marker.
- **Current prisma-engines `main` sends each statement of `migration.sql` on its own.** That means no implicit transaction. Older behavior differed, and I could not find which 7.x release changed it.

### Rails (ActiveRecord 8.1.0, `bin/rails db:migrate`)

**1. Pointing at another database**
- `DATABASE_URL` is merged with `database.yml` for the current environment. Where both set a value, the URL wins. [Rails guide, Connection Preference](https://guides.rubyonrails.org/configuring.html#connection-preference)
- The code reads `"#{name.upcase}_DATABASE_URL"` for every named database. It falls back to `DATABASE_URL` only for `primary`. [database_configurations.rb](https://github.com/rails/rails/blob/v8.1.0/activerecord/lib/active_record/database_configurations.rb)
- In multi-database setups, the other databases (for example Rails 8 `cache` or `queue`) still go to their configured hosts unless you also set `CACHE_DATABASE_URL` and similar.
- The environment variable is ignored when the yml entry already has a `url:` key (`next config if config.is_a?(UrlConfig)`, same file). In that case Rails migrates the configured database.
- I found no CLI flag that overrides the URL.

**2. Bookkeeping**
- Table: `schema_migrations`, with a single string column `version` as primary key. It is created if missing when the Migrator is built. [schema_migration.rb](https://github.com/rails/rails/blob/v8.1.0/activerecord/lib/active_record/schema_migration.rb)
- The insert is built with `im.insert(arel_table[primary_key] => version)`. Arel quotes plain values inline (`collector << quote(value).to_s`). [to_sql.rb](https://github.com/rails/rails/blob/v8.1.0/activerecord/lib/arel/visitors/to_sql.rb)
  - So the INSERT appears to be a literal, not parameterized: `INSERT INTO "schema_migrations" ("version") VALUES ('<version>')`.
  - A trailing `RETURNING "version"` is likely but unverified.
- Order: `ddl_transaction(migration) do migration.migrate(@direction); record_version_state_after_migrating(migration.version) end`. The version INSERT runs after the migration's DDL, inside the same transaction. [migration.rb](https://github.com/rails/rails/blob/v8.1.0/activerecord/lib/active_record/migration.rb)
- `record_environment` also writes `ar_internal_metadata` before the migrations run (same file).
- Advisory lock:
  - Lock id is `MIGRATOR_SALT * Zlib.crc32(connection.current_database)`, with `MIGRATOR_SALT = 2053462845` (migration.rb).
  - Taken with `SELECT pg_try_advisory_lock(#{lock_id})` and released with `SELECT pg_advisory_unlock(#{lock_id})`. [postgresql_adapter.rb](https://github.com/rails/rails/blob/v8.1.0/activerecord/lib/active_record/connection_adapters/postgresql_adapter.rb)
  - In 8.1 the lock appears to be taken on the same migration connection.
- Startup queries in `configure_connection` (postgresql_adapter.rb):
  - `SET standard_conforming_strings = on`
  - `SET intervalstyle = iso_8601`
  - `SET SESSION <k> TO ...` for each configured variable
  - `SELECT t.oid, t.typname FROM pg_type as t WHERE t.typname IN (...)`
  - a `client_min_messages` setter and `SET SESSION timezone TO 'UTC'`

**3. Transactions on the wire**
- Rails sends literal `BEGIN`, `COMMIT` and `ROLLBACK` strings. [postgresql/database_statements.rb](https://github.com/rails/rails/blob/v8.1.0/activerecord/lib/active_record/connection_adapters/postgresql/database_statements.rb)
- Transactions are lazy (`supports_lazy_transactions? true`), so `BEGIN` goes out just before the first statement.
- There is one transaction per migration and no savepoints at migration level.
- Opt-out: `disable_ddl_transaction!`. `use_transaction?` is `!migration.disable_ddl_transaction && connection.supports_ddl_transactions?`. With the opt-out, the DDL and the version INSERT each autocommit.

**4. Protocol** (`perform_query` in database_statements.rb)
- `prepare` true: `exec_prepared`.
- No binds: `raw_connection.async_exec(sql)`. This appears to be the simple query protocol.
- With binds: `exec_params`, which is the extended protocol.
- DDL goes through `execute` with no binds, so it should be a simple query.
- Bind values use `PG::TextEncoder`, which means text format (`add_pg_encoders`).

**5. Offline SQL:** I found no built-in print-SQL or dry-run mode.

### Django (6.0 source, `manage.py migrate`)

**1. Pointing at another database**
- Django has no built-in database URL environment variable. Connection comes from `settings.DATABASES`.
- `--database <alias>` only chooses among the configured aliases. [sqlmigrate.py shows the option](https://github.com/django/django/blob/6.0/django/core/management/commands/sqlmigrate.py)
- The way in is a different settings module, via `DJANGO_SETTINGS_MODULE` or `--settings`. dbscope could generate a module outside the project that imports the project's settings and replaces `DATABASES`.
- The backend passes only the parameters that are set (`conn_params = {"dbname": ..., **OPTIONS}`). So libpq variables such as `PGHOST` and `PGPORT` may apply when HOST and PORT are empty. [postgresql/base.py](https://github.com/django/django/blob/6.0/django/db/backends/postgresql/base.py)

**2. Bookkeeping**
- Table: `django_migrations`, with columns `app`, `name`, `applied`. Written by `self.migration_qs.create(app=app, name=name)`, which is an ORM INSERT. [recorder.py](https://github.com/django/django/blob/6.0/django/db/migrations/recorder.py)
- The INSERT is parameterized in Python, but the default cursor binds on the client, so the wire carries literals.
- Order matters ([executor.py](https://github.com/django/django/blob/6.0/django/db/migrations/executor.py)):
  ```
  with self.connection.schema_editor(atomic=migration.atomic) as schema_editor:
      state = migration.apply(state, schema_editor)
      if not schema_editor.deferred_sql:
          self.record_migration(...)
  ...
  if not migration_recorded:
      self.record_migration(...)
  ```
  The schema editor's `__exit__` runs `deferred_sql` (often FK and index statements) and then commits. [base/schema.py](https://github.com/django/django/blob/6.0/django/db/backends/base/schema.py)
  - Without deferred SQL, the INSERT is inside the transaction.
  - With deferred SQL, the INSERT comes after COMMIT, in autocommit.
- Squashed migrations record each replaced migration (`for replaced_app_label, replaced_name in migration.replaces`, executor.py).
- I found no advisory lock in executor.py or recorder.py.
- Startup: `has_table()` calls `introspection.table_names()`, then the code reads all `django_migrations` rows (recorder.py).

**3. Transactions on the wire**
- Django sends no `BEGIN` text itself. It uses `atomic()`, which turns off driver autocommit.
- psycopg 3 then sends `BEGIN` before the first statement and `COMMIT` at the end. Nested blocks use `SAVEPOINT`. [psycopg transactions](https://www.psycopg.org/psycopg3/docs/basic/transactions.html)
- One transaction per migration (`schema_editor(atomic=migration.atomic)`).
- Opt-out: `atomic = False` on the Migration class. Individual operations can still be forced atomic: `if not schema_editor.atomic_migration and atomic_operation: with atomic(...)`. [migration.py](https://github.com/django/django/blob/6.0/django/db/migrations/migration.py)

**4. Protocol**
- With psycopg 3.1.8+, Django defaults to client-side binding cursors. `OPTIONS["server_side_binding"]` switches this. [Django databases docs](https://docs.djangoproject.com/en/6.0/ref/databases/#server-side-parameters-binding)
- psycopg says `ClientCursor` "should ensure that psycopg will always use the simple query protocol". [psycopg cursors](https://www.psycopg.org/psycopg3/docs/advanced/cursors.html)
- So by default everything, DDL included, appears to go as simple query with literals inlined.
- psycopg2 binds only on the client. [psycopg from_pg2](https://www.psycopg.org/psycopg3/docs/basic/from_pg2.html)

**5. Offline SQL: `sqlmigrate`**
- It needs a live connection "to resolve constraint names". [docs](https://docs.djangoproject.com/en/6.0/ref/django-admin/#sqlmigrate) It also builds `MigrationLoader(connection, ...)`, which reads `django_migrations`.
- It wraps output in `BEGIN;` and `COMMIT;` only when `migration.atomic and connection.features.can_rollback_ddl` (sqlmigrate.py).
- For RunPython and other non-SQL operations it emits `-- THIS OPERATION CANNOT BE WRITTEN AS SQL` and skips them (migration.py).
- It does not include the `django_migrations` INSERT.

### Alembic (1.20.1 latest; `alembic upgrade head`)

**1. Pointing at another database**
- The generic `env.py` reads only `config.get_main_option("sqlalchemy.url")` (offline) and `engine_from_config(..., prefix="sqlalchemy.")` (online). [env.py template](https://github.com/sqlalchemy/alembic/blob/main/alembic/templates/generic/env.py)
- No environment variable is read for the URL. The tutorial says the URL is "consumed by the user-maintained env.py script only". [tutorial](https://alembic.sqlalchemy.org/en/latest/tutorial.html)
- CLI options: `-c/--config`, which defaults to the `ALEMBIC_CONFIG` environment variable, plus `-n/--name` and `-x`. There is no URL flag. [config.py](https://github.com/sqlalchemy/alembic/blob/rel_1_20_1/alembic/config.py)
- A dbscope-written ini passed via `ALEMBIC_CONFIG` works only if `env.py` still uses `sqlalchemy.url`. Many projects override it in `env.py`, and then Alembic migrates the configured database.

**2. Bookkeeping**
- Table: `alembic_version`, defined as `Column("version_num", String(32), nullable=False)` with primary key name `f"{version_table}_pkc"`. [ddl/impl.py](https://github.com/sqlalchemy/alembic/blob/rel_1_20_1/alembic/ddl/impl.py)
- Writes use `literal_column("'%s'" % version)`, so they are not parameterized. [runtime/migration.py](https://github.com/sqlalchemy/alembic/blob/main/alembic/runtime/migration.py)
  - `_insert_version`: INSERT
  - `_update_version`: UPDATE ... WHERE `version_num = '<from>'`
  - `_delete_version`: DELETE
- Order: inside `with self.begin_transaction(_per_migration=True):` the code calls `step.migration_fn(**kw)` and then `head_maintainer.update_to_step(step)`. The version write follows the DDL in the same transaction (same file).
- No advisory lock found.
- Startup: SQLAlchemy dialect initialization and a has-table check for `alembic_version`. Exact texts unverified.

**3. Transactions on the wire**
- Default: one transaction for the whole run, from `with context.begin_transaction(): context.run_migrations()`.
- `transaction_per_migration=True` gives "nest each migration script in a transaction". [runtime API](https://alembic.sqlalchemy.org/en/latest/api/runtime.html)
- `autocommit_block()` commits the open transaction unconditionally, switches to AUTOCOMMIT, then starts a new transaction (same doc and source).
- `BEGIN` and `COMMIT` come from the driver (psycopg 3 as above; psycopg2 appears similar).

**4. Protocol**
- psycopg2 binds on the client. psycopg 3 under SQLAlchemy "by default will bind them server side". [SQLAlchemy docs](https://docs.sqlalchemy.org/en/20/dialects/postgresql.html#using-a-different-cursor-class)
- psycopg 3 uses `send_query` (simple) when there are no params and the format is text. Otherwise it uses `send_query_params` (extended). [_cursor_base.py](https://github.com/psycopg/psycopg/blob/master/psycopg/psycopg/_cursor_base.py)
- Rendered DDL and `alembic_version` writes have no params, so they appear to go as simple queries on both drivers.

**5. Offline SQL: `upgrade --sql`**
- Needs no connection.
- Emits `BEGIN` and `COMMIT` strings, `CREATE TABLE alembic_version` when starting from base, `-- Running <step>` comments, and the literal INSERT and UPDATE (runtime API; migration.py).

### Drizzle (stable drizzle-orm 0.45.4 and drizzle-kit 0.31.11; `rc` tag 1.0.0-rc.5)

[npm dist-tags](https://registry.npmjs.org/-/package/drizzle-orm/dist-tags)

**1. Pointing at another database**
- The documented flags are `drizzle-kit migrate --dialect=postgresql --url=...`, or `--config=<file>`. [docs](https://orm.drizzle.team/docs/drizzle-kit-migrate)
- The URL lives in `drizzle.config.ts` `dbCredentials.url`. Using `DATABASE_URL` there is only a project convention.
- If the project calls `migrate()` from its own code, it uses whatever URL that code passes.

**2 and 6. Bookkeeping, transaction, folder layout**
- 0.x ([pg-core/dialect.ts](https://github.com/drizzle-team/drizzle-orm/blob/main/drizzle-orm/src/pg-core/dialect.ts)):
  - `CREATE SCHEMA IF NOT EXISTS "drizzle"`
  - `CREATE TABLE IF NOT EXISTS "drizzle"."__drizzle_migrations" (id SERIAL PRIMARY KEY, hash text NOT NULL, created_at bigint)`
  - `select id, hash, created_at from ... order by created_at desc limit 1`
  - Then **one** `session.transaction` for all pending migrations. Each statement runs, followed by `insert into ... ("hash", "created_at") values(${migration.hash}, ${migration.folderMillis})`, which is parameterized.
- Hash: `sha256` of the whole `.sql` file. ([migrator.ts](https://github.com/drizzle-team/drizzle-orm/blob/main/drizzle-orm/src/migrator.ts))
- A migration is applied only if `Number(lastDbMigration.created_at) < migration.folderMillis`.
- 0.x layout: `meta/_journal.json` lists `entries` (`idx, when, tag, breakpoints`). Files are `<tag>.sql`, split on `--> statement-breakpoint`.
- v1 rc ([beta migrator.ts](https://github.com/drizzle-team/drizzle-orm/blob/beta/drizzle-orm/src/migrator.ts)):
  - It throws if `meta/_journal.json` exists ("run drizzle-kit up").
  - It reads `<dir>/migration.sql` for each subfolder, sorted by name. The first 14 characters of the folder name are the timestamp.
  - The table adds `name text` and `applied_at timestamp with time zone DEFAULT now()`.
  - The INSERT becomes `("hash", "created_at", "name") values(...)`.
  - It is still one `db.transaction` for the whole run. [pg-core/async/session.ts](https://github.com/drizzle-team/drizzle-orm/blob/beta/drizzle-orm/src/pg-core/async/session.ts)

**3. Transactions on the wire**
- Literal lowercase `begin` and `commit` (or `rollback`). Nested transactions use `savepoint spN`. [node-postgres/session.ts](https://github.com/drizzle-team/drizzle-orm/blob/main/drizzle-orm/src/node-postgres/session.ts)
- There is no per-migration opt-out, so `CREATE INDEX CONCURRENTLY` should fail under this migrator.

**4. Protocol**
- drizzle-kit uses `drizzle-orm/node-postgres/migrator` when `pg` is installed. [connections.ts](https://github.com/drizzle-team/drizzle-orm/blob/main/drizzle-kit/src/cli/connections.ts)
- node-postgres sends a simple query when `values` is empty and Parse/Bind/Execute when there are values. [query.js](https://github.com/brianc/node-postgres/blob/master/packages/pg/lib/query.js)
- So migration statements go as simple queries and the bookkeeping INSERT goes extended.
- With a Pool, the setup queries and the transaction may use different connections (session.ts).

**5. Offline SQL:** `generate` already writes the files. dbscope can ingest them directly with the layouts above.

### Prisma

**Versions**
- npm `prisma` `latest` is `8.0.0-rc.27`; `prev` is `7.10.1`. `@prisma/client` latest is still `7.10.1`. [dist-tags](https://registry.npmjs.org/-/package/prisma/dist-tags), [prisma-cli README](https://github.com/prisma/prisma-cli)

**Prisma 7: `migrate deploy`**
- Flags: only `--schema` and `--help` are documented. [v7 CLI](https://www.prisma.io/docs/orm/v7/reference/prisma-cli-reference)
- The URL comes from `prisma.config.ts` `datasource.url`, usually `env("DATABASE_URL")`. `.env` must be loaded explicitly. [v7 config](https://www.prisma.io/docs/orm/v7/reference/prisma-config-reference)
- Table and lock ([postgres.rs](https://github.com/prisma/prisma-engines/blob/main/schema-engine/connectors/sql-schema-connector/src/flavour/postgres.rs)):
  - `CREATE TABLE _prisma_migrations (id VARCHAR(36) PRIMARY KEY NOT NULL, checksum VARCHAR(64) NOT NULL, finished_at TIMESTAMPTZ, migration_name VARCHAR(255) NOT NULL, logs TEXT, rolled_back_at TIMESTAMPTZ, started_at TIMESTAMPTZ NOT NULL DEFAULT now(), applied_steps_count INTEGER NOT NULL DEFAULT 0)`
  - `SELECT pg_advisory_lock(72707369)`, with a 10 second timeout.
- Order per migration ([apply_migrations.rs](https://github.com/prisma/prisma-engines/blob/main/schema-engine/commands/src/commands/apply_migrations.rs), [sql_migration_persistence.rs](https://github.com/prisma/prisma-engines/blob/main/schema-engine/connectors/sql-schema-connector/src/sql_migration_persistence.rs)):
  1. `record_migration_started`: INSERT of id, checksum, started_at, migration_name. Built with quaint, so likely parameterized.
  2. `apply_script`.
  3. `record_successful_step`: UPDATE `applied_steps_count = applied_steps_count + 1`.
  4. `record_migration_finished`: UPDATE `finished_at`.
  - On failure, `record_failed_step` writes `logs`.
- Transactions: on `main`, `apply_migration_script` splits the script and runs each piece with `client.simple_query(stmt)`. The code comment says simple protocol "automatically wraps the script in a transaction, which is sometimes undesirable". [native/mod.rs](https://github.com/prisma/prisma-engines/blob/main/schema-engine/connectors/sql-schema-connector/src/flavour/postgres/connector/native/mod.rs)
  - So there is no implicit transaction now. Bookkeeping writes happen outside the script.
  - Earlier behavior, per [issue #22922](https://github.com/prisma/orm/issues/22922) and [discussion #10601](https://github.com/prisma/prisma/discussions/10601): a single statement ran bare, and a multi-statement file got the Postgres implicit transaction.
- Offline: `migrate diff --from-... --to-... --script`, with `--from-migrations`, `--to-schema`, `--from-config-datasource` and so on (v7 CLI).

**Prisma 8 RC: `prisma db migrate`**
- The docs say "`migrate deploy` becomes `db migrate`". [how migrations work](https://www.prisma.io/docs/orm/migrations/how-migrations-work)
- Flag `--db <url>`; otherwise `db.connection` in `prisma.config.ts` is used. [db migrate](https://www.prisma.io/docs/cli/db-migrate)
- Layout: `migrations/app/<ts>_<name>/{migration.ts, ops.json, migration.json}`. Prisma runs `ops.json`.
- Bookkeeping: a "marker" and a "ledger" stored in the `prisma_contract` schema.
- Transactions: "The whole run is one transaction" on PostgreSQL. [applying a migration](https://www.prisma.io/docs/orm/migrations/applying-a-migration)
- Offline: `migration show <dir>` prints the SQL with no database connection.

### Summary table

| framework | env override | bookkeeping marker SQL | tx per migration | opt-out | protocol | offline SQL mode |
|---|---|---|---|---|---|---|
| Rails 8.1 | `DATABASE_URL`, `<NAME>_DATABASE_URL`; ignored if yml has `url:` | literal `INSERT INTO "schema_migrations" ("version") VALUES ('v')`, inside tx after DDL | yes, literal BEGIN/COMMIT | `disable_ddl_transaction!` | simple (`async_exec`); extended if binds or prepared | none found |
| Django 6.0 | none; generated settings module via `DJANGO_SETTINGS_MODULE`/`--settings` | ORM INSERT into `django_migrations(app,name,applied)`; **after COMMIT if deferred SQL exists** | yes, driver BEGIN | `atomic = False` | simple, client binding by default | `sqlmigrate` (needs a DB, BEGIN/COMMIT if atomic, RunPython is a comment) |
| Alembic 1.20 | none by default; `ALEMBIC_CONFIG`/`-c` ini; `-x` only if env.py reads it | literal INSERT, UPDATE or DELETE on `alembic_version.version_num`, inside tx after DDL | **no**, one tx per run by default | `transaction_per_migration`, `autocommit_block()` | psycopg2 simple; psycopg3 extended only when params | `upgrade --sql` (no DB) |
| Drizzle 0.45 / 1.0 rc | CLI `--url`/`--dialect` or config file | parameterized `insert into "drizzle"."__drizzle_migrations" ("hash","created_at"[, "name"]) values($1,$2[,$3])` | **no**, one tx for all pending | none | DDL simple; INSERT extended, text | not needed; read files |
| Prisma 7 | `datasource.url` in prisma.config.ts (usually `env("DATABASE_URL")`) | INSERT `_prisma_migrations` before the script, UPDATEs after | **no** on engines main (each statement alone) | n/a | script via `simple_query`; bookkeeping likely extended | `migrate diff --script` |
| Prisma 8 RC | `--db <url>` or `db.connection` | marker and ledger in `prisma_contract` schema | one tx per run | unverified | unverified | `migration show <dir>` |

### Unverified
- Whether the Rails `schema_migrations` INSERT ends with `RETURNING "version"`, and the exact `client_min_messages` and introspection SQL.
- The exact literal format of Django's `applied` timestamp. Also whether psycopg2 sends exactly the text `BEGIN`.
- SQLAlchemy dialect startup query texts. asyncpg behavior under Alembic's async template.
- Drizzle v1 `getMigrationsToRun` and `upgradeIfNeeded` SQL. Whether drizzle-kit allows `--url` together with a config file. postgres.js protocol behavior.
- Which Prisma 7.x release first shipped the statement-splitting `apply_migration_script`. Whether tokio-postgres sends parameters in binary. The exact quaint-rendered `_prisma_migrations` SQL. Whether checksum is sha256.
- Prisma 8 marker and ledger table names, columns and SQL, its lock mechanism, its driver and protocol, and any per-migration opt-out.
- The Postgres rule that a multi-statement simple query runs as one implicit transaction. I did not fetch the PostgreSQL protocol docs for this.


# Part 2: wire protocol proxy


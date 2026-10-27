I've finished the research report. It's below, and I didn't create or change any files. Every PostgreSQL claim comes from docs I fetched this session for versions 14, 17 and 18. The squawk and Eugene findings come from their GitHub sources, and strong_migrations, Atlas, GitLab and the framework findings from their official docs. Shell was denied, so nothing was run. I did not review pgroll or pg-osc.

Two findings change dbscope's design:
- **The parser can't read some PG18 syntax.** dbscope uses the PG17 grammar, so PG18-only migration syntax (virtual generated columns, `NOT NULL ... NOT VALID`, `NOT ENFORCED`) will likely fail to parse (§1.3).
- **PG14 support ends soon.** PG14's final release is listed for November 12, 2026 ([versioning policy](https://www.postgresql.org/support/versioning/)).

## Result

### 1. Lock, rewrite and transaction table (PG 14 to 18)

Abbreviations: AE = ACCESS EXCLUSIVE, E = EXCLUSIVE, SRE = SHARE ROW EXCLUSIVE, S = SHARE, SUE = SHARE UPDATE EXCLUSIVE, RS = ROW SHARE. "Txn OK" means the statement can run inside a transaction block.

**1.1 Ground rules from the docs**
- **Default lock is AE.** Any ALTER TABLE subform takes AE "unless explicitly noted". With several subcommands, the strictest lock wins ([ALTER TABLE 18, Description](https://www.postgresql.org/docs/18/sql-altertable.html)).
- **Combining actions saves passes.** Several actions in one ALTER TABLE share one scan or rewrite pass. RENAME, SET SCHEMA, ATTACH and DETACH can't be combined.
- **Rewrites are not MVCC-safe.** Concurrent transactions with older snapshots see the table as empty. A rewrite can temporarily need about twice the disk space ([Notes](https://www.postgresql.org/docs/18/sql-altertable.html#SQL-ALTERTABLE-NOTES)).
- **Only AE blocks plain SELECT.** The conflict matrix is Table 13.2 in [Explicit Locking](https://www.postgresql.org/docs/18/explicit-locking.html).

**1.2 Table**

| Statement | Lock on target | Locks on other tables | Rewrite / scan | Txn OK | 14 to 18 notes | Source |
|---|---|---|---|---|---|---|
| CREATE INDEX | S (blocks writes, not reads) | none | one table scan to build | yes | same | [CREATE INDEX](https://www.postgresql.org/docs/18/sql-createindex.html), [locking](https://www.postgresql.org/docs/18/explicit-locking.html) |
| CREATE INDEX CONCURRENTLY | SUE | none | two scans, plus waits for older transactions; a failure leaves an INVALID index that still enforces uniqueness | **no** | not supported on partitioned parents (build each partition, then `ON ONLY` parent) | CREATE INDEX |
| DROP INDEX | AE on the parent table | none | none | yes | same | [DROP INDEX](https://www.postgresql.org/docs/18/sql-dropindex.html) |
| DROP INDEX CONCURRENTLY | docs say it waits for conflicting transactions; exact lock level not stated | none | none | **no**; one index only; no CASCADE; not for partitioned indexes | same | DROP INDEX |
| ADD COLUMN, no default | AE | none | none | yes | same | ALTER TABLE Notes |
| ADD COLUMN, non-volatile default | AE | none | none (value stored in metadata) | yes | same (PG11+) | Notes |
| ADD COLUMN, volatile default (e.g. `clock_timestamp()`; serial is likely the same) | AE | none | **rewrites table and indexes** | yes | same | Notes |
| ADD COLUMN identity | AE | none | **rewrite** | yes | same | Notes |
| ADD COLUMN GENERATED STORED | AE | none | **rewrite** | yes | same | Notes |
| ADD COLUMN GENERATED VIRTUAL | AE | none | "never requires a rewrite" | yes | **PG18 only**; `VIRTUAL` isn't in the PG17 grammar | [18 Notes](https://www.postgresql.org/docs/18/sql-altertable.html#SQL-ALTERTABLE-NOTES) |
| ADD COLUMN with a constrained domain type | AE | none | **rewrite** | yes | same | Notes |
| ADD COLUMN NOT NULL with constant default | AE | none | none | yes | same; NOT NULL without a default fails on a non-empty table (Atlas MF103) | Notes, [Atlas](https://atlasgo.io/lint/analyzers) |
| DROP COLUMN | AE | none | none (column only hidden) | yes | same; needs CASCADE for views or FKs; drops indexes on the column | Notes |
| TYPE varchar(n) to varchar(m), m > n | AE | none | no rewrite per [strong_migrations](https://github.com/ankane/strong_migrations); the docs only state the general binary-coercible rule | yes | same | Notes |
| TYPE varchar to text (or back) | AE | none | no rewrite and no index rebuild (unless the collation changes) | yes | same | Notes (explicit example) |
| TYPE text to varchar(n) | AE | none | likely rewrite (length check). **Unverified** | yes | same | none |
| TYPE int to bigint | AE | none | **rewrite plus index rebuild** | yes | same | Notes |
| TYPE numeric precision increase, same scale | AE | none | no rewrite per strong_migrations; other changes rewrite | yes | same | strong_migrations |
| TYPE ... USING expr | AE | none | rewrite, unless USING leaves contents unchanged and the cast is binary-coercible | yes | same | Notes |
| SET NOT NULL | AE | none | full scan, **skipped if a valid CHECK proves no NULLs** | yes | 14 to 18 same; in 18 it also validates an invalid not-null constraint | [14](https://www.postgresql.org/docs/14/sql-altertable.html), [18](https://www.postgresql.org/docs/18/sql-altertable.html) |
| ADD CONSTRAINT ... NOT NULL col NOT VALID | AE (no lower level noted) | none | no scan; VALIDATE scans later under SUE | yes | **PG18 only**; 14 and 17 allow NOT VALID only for FK and CHECK | 17 vs 18 docs |
| DROP NOT NULL | AE | none | none | yes | same | ALTER TABLE |
| SET / DROP DEFAULT | AE (no lower level noted) | none | none (future rows only) | yes | same | ALTER TABLE |
| ADD FOREIGN KEY | SRE | **SRE on the referenced table** | scan; NOT VALID skips it | yes | in 14 and 17, FKs on partitioned tables can't be NOT VALID; 18 drops that sentence | 14, 17, 18 docs |
| ADD CHECK | AE | none | scan; NOT VALID skips it | yes | `NOT ENFORCED` in 18 skips verification | 18 Notes |
| ADD UNIQUE / PRIMARY KEY | AE | none | index build; a PK also scans for NULLs on nullable columns | yes | same | ALTER TABLE |
| ADD UNIQUE/PK USING INDEX | AE | none | fast; a PK on nullable columns triggers a SET NOT NULL scan | yes | not supported on partitioned tables | ALTER TABLE |
| ADD EXCLUDE | AE | none | index build; no NOT VALID option | yes | same | ALTER TABLE |
| VALIDATE CONSTRAINT | SUE | **RS on the referenced table** (FK case) | scan | yes | 18 adds not-null constraints | Notes |
| RENAME COLUMN / TABLE | AE | none | none; breaks clients | yes | same | ALTER TABLE |
| SET TABLESPACE | AE | none | copies the data files (cost grows with size); indexes are not moved | yes | same | ALTER TABLE |
| ATTACH PARTITION | SUE on the parent | AE on the attached table and on the default partition | scans the partition unless a matching valid CHECK exists; the default partition is also scanned | yes | same | ALTER TABLE |
| DETACH PARTITION | AE (no lower level noted) | **S on tables whose FKs reference the parent** | none | yes | same | ALTER TABLE |
| DETACH ... CONCURRENTLY | SUE on parent and partition, then SUE on parent plus AE on partition | as above | waits for transactions | **no**; not allowed with a default partition; `FINALIZE` resumes an interrupted detach | PG14+ | ALTER TABLE |
| VACUUM FULL | AE | none | **rewrite** | **no** | same | [VACUUM](https://www.postgresql.org/docs/18/sql-vacuum.html), locking |
| CLUSTER table | AE | none | **rewrite plus sort**; needs about table + indexes of free disk, up to twice the table with sort | yes for one plain table; **no** for bare `CLUSTER` or partitioned tables | same | [CLUSTER](https://www.postgresql.org/docs/18/sql-cluster.html) |
| REINDEX INDEX/TABLE | blocks writes on the table; AE on each index (blocks most reads, since the planner locks every index) | none | index rebuild | yes (no for partitioned, SCHEMA, DATABASE, SYSTEM) | same | [REINDEX](https://www.postgresql.org/docs/18/sql-reindex.html) |
| REINDEX CONCURRENTLY | SUE (session level) on index and table | none | two scans per index | **no** | same; skips exclusion-constraint indexes | REINDEX |
| CREATE TRIGGER | SRE | none | none | yes | same | locking |
| ALTER TYPE ... ADD VALUE (enum) | lock is on the type, level not documented. **Unverified** | none | none | yes, but the new value can't be used until commit | same | [ALTER TYPE](https://www.postgresql.org/docs/18/sql-altertype.html) |
| REFRESH MATERIALIZED VIEW | AE on the matview | none | full recompute | yes | same | locking, [REFRESH](https://www.postgresql.org/docs/18/sql-refreshmaterializedview.html) |
| REFRESH ... CONCURRENTLY | E (reads still allowed) | none | recompute plus diff | docs give no restriction | needs a plain unique index and an already-populated view | same |
| DROP TABLE | AE | lock on FK-referenced tables **unverified** | none | yes | same | locking |
| TRUNCATE | AE on each table, plus sequences with RESTART IDENTITY | CASCADE pulls in FK-referencing tables | no scan; not MVCC-safe | yes | same | [TRUNCATE](https://www.postgresql.org/docs/18/sql-truncate.html) |

**1.3 What dbscope should do**
- Key every rule on (statement subtype, PG major). Keep the version-specific rows in data, not in code: 18's virtual generated columns, `NOT NULL ... NOT VALID`, `NOT ENFORCED`, and partitioned FK `NOT VALID`.
- Run a parser check against the target version. With the PG17 grammar, PG18-only syntax will likely fail to parse. Report it as "unparseable for the configured grammar", not as a crash.
- Libraries built on libpg_query 18 appear to exist (pglast v8 says it covers PG18). I did not check whether a Go binding exists.

### 2. Lock queue and timeouts

**Why one waiting AE request blocks everyone.** Lock requests queue up. A new request that conflicts with a request already waiting in the queue also waits, even if it doesn't conflict with what is currently held.

[GitLab's style guide](https://docs.gitlab.com/development/migration_style_guide/) walks through the case:
1. Transaction 1 holds ROW EXCLUSIVE.
2. The migration's ALTER waits for AE.
3. New reads and writes that are compatible with transaction 1 now pile up behind the queued AE.

[Squawk](https://raw.githubusercontent.com/sbdchd/squawk/master/docs/docs/safe_migrations.md) describes the same outcome. A lock wait has no limit unless a deadlock occurs ([Explicit Locking 13.3.4](https://www.postgresql.org/docs/18/explicit-locking.html)).

**Timeout semantics** ([runtime-config-client](https://www.postgresql.org/docs/18/runtime-config-client.html)):
- `lock_timeout` applies to each lock acquisition separately.
- `statement_timeout` applies per statement, including each statement in a multi-statement simple query (since PG13). Setting `lock_timeout` at or above `statement_timeout` is pointless.
- `transaction_timeout` exists from PG17.
- `SET LOCAL` outside a transaction block "emits a warning and otherwise has no effect" ([SET](https://www.postgresql.org/docs/18/sql-set.html)).
- A `SET` is undone if its transaction rolls back.

**Published guidance:**

| Source | lock_timeout | statement_timeout | Retry |
|---|---|---|---|
| squawk | 1s | 5s | retry manually |
| Eugene E9 | `SET LOCAL lock_timeout = '2s'` | none given | retry |
| strong_migrations | 10s | 1h | optional `lock_timeout_retries` |
| GitLab `with_lock_retries` | short, e.g. `SET LOCAL lock_timeout '100ms'` | GitLab.com default is short | 50 attempts over about 40 min, then one final try without lock_timeout |

With PgBouncer in transaction mode, set the timeouts on the role. Session SETs aren't reliable there (strong_migrations).

**How the tools detect a missing timeout:**
- **Squawk (file level).** `require-lock-timeout` and `require-statement-timeout` (formerly `require-timeout-settings`) want a `SET` "at the beginning of your migration file" before any DDL. You can ignore them if the connection is already configured.
- **Eugene (statement level).** The lint context tracks state across the script. E9 fires only when a statement locks an object that is visible outside the transaction and no lock timeout is in effect ([rules.rs](https://raw.githubusercontent.com/kaaveland/eugene/main/eugene/src/lints/rules.rs)).
- **Atlas.** Recommends a statement-rule pattern ("Require lock_timeout before DDL") or a migration hook (`after_begin`).

**Recommendation for dbscope.** Model the GUC state per statement, as Eugene does:
1. Track SET, SET LOCAL, RESET, `set_config(...)`, and BEGIN/COMMIT/ROLLBACK/SAVEPOINT.
2. Treat SET LOCAL outside a transaction as having no effect, and flag it.
3. Accept outside sources of a timeout: a CLI flag, framework config (e.g. `StrongMigrations.lock_timeout`, an Atlas hook), and role or database settings read from `pg_db_role_setting` during introspection.
4. Fire only for locks that conflict with ROW EXCLUSIVE or ACCESS SHARE on objects that existed before the transaction.

### 3. Linter rule sets

**3.1 Squawk.** The rule list comes from [docs/sidebars.js](https://raw.githubusercontent.com/sbdchd/squawk/master/docs/sidebars.js). I read the full docs only for adding-field-with-default, adding-not-nullable-field, require-timeout-settings, require-lock-timeout and safe_migrations. The other "flags" entries below are paraphrased from README example output or inferred from the rule names.

| Rule | Flags | Safe alternative |
|---|---|---|
| adding-field-with-default | default that causes a rewrite; version-aware (11+ is safe for non-volatile) | add nullable, SET DEFAULT, backfill, SET NOT NULL; for generated columns use a trigger |
| adding-not-nullable-field | SET NOT NULL on an existing column | CHECK (...) NOT VALID, backfill, VALIDATE, then SET NOT NULL (12+) |
| adding-foreign-key-constraint, constraint-missing-not-valid | constraint added without NOT VALID | NOT VALID, then VALIDATE later |
| disallowed-unique-constraint | ADD UNIQUE (AE while the index builds) | `CREATE UNIQUE INDEX CONCURRENTLY`, then `USING INDEX` |
| require-concurrent-index-creation / -deletion | CREATE/DROP INDEX without CONCURRENTLY | add CONCURRENTLY |
| ban-concurrent-index-creation-in-transaction, transaction-nesting, ban-uncommitted-transaction | transaction misuse | `--assume-in-transaction` changes how files are modelled |
| changing-column-type, prefer-text-field, prefer-bigint-over-int/-smallint, prefer-identity, prefer-timestamptz, ban-char-field | type choices | text + CHECK; bigint; identity |
| renaming-column, renaming-table, renaming-object, ban-drop-* (column, table, database, default, not-null, index, constraint, schema, sequence, type, view, function, trigger, policy, extension, generated-expression, domain), ban-truncate-cascade | breaking or destructive changes | expand/contract |
| require-lock-timeout, require-statement-timeout | timeouts not set | SET at file start |
| require-concurrent-partition-detach, require-concurrent-reindex, prefer-repack | blocking maintenance | CONCURRENTLY, pg_repack |
| prefer-robust-stmts, require-enum-value-ordering, require-table-schema, identifier-too-long, ban-create/alter-domain-with-constraint, many newer ban-alter-* / ban-add-* rules, syntax-error, semantic-error | robustness and policy | not reviewed |

**3.2 strong_migrations** ([README](https://raw.githubusercontent.com/ankane/strong_migrations/master/README.md)). Checks and their "good" path:
- **Remove column:** set `ignored_columns`, deploy, then drop.
- **Change type:** six-step dual-write. Safe Postgres cases are listed: varchar limit increase, to text, numeric precision increase, timestamptz when the session is UTC.
- **Rename column or table:** dual-write.
- **Auto-increment column or stored generated column:** new table, or callbacks/triggers, or a virtual generated column.
- **Foreign key:** `validate: false`, then validate.
- **Check constraint:** `validate: false`, then validate.
- **Raw SQL:** `safety_assured`.
- **Non-concurrent index or reference:** `algorithm: :concurrently` plus `disable_ddl_transaction!`.
- **Unique constraint:** concurrent unique index, then `using_index`.
- **Exclusion constraint:** no safe way.
- **json column:** use jsonb.
- **Volatile default:** add the column, then change the default and backfill.
- **SET NOT NULL:** CHECK with `validate: false`, then validate, SET NOT NULL, drop the check.
- **Rename enum value or schema.**
- **Opt-in:** `remove_index`.
- **Options:** `target_version` makes checks version-aware. There are no size thresholds; the README notes that small projects may not need the gem.

**3.3 Eugene** ([hint_data.rs](https://raw.githubusercontent.com/kaaveland/eugene/main/eugene/src/hint_data.rs)):

| ID | Flags | Workaround |
|---|---|---|
| E1 | new constraint that is already VALID | NOT VALID, then VALIDATE |
| E2 | NULL changed to NOT NULL | CHECK NOT VALID, validate, then SET NOT NULL |
| E3 | json column | jsonb |
| E4 | another statement run while holding AE | run it in a new transaction |
| E5 | type change that isn't binary-compatible | new column, batch backfill, drop the old one |
| E6 | non-concurrent index on an existing table | CREATE INDEX CONCURRENTLY |
| E7 | new unique constraint | unique index concurrently, then USING INDEX |
| E8 | exclusion constraint | no safe way |
| E9 | dangerous lock taken without a timeout | `SET LOCAL lock_timeout='2s'`, retry |
| E10 | rewrite while holding a dangerous lock | build new, dual-write, swap |
| E11 | SERIAL or GENERATED STORED column | can't avoid the rewrite |
| W12 | several ALTERs where one would do | combine them |
| W13 | creating an enum | lookup table with FK |
| W14 | PK added USING INDEX | make the columns NOT NULL first |
| E15 | FK without a supporting index | create the index |

`eugene trace` runs the migration against a real database and inspects the locks it takes. `lint` skips objects that aren't visible to other transactions.

**3.4 Atlas lint** ([analyzers](https://atlasgo.io/lint/analyzers)):
- **BC101-104:** rename or drop of a table or column.
- **DS101-103:** drop schema, table, or non-virtual column.
- **MF101-104:** data-dependent changes (unique index, NOT NULL).
- **CD101-103:** constraint drops (Pro).
- **PG101-103:** missing CONCURRENTLY, and missing the `-- atlas:txmode none` file directive.
- **PG104/105/108/109/110:** PK/UNIQUE taking AE, index on a partitioned table, EXCLUDE, column alignment.
- **PG301-320 (Pro):** type rewrite, volatile default, NOT NULL scan, PK on nullable columns, CHECK scan, FK scan, LOGGED/UNLOGGED, trigger SRE, stored generated, identity, access method, PK redefinition, REPLICA IDENTITY, autovacuum disabled.
- **TX101/TX201:** statements that can't share a transaction; nested transactions.
- **NM:** naming. **SA101:** SQL injection.

**3.5 Where the tools disagree**
- **UNIQUE constraint lock.** Eugene E7 says it "blocks all writes". Squawk, strong_migrations, Atlas PG105 and the PG docs/source say AE, which blocks reads too.
- **varchar widening.** Squawk prefer-text-field warns because it takes AE. strong_migrations lists it as safe because there is no rewrite. Both are correct on different axes, so dbscope should report the lock and the rewrite separately.
- **Non-concurrent DROP INDEX.** On by default in squawk and Atlas PG102. Opt-in in strong_migrations.
- **Statement timeout.** Squawk suggests 5s, strong_migrations 1h. 5s would kill CREATE INDEX CONCURRENTLY on large tables.
- **Batching under one lock.** GitLab says to do as much as possible once you hold the lock. Eugene E4 says to start a new transaction.
- **Stored generated columns.** Squawk suggests a trigger. strong_migrations allows a virtual generated column (PG18). Eugene says there's no workaround.
- **NOT NULL on PG18.** No tool doc I read mentions the native `NOT NULL ... NOT VALID` path. All of them still teach the CHECK workaround.
- **Version-dependent rules:** adding-field-with-default (<11), the SET NOT NULL CHECK skip (12+), ADD VALUE in a transaction (12+), REINDEX CONCURRENTLY (12+), DETACH CONCURRENTLY (14+), and the PG18 items in §1.

### 4. Risk scoring

**Published size guidance:**
- **GitLab.com size limits** ([large tables](https://docs.gitlab.com/development/database/large_tables_limitations/)): no new index above 50 GB, no column with an FK above 50 GB, no new column above 100 GB.
- **GitLab size classes:** small under 10 GB, medium under 50 GB, large under 100 GB, over_limit above that ([handbook ADR 001](https://handbook.gitlab.com/handbook/engineering/architecture/design-documents/database_size_limits/decisions/001_table_classification/)).
- **GitLab duration targets:** regular migrations up to 3 min, post-deploy up to 10 min, concurrent index builds up to 20 min. Non-concurrent `remove_index` is fine below about 1,000 rows.
- strong_migrations and squawk publish no numeric thresholds.

**Proposed formula** (my proposal, not from any source):
- **L, lock class:** 0 = none / ACCESS SHARE / RS; 1 = SUE; 2 = S, SRE, E (blocks writes); 3 = AE.
- **W, work:** 0 = catalog only; 1 = scan or index build; 2 = rewrite.
- **Z, size tier,** from `pg_total_relation_size` or `relpages × block_size`: 0 under 10 MB, 1 under 1 GB, 2 under 10 GB, 3 under 50 GB, 4 at or above 50 GB. The top tiers follow GitLab's classes. If `reltuples` is -1 or there are no stats, use Z = 2.
- **T** = 1 if L ≥ 2 and no lock_timeout is in effect.
- **Q** = 1 if L ≥ 2 (lock-queue exposure even when the work is instant).
- **H** = 1 if AE is already held earlier in the same transaction (Eugene E4).
- **score** = L·W·Z + Q + 3T + 2H, computed for every table locked (including FK-referenced tables), keeping the maximum.
- **Bands:** Low 0 to 2, Medium 3 to 6, High 7 to 11, Critical 12 or more.

Examples: AE metadata-only with a timeout scores 1 (Low), without one 4 (Medium). AE rewrite at Z = 2 scores 13 (Critical). SET NOT NULL at Z = 3 scores 10 (High). VALIDATE at Z = 4 scores 4 (Medium).

**Overrides:**
- **Will fail → Critical / SARIF error:** CONCURRENTLY inside a transaction, ADD NOT NULL without a default on a table with rows, or syntax the target version can't parse.
- **Separate breaking axis:** drop or rename, type narrowing, new NOT NULL. Rate it at least High when dependents (§6) or `pg_stat_statements` hits exist.
- **Data loss:** DROP TABLE, DROP COLUMN, or TRUNCATE on a non-empty table is Critical unless suppressed.
- **Optional traffic bump:** +2 for "hot" tables, judged from `pg_stat_user_tables` scan and tuple counters.

### 5. Ingesting migration files

| Framework | Ordering | Transaction behavior | How dbscope detects it |
|---|---|---|---|
| Flyway | `V<version>__<desc>.sql`, numeric version sort ([docs](https://documentation.red-gate.com/flyway/flyway-concepts/migrations/versioned-migrations)) | one transaction per migration; auto-marks files that can't run in a transaction; `group`/`mixed` settings; `executeInTransaction` per-script config ([docs](https://documentation.red-gate.com/flyway/flyway-concepts/migrations/migration-transaction-handling)) | read the `.conf` sidecar; detect CONCURRENTLY. A Flyway transactional-lock hang with CREATE INDEX CONCURRENTLY is reported ([issue #3854](https://github.com/flyway/flyway/issues/3854)) |
| golang-migrate | `{version}_{title}.up.sql` / `.down.sql` | each file is sent as one Exec, so a multi-statement file runs in an implicit transaction; `x-multi-statement` splits it ([README](https://raw.githubusercontent.com/golang-migrate/migrate/master/database/postgres/README.md)) | count statements; read the URL params |
| Rails | timestamped `.rb` files, Ruby DSL | wrapped in a transaction unless `disable_ddl_transaction!` | regex the `.rb` file. `structure.sql` is a resulting schema dump, not migration SQL, so the SQL must be captured from a shadow run |
| Django | dependency graph, not filenames ([docs](https://docs.djangoproject.com/en/5.2/howto/writing-migrations/)) | atomic by default on PG; `atomic = False` | `sqlmigrate` output (BEGIN/COMMIT appears when atomic, unverified) |
| Prisma | `migrations/<ts>_<name>/migration.sql` | a single statement runs bare; multiple statements run in one transaction ([issue #22922](https://github.com/prisma/orm/issues/22922)) | statement count. Newer Prisma appears to rename commands ("migration plan", "db migrate"). **Unverified** |
| Alembic | revision graph | `transaction_per_migration`; `autocommit_block()` commits first ([docs](https://alembic.sqlalchemy.org/en/latest/api/runtime.html)) | `--sql` offline output |
| Atlas | versioned files plus `atlas.sum`, linear by default | `--tx-mode file\|all\|none`; `-- atlas:txmode none` per file ([docs](https://atlasgo.io/versioned/apply)) | read the directive |
| sqitch | `sqitch.plan` | scripts usually contain their own BEGIN/COMMIT. **Unverified** | parse the plan |

The root mechanism is the PG protocol rule. A multi-statement simple Query runs in an implicit transaction block, and an explicit BEGIN/COMMIT changes that ([protocol 54.2.2.1](https://www.postgresql.org/docs/18/protocol-flow.html)).

dbscope should give each statement a transaction context of `{none, implicit, explicit, framework}`. It should combine three inputs:
1. The framework profile.
2. File directives and sidecars.
3. The parsed BEGIN/COMMIT statements and the statement count.

### 6. Downstream impact queries

These are sketches. I wrote them from catalog knowledge and did not execute them.

```sql
-- Views and matviews on a table or column (one level; recurse on result oids)
SELECT DISTINCT r.ev_class::regclass AS dependent, c.relkind
FROM pg_depend d
JOIN pg_rewrite r ON d.classid='pg_rewrite'::regclass AND r.oid=d.objid
JOIN pg_class c ON c.oid=r.ev_class
WHERE d.refclassid='pg_class'::regclass AND d.refobjid=$1::regclass
  AND ($2::int2 IS NULL OR d.refobjsubid=$2) AND r.ev_class<>$1::regclass;

-- SQL-standard function bodies (PG14+), policies, generated-column defaults, expression indexes
SELECT d.classid::regclass, d.objid, d.deptype
FROM pg_depend d
WHERE d.refclassid='pg_class'::regclass AND d.refobjid=$1::regclass
  AND ($2::int2 IS NULL OR d.refobjsubid=$2)
  AND d.classid IN ('pg_proc'::regclass,'pg_policy'::regclass,'pg_attrdef'::regclass,'pg_class'::regclass);

-- FK children
SELECT conrelid::regclass, conname, confkey FROM pg_constraint
WHERE contype='f' AND confrelid=$1::regclass AND ($2::int2 IS NULL OR $2 = ANY(confkey));

-- Triggers (column list in tgattr; the function body is opaque)
SELECT tgname, tgfoid::regproc, tgattr FROM pg_trigger WHERE tgrelid=$1::regclass AND NOT tgisinternal;

-- Plain-column indexes
SELECT indexrelid::regclass FROM pg_index WHERE indrelid=$1::regclass AND $2 = ANY(indkey);
```

**Limits:**
- Only SQL-standard bodies (`BEGIN ATOMIC` / `RETURN`) record dependencies in pg_depend. String bodies, including all PL/pgSQL and dynamic SQL, are opaque ([CREATE FUNCTION](https://www.postgresql.org/docs/18/sql-createfunction.html)).
- For opaque bodies, fall back to a text or plpgsql parse of `prosrc` and label the result "heuristic".

**pg_stat_statements:**
- Only superusers and members of `pg_read_all_stats` can see other users' query text ([docs](https://www.postgresql.org/docs/18/pgstatstatements.html)).
- Texts are normalized and may be NULL if the text file was discarded.
- Filter on `dbid`. Parse each text with pg_query and match RangeVar/ColumnRef nodes. Unqualified columns and `search_path` make matches approximate.

### 7. SARIF 2.1.0 and GitHub

```json
{"$schema":"https://json.schemastore.org/sarif-2.1.0.json","version":"2.1.0",
 "runs":[{"tool":{"driver":{"name":"dbscope","semanticVersion":"0.4.0",
   "rules":[{"id":"DBS-AE-REWRITE","name":"AccessExclusiveRewrite",
     "shortDescription":{"text":"Statement rewrites a large table under ACCESS EXCLUSIVE"},
     "help":{"markdown":"Add a new column, backfill in batches, swap."},
     "defaultConfiguration":{"level":"error"},
     "properties":{"tags":["postgres","migration"],"precision":"high","problem.severity":"error"}}]}},
  "automationDetails":{"id":"dbscope/migrate-check/"},
  "results":[{"ruleId":"DBS-AE-REWRITE","ruleIndex":0,"level":"error",
    "message":{"text":"ALTER COLUMN TYPE bigint rewrites orders (42 GB). Rating: Critical."},
    "locations":[{"physicalLocation":{"artifactLocation":{"uri":"db/migrations/0042_orders.sql"},
      "region":{"startLine":3,"startColumn":1}}}],
    "partialFingerprints":{"primaryLocationLineHash":"<stable hash>"}}]}]}
```

**GitHub requirements** ([SARIF support](https://docs.github.com/en/code-security/code-scanning/integrating-with-code-scanning/sarif-support-for-code-scanning), [upload](https://docs.github.com/en/code-security/code-scanning/integrating-with-code-scanning/uploading-a-sarif-file-to-github)):
- **Version and values:** only SARIF 2.1.0 is accepted, and required properties can't be empty strings.
- **Locations:** results need at least one location, and only the first is used. Use repo-relative URIs.
- **Levels:** valid values are note, warning and error. Map Critical and High to error, Medium to warning, Low to note.
- **Upload:** use `github/codeql-action/upload-sarif@v4` with `sarif_file` and an optional `category`. The job needs the `security-events: write` permission.
- **Size:** at most 10 MB gzip-compressed per file.
- **Multiple files:** several files for the same commit need distinct categories or distinct `runAutomationDetails.id` values.
- **Fingerprints:** the action computes `partialFingerprints` if you omit them.
- **PR display:** an alert appears in a PR check only if all its lines are in the diff.
- **Private repos:** code scanning needs GitHub Code Security enabled.

**Annotations alternative** ([workflow commands](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands)): `::error file={path},line={n},endLine={n},col={c},endColumn={c},title={t}::{message}`. `warning` and `notice` use the same parameters.

### Unverified
- pgroll and pg-osc were not reviewed.
- Lock level for DROP INDEX CONCURRENTLY, for ALTER TYPE ADD VALUE, and for DROP TABLE on FK-referenced tables.
- No rewrite for varchar widening and numeric precision increases (source: strong_migrations, not the PG docs). A rewrite for text to varchar(n).
- Whether serial behaves like a volatile default (it adds a column with a `nextval()` default).
- The exact PG version where FK `NOT VALID` on partitioned tables became allowed. The docs show it disallowed in 17 and the sentence gone in 18.
- Whether VIRTUAL is the default for generated columns in 18.
- `reltuples = -1` meaning "never analyzed" (believed PG14+).
- GitHub's numeric per-object SARIF limits and per-step annotation caps. The pages I fetched didn't show the numbers.
- SARIF's default column unit (believed UTF-16 code units). This matters when converting libpg_query byte offsets to columns.
- Sqitch BEGIN/COMMIT conventions, Django sqlmigrate output, and the newer Prisma command names.
- The lock-queue rule is cited from GitLab and squawk. I did not cite PostgreSQL's lmgr README.
- The scoring formula and size tiers below 10 GB are my own proposal.

```json
{"status":"done","summary":"Collected primary-source findings for dbscope Phase 4: a per-DDL lock/rewrite/transaction table built from PostgreSQL 14, 17 and 18 docs, with version deltas (PG18 virtual generated columns, NOT NULL NOT VALID, NOT ENFORCED, partitioned FK NOT VALID; DETACH CONCURRENTLY since 14); lock-queue and timeout semantics from the PG runtime-config and SET docs; full rule lists for Eugene (source), squawk (sidebar list plus selected docs), strong_migrations (README) and Atlas (analyzers page), with the disagreements between them; GitLab size and duration thresholds plus a proposed deterministic scoring formula; framework transaction detection (Flyway, golang-migrate, Rails, Django, Prisma, Alembic, Atlas) grounded in PG simple-query implicit-transaction semantics; pg_depend-based dependency queries; and a minimal SARIF document with GitHub upload requirements. pgroll and pg-osc were not reviewed. The PG17 grammar in go-pgquery likely cannot parse PG18-only syntax.","artifacts":[],"tests":{"command":"","passed":null,"output_tail":""},"claims_to_verify":["libpg_query PG17 grammar (pg_query_go v6) fails to parse GENERATED ... VIRTUAL and ADD CONSTRAINT ... NOT NULL col NOT VALID","PG18 allows NOT VALID foreign keys on partitioned tables (sentence present in 17 docs, absent in 18)","varchar(n) widening and numeric precision increase at the same scale do not rewrite (strong_migrations claim)","text to varchar(n) forces a rewrite","DROP TABLE takes ACCESS EXCLUSIVE on FK-referenced tables","DROP INDEX CONCURRENTLY lock level","reltuples = -1 means never analyzed on PG14+","Catalog SQL in section 6 runs correctly on PG14 to 18","Explicit-locking page lists REINDEX under ACCESS EXCLUSIVE while the REINDEX page says writes are blocked on the table and AE is taken on the index","PG14 final release date November 12, 2026 per postgresql.org versioning page","SARIF default columnKind is utf16CodeUnits"],"open_questions":["Should dbscope target a libpg_query 18 binding to support PG18 syntax, or report PG18-only syntax as unparseable?","Should the size-tier thresholds and throughput assumptions be configurable per deployment?","Should breaking and data-loss risk be a separate SARIF rule family from lock risk?"]}
```
# Research: workload insights from pg_stat_statements

Collected 2026-10-28 by a research worker from the PostgreSQL docs and source (REL_14 to REL_18) and the cited tools' docs and source; the design dbscope adopted is in docs/WORKLOAD.md. Claims it could not verify are listed at the end.


### 1. pg_stat_statements columns, PG14 to PG18

**Each server major ships a different default extension version.** The control files at REL_*_STABLE set `default_version` to `1.9` on PG14 and `1.12` on PG18. PG16 is `1.10`. From the upgrade scripts, PG15 appears to ship `1.10` and PG17 `1.11`.

The columns a database actually exposes depend on its installed `extversion`, not on the server major. A pg_upgrade without `ALTER EXTENSION pg_stat_statements UPDATE` leaves the old column set in place. Datadog's agent says this in its source and discovers columns with `SELECT * ... LIMIT 0` ([statements.py](https://raw.githubusercontent.com/DataDog/integrations-core/master/postgres/datadog_checks/postgres/statements.py)). Azimutt checks whether `toplevel` exists before using it ([history.ts](https://raw.githubusercontent.com/azimuttapp/azimutt/main/libs/connector-postgres/src/history.ts)).

**Columns present in 1.9 through 1.12 (safe across PG14 to 18):**
`userid`, `dbid`, `toplevel`, `queryid`, `query`, `plans`, `total_plan_time`, `min_plan_time`, `max_plan_time`, `mean_plan_time`, `stddev_plan_time`, `calls`, `total_exec_time`, `min_exec_time`, `max_exec_time`, `mean_exec_time`, `stddev_exec_time`, `rows`, `shared_blks_hit`, `shared_blks_read`, `shared_blks_dirtied`, `shared_blks_written`, `local_blks_hit`, `local_blks_read`, `local_blks_dirtied`, `local_blks_written`, `temp_blks_read`, `temp_blks_written`, `wal_records`, `wal_fpi`, `wal_bytes`.

**Changes by version:**

| Ext version (server) | Columns added or renamed |
|---|---|
| 1.9 (PG14) | Has `blk_read_time` and `blk_write_time`. No jit columns. ([docs 14](https://www.postgresql.org/docs/14/pgstatstatements.html)) |
| 1.10 (PG15, PG16) | Adds `temp_blk_read_time`, `temp_blk_write_time`, `jit_functions`, `jit_generation_time`, `jit_inlining_count`, `jit_inlining_time`, `jit_optimization_count`, `jit_optimization_time`, `jit_emission_count`, `jit_emission_time`. ([1.9--1.10.sql](https://raw.githubusercontent.com/postgres/postgres/REL_15_STABLE/contrib/pg_stat_statements/pg_stat_statements--1.9--1.10.sql)) |
| 1.11 (PG17) | Renames `blk_read_time` to `shared_blk_read_time` and `blk_write_time` to `shared_blk_write_time`. Adds `local_blk_read_time`, `local_blk_write_time`, `jit_deform_count`, `jit_deform_time`, `stats_since`, `minmax_stats_since`. `pg_stat_statements_reset` gains a `minmax_only` argument and now returns `timestamptz`. ([1.10--1.11.sql](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/contrib/pg_stat_statements/pg_stat_statements--1.10--1.11.sql), [PG17 release notes](https://www.postgresql.org/docs/release/17.0/)) |
| 1.12 (PG18) | Adds `wal_buffers_full`, `parallel_workers_to_launch`, `parallel_workers_launched`. ([1.11--1.12.sql](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/contrib/pg_stat_statements/pg_stat_statements--1.11--1.12.sql), [docs 18](https://www.postgresql.org/docs/18/pgstatstatements.html)) |

`pg_stat_statements_info` has had the same two columns, `dealloc` and `stats_reset`, since PG14.

**How to write one query per version range:**
1. Find the extension's schema and version: `SELECT n.nspname, e.extversion FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'pg_stat_statements'`.
2. Keep three SQL templates keyed on `extversion`: `1.9`/`1.10`, `1.11`, and `1.12`. Each selects the common columns plus that range's extras, aliased to stable names (for example `blk_read_time AS shared_blk_read_time`).
3. As a safety net, list the real columns once from `pg_attribute` for the view, the way Datadog and Azimutt do. This handles `1.8`, which has no `toplevel`.
4. Always schema-qualify the call, using the schema from step 1.

### 2. Access, failure modes, and replicas

**Privileges:**
- The view is granted to PUBLIC, so any role can read the stats.
- Query text and `queryid` for other users' rows are shown only to superusers and roles with the privileges of `pg_read_all_stats` ([docs 18](https://www.postgresql.org/docs/18/pgstatstatements.html)).
- In the source, those rows get `queryid` NULL and `query` set to the literal string `<insufficient privilege>`, or NULL when `showtext` is false. Rows for the role's own statements stay fully visible ([pg_stat_statements.c](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/contrib/pg_stat_statements/pg_stat_statements.c)).
- `pg_monitor` includes `pg_read_all_stats`. pganalyze and Azimutt both recommend `GRANT pg_monitor` ([pganalyze](https://pganalyze.com/docs/install/self_managed/01_create_monitoring_user), [Azimutt Inspector](https://inspector.azimutt.app/)).

**Detecting each failure mode:**

| Condition | How to detect |
|---|---|
| Extension not created in this DB | `pg_extension` returns no row. Selecting the view raises SQLSTATE `42P01` (`relation "pg_stat_statements" does not exist`). |
| Created but not preloaded | SQLSTATE `55000` (ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE), message `pg_stat_statements must be loaded via "shared_preload_libraries"`. Both the view function and `pg_stat_statements_info` raise it (source). Do not rely on reading `shared_preload_libraries`. It is flagged `GUC_SUPERUSER_ONLY` ([guc_tables.c](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/utils/misc/guc_tables.c)), so treat the error as the signal. |
| Loaded but not collecting | `pgss_store` returns early when the query ID is 0, which happens when `compute_query_id` is off. The view stays empty or stale. Check `current_setting('compute_query_id')` and `current_setting('pg_stat_statements.track', true)`, where `none` disables collection. |
| No text privilege | `pg_has_role('pg_read_all_stats','USAGE')` is false, or rows have `queryid IS NULL`. Filter those rows out of attribution as Azimutt does, but still count them in totals. |

**Hot standby replicas:**
- The hot standby docs say pg_stat_statements works on standbys ([hot-standby](https://www.postgresql.org/docs/18/hot-standby.html)).
- Its counters live in each server's own shared memory. The source removes the persisted stats file so it is not copied to standbys. So a replica appears to have its own independent stats covering only queries run on the replica.
- The extension object comes from the primary through WAL. `shared_preload_libraries` must still be set on the standby itself. I inferred this from the design, not from an explicit doc statement.
- `pg_stat_reset`-style calls and `CREATE EXTENSION` cannot run on a standby.
- WAL replay does not increment `pg_stat_all_tables` counters such as `n_tup_ins` (hot-standby docs). Write activity is therefore invisible on a replica.
- dbscope should check `pg_is_in_recovery()` and label every number "replica-local reads only".

### 3. Cost and safety of reading

- **Size:** `pg_stat_statements.max` defaults to 5000 and can only be set at server start. The view returns at most that many rows across all databases.
- **When the table fills:** the least-used 5% of entries (minimum 10) are evicted, and `dealloc` goes up by 1 per eviction batch, not per entry (source, `USAGE_DEALLOC_PERCENT 5`).
- **Locking:** a SELECT holds the shared LWLock while it scans every entry in the hash table. That blocks creation of new entries for the duration (source comment).
- **WHERE clauses don't save work:** the scan covers every database. A `WHERE dbid = ...` filter does not reduce it.
- **Query text cost:** with `showtext` true, the backend loads the whole external query-text file into memory. `pg_stat_statements(false)` skips that step. The docs recommend it for tools that poll repeatedly and cache texts themselves ([docs 18](https://www.postgresql.org/docs/18/pgstatstatements.html)). Datadog uses `pg_stat_statements(false)` for counts.
- **pganalyze's experience:** loading the text every 10 minutes is expensive and can exhaust the collector's memory ([pganalyze troubleshooting](https://pganalyze.com/docs/install/troubleshooting/reset_stat_statements)).
- **Reading does not reset anything.** Only `pg_stat_statements_reset()` resets, and it is superuser-only by default.
- **Other tools may reset:** PgHero's `capture_query_stats` resets pg_stat_statements after each capture ([query_stats.rb](https://raw.githubusercontent.com/ankane/pghero/master/lib/pghero/methods/query_stats.rb)). pganalyze can be configured to reset automatically. Resets are cluster-wide, so another tool's reset will make dbscope's totals drop.
- **What `stats_reset` means:** it is set when shared memory is first created. It is restored from the dump file after a clean restart with `pg_stat_statements.save` on. It changes only on a full reset (source). After a crash, it effectively equals the server start time.
- **Per-entry age:** on PG17+, `stats_since` gives each entry's own age. Before PG17, an entry can be newer than `stats_reset` because it was evicted and re-added.
- **How to tell stats are too fresh to trust:**
  - `now() - stats_reset` is short. The threshold is a product choice; something like under 24h is a reasonable start.
  - `dealloc` grew between two polls, meaning churn.
  - The row count is near `pg_stat_statements.max`.
  - `stats_since` is recent for the specific entry (PG17+).
  - Literal constants appear in query texts. The docs say this happens more often when deallocation is high.

### 4. Mapping normalized queries to tables and columns

**Parsing with libpg_query:**
- `$n` is ordinary Postgres grammar (ParamRef), so normalized DML parses wherever a constant is allowed.
- libpg_query added special cases for pg_stat_statements output: `$1 FROM $2`, `$1ORc=$2` with trailing junk, and leaving TypeName typmods unnormalized "to avoid parsing errors on the normalized statement". It dropped `?` placeholder support in 14-3.0.0 ([CHANGELOG](https://raw.githubusercontent.com/pganalyze/libpg_query/17-latest/CHANGELOG.md)).
- PgHero parses pg_stat_statements text with `PgQuery.parse`. It skips `<insufficient privilege>` texts, texts over 10000 chars, parse errors, and anything that is not exactly one statement ([suggested_indexes.rb](https://raw.githubusercontent.com/ankane/pghero/master/lib/pghero/methods/suggested_indexes.rb)).

**Edge cases:**
- **PG18 list squashing:** constant lists collapse to `IN ($1 /*, ... */)`. The comment is ignored by the parser, so it should parse ([docs 18](https://www.postgresql.org/docs/18/pgstatstatements.html)). Do not infer list length.
- **Utility statements:** PG14 and PG15 compared them by exact text, so literals were kept and created many entries ([docs 14](https://www.postgresql.org/docs/14/pgstatstatements.html)). PG16 normalizes constants in utility commands ([PG16 notes](https://www.postgresql.org/docs/release/16.0/)). PG17 also replaces CALL parameters, savepoint names, 2PC GIDs, and DEALLOCATE names with placeholders ([PG17 notes](https://www.postgresql.org/docs/release/17.0/)). Some of these placeholder positions may not be valid grammar (unverified). Treat a utility parse failure as "skip" and track the failure rate.
- **Multi-statement strings:** `pgss_store` calls `CleanQuerytext` to keep only the current statement of a multi-statement string (source). Entries should be single statements, but still guard for more than one.
- **Truncation:** pg_stat_statements stores full text in a file and does not truncate. Tools may: PgHero applies `LEFT(query, 10000)`. Text can also be NULL after text-file garbage collection or when `showtext` is false.

**How existing tools attribute queries to tables:**
- **pganalyze:** queries are parsed and "an internal association is stored" for per-table lookup. Each query is also broken into scans and shown in a table's "Queries" tab ([schema statistics](https://pganalyze.com/docs/schema-statistics)).
- **pg_stat_monitor:** computes it in the server. It has a `relations text[]` column, "the list of tables involved in the query" ([reference](https://docs.percona.com/pg-stat-monitor/reference.html)).
- **Datadog DBM:** the agent's SQL obfuscator returns metadata `tables`, `commands`, and `comments` (`collect_tables` defaults to true). This is lexer-based, not catalog-resolved ([conf.yaml.example](https://raw.githubusercontent.com/DataDog/integrations-core/master/postgres/datadog_checks/postgres/data/conf.yaml.example), statements.py).
- **PgHero:** single-table extraction only, used for index suggestions. JOINs are "not supported yet".

**Pitfalls:**
- **search_path and unqualified names:** before PG18, the same text under different search_paths becomes separate entries. Their texts are identical, so text alone cannot tell you the schema. `userid` and each role's configured search_path can help. PG18 groups queries on the same relation name even across schemas with different columns ([PG18 notes](https://www.postgresql.org/docs/release/18.0/)). One entry can then cover `a.t` and `b.t`. Report these as "ambiguous schema" rather than guessing.
- **queryid is not stable** across major versions, or across a function being dropped and recreated (docs). Key history on (dbid, userid, queryid, toplevel) and accept that keys will churn.
- **Views:** a query names the view, not its base tables. Resolve them through `pg_depend` and `pg_rewrite`.
- **CTEs:** a CTE name can shadow a table name. Resolve names by scope.
- **Functions and procedures:** with `track = top` (the default), statements inside function bodies are invisible. Tables used only through functions will show no pg_stat_statements heat. With `track = all`, nested rows have `toplevel` false. Top-level time already includes nested time, so filter `toplevel` to avoid double counting.
- **`SELECT *`:** counts as reading all columns. Mark it as lower confidence for column usage.
- **Aliases:** a different table alias produces a different entry (docs). Merging by table is still fine.

### 5. Per-table and per-index stats

- **`pg_stat_user_tables`:** `seq_scan`, `seq_tup_read`, `idx_scan`, `idx_tup_fetch`, `n_tup_ins`, `n_tup_upd`, `n_tup_del`, `n_tup_hot_upd`, `n_live_tup`, `n_dead_tup`. PG16 added `last_seq_scan`, `last_idx_scan`, and `n_tup_newpage_upd` ([monitoring-stats 16](https://www.postgresql.org/docs/16/monitoring-stats.html)). PG18 adds `total_vacuum_time`, `total_autovacuum_time`, `total_analyze_time`, and `total_autoanalyze_time`.
- **`pg_stat_user_indexes`:** `idx_scan` (plus `last_idx_scan` in PG16+), `idx_tup_read`, `idx_tup_fetch`.
- **`pg_statio_user_tables`:** `heap_blks_read`, `heap_blks_hit`, `idx_blks_read`, `idx_blks_hit`. PgHero uses these for its hit rates ([tables.rb](https://raw.githubusercontent.com/ankane/pghero/master/lib/pghero/methods/tables.rb)).
- **Freshness:** these counters lag up to about 1s and are cached for the rest of the transaction. They are reset after a crash or PITR (monitoring-stats). PgHero reads the reset time with `pg_stat_get_db_stat_reset_time(oid)` ([indexes.rb](https://raw.githubusercontent.com/ankane/pghero/master/lib/pghero/methods/indexes.rb)).
- **PgHero's heuristics:**
  - Index usage percent is `100 * idx_scan / (seq_scan + idx_scan)`.
  - A table counts as missing an index if that is under 95% with `n_live_tup >= 10000`.
  - An index counts as unused if it is non-unique with `idx_scan <= 50` (configurable).
  - A table counts as unused if `idx_scan = 0`.
  - I found no "most active tables" score in PgHero.

**What a hotness score should combine.** This is my recommendation, not taken from any of these tools. Use deltas between two snapshots, never raw totals:
- Read activity: `seq_scan + idx_scan` per minute and `seq_tup_read + idx_tup_fetch` per minute.
- Write activity: `n_tup_ins + n_tup_upd + n_tup_del` per minute.
- Workload: calls/min and exec ms/min of the pg_stat_statements queries that reference the table.

Show each metric as a log-scale percentile across tables. Let the user choose the metric instead of hard-coding a weighted sum. Table stats are the fallback when pg_stat_statements is unavailable. They also catch access that happens inside functions.

### 6. UX patterns in existing tools

- **pganalyze:** per-table "Queries" tab (queries plus scans with WHERE and JOIN clauses), per-table index list with Index Write Overhead, per-index usage over time and last-scanned date ([schema statistics](https://pganalyze.com/docs/schema-statistics)). Query stats are collected every minute and sent every 10 minutes. Lists default to sorting by percent of total runtime. Percentiles are approximated from totals and calls, and pganalyze notes they understate outliers ([query performance](https://pganalyze.com/docs/query-performance)).
- **PgHero:** a top-queries list with total time, average time, calls, and percent of total. Historical views only work if capture is enabled, and capture resets the stats. It also has index usage, unused index, and unused table lists. It has no schema diagram.
- **Supabase:** CLI inspect commands include `seq-scans`, `unused-indexes`, `index-usage`, and `outliers`. Its docs warn that pg_stat_statements counters are cumulative since the last reset and do not describe an arbitrary window, so compare saved snapshots ([inspect](https://supabase.com/docs/guides/database/inspect)). The Studio "Query Performance Report" has an indexes tab powered by index_advisor ([index_advisor](https://supabase.com/docs/guides/database/extensions/index_advisor)).
- **Datadog:** tracks the top 200 normalized queries by total time per 10s interval and rolls the rest into "Other Queries" ([data collected](https://docs.datadoghq.com/database_monitoring/data_collected/)). It diffs successive snapshots to get rates (`compute_derivative_rows` in statements.py).
- **Azimutt:** its connector reads top-level, visible pg_stat_statements rows ordered by total exec time (history.ts). How it maps those queries onto its ERD: unverified.
- **DataGrip, DBeaver:** unverified. I found no primary source showing workload on their ERDs.
- **Common pitfalls:** totals since reset presented as rates; another tool's reset making counters drop; replica-local stats; entries evicted under churn.

### Recommended design for dbscope

1. **Probe once per connection.**
   - Check `pg_is_in_recovery()`.
   - Read the extension schema and `extversion` from `pg_extension`.
   - Check `pg_has_role('pg_read_all_stats','USAGE')`, `compute_query_id`, `pg_stat_statements.track`, and `pg_stat_statements.max`.
   - Try `pg_stat_statements_info` and map `42P01` and `55000` to clear UI messages.
   - Choose the 1.9/1.10, 1.11, or 1.12 template, and confirm columns against `pg_attribute`.
2. **Poll stats.**
   - Every 60s while the workload panel is open, otherwise on demand, call `<schema>.pg_stat_statements(false)` filtered to the current dbid with `toplevel`.
   - In the same poll, read `pg_stat_user_tables`, `pg_stat_user_indexes`, `pg_statio_user_tables`, and `pg_stat_statements_info`.
   - Fetch text with `showtext` true only for unseen `queryid`s, or every 10 minutes at most. Cache texts locally.
   - Never reset.
3. **Compute rates.**
   - Diff consecutive snapshots per key.
   - Treat any negative delta, a changed `stats_reset`, or a changed `stats_since` as a reset. Drop that interval.
   - With only one snapshot, show "average since `stats_reset`/`stats_since`" and label it that way.
4. **Attribute queries to tables.** Parse each text once with libpg_query and cache the result by queryid. Collect RangeVars, minus CTE names. Resolve each through the user's search_path to an OID, and resolve views through pg_depend. If a name resolves to more than one relation, or on PG18 relation-name grouping, mark it ambiguous.
5. **Attribute columns.**
   - Resolve ColumnRefs against the FROM scope.
   - Classify each use as filter/join, projection, write target, or order/group.
   - Flag `*` as wildcard usage.
   - Mark parse failures and `<insufficient privilege>` rows as "unattributed" and show their share of total calls.
6. **What to show.**
   - ERD heat from table-stat deltas, which works without pg_stat_statements. Offer a metric picker (reads, writes, query calls, query time) on a log or percentile scale.
   - A per-table top-queries list by calls/min and ms/min.
   - Migration warnings like "ALTER rewrites `t` (≈N calls/min, M writes/min over last X min)" and "dropped column `c` is referenced by K queries (+J via `*`)".
   - Always show the window, stats age, replica status, dealloc churn, and the unattributed share.

### Unverified

- Whether PG16+ normalized utility texts with placeholders (for example SET values, `SAVEPOINT $1`, `CALL p($1)`) parse with libpg_query's PG17 grammar.
- That a standby needs its own `shared_preload_libraries`, and that its pg_stat_statements starts empty. Both are inferred from design and source comments.
- Whether `compute_query_id` and `pg_stat_statements.*` settings are readable by non-superusers. I did not check their GUC flags.
- How Supabase Studio's Query Performance Report lays out its tabs and time windows. Azimutt's mapping of queries onto its ERD. Whether DataGrip or DBeaver show any workload on ERDs.
- That `pg_monitor` includes `pg_read_all_stats`. This is from memory and was not re-fetched.
- `information_schema.view_table_usage` restrictions. I did not rely on them.


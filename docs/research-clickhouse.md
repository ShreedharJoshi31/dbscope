# Research: ClickHouse support

Collected 2026-10-28 by a research worker; the design dbscope adopted, and what testing showed, is in docs/ENGINES.md.

Collected 2026-10-28 from primary sources: clickhouse.com/docs, the ClickHouse source on GitHub (master), the clickhouse-go repo and Go module proxy, and the Docker Hub API. Facts carry a reference link, and the URL list is at the end. Claims without a primary source are marked "unverified". Lines marked **design** are proposals for dbscope, not facts about ClickHouse. Content from the cited pages was rephrased for compliance with licensing restrictions.

**Versions seen on the research date**
- **Server:** the Docker `latest` and `26.10` tags have the same index digest, `sha256:739e099a…` ([hub latest][hub-latest], [hub 26.10][hub-2610]). The newest full tag in that series is `26.10.3.7` ([hub tags 26.1x][hub-261]). Master already mentions 26.11 in its setting histories ([Settings.cpp][src-settings]).
- **Driver:** `github.com/ClickHouse/clickhouse-go/v2` is at v2.50.0, published 2026-10-26 ([proxy][go-proxy]). Its go.mod declares `go 1.26.0` ([go.mod][go-mod]), which matches dbscope's go.mod.

**Findings that matter most for dbscope**
- **The server can enforce read-only per query.** When `readonly` is above 0, the server refuses INSERT, OPTIMIZE, DDL, SYSTEM, KILL QUERY and access management on persistent tables. It does this by checking privilege flags ([ContextAccess.cpp][src-ctxaccess], [permissions][d-perm]). This satisfies the ENGINES.md rule "the engine enforces safety".
- **`readonly = 1` blocks every other setting change.** At level 1, a setting can change only if it has a `CHANGEABLE_IN_READONLY` constraint or is on a short built-in list (format, database, limit, offset and a few others) ([SettingsConstraints.cpp][src-constraints]). So a level-1 user cannot raise or lower `max_execution_time`. That user also cannot set the two settings that EXPLAIN `indexes = 1` needs on 25.9 and later ([EXPLAIN][d-explain]).
- **`readonly = 2` is weaker than its name.** It also allows SET, CREATE TEMPORARY TABLE and RESTORE, and RESTORE can create and fill a table. BACKUP is not limited by `readonly` at any level ([permissions][d-perm]).
- **Most system tables need an explicit grant.** With the default `select_from_system_db_requires_grant = true`, every user can read only `tables`, `columns`, `databases` and constant tables ([access control settings][d-acs]).
- **`create_table_query` and `engine_full` can contain credentials.** Secret masking uses a hand-maintained list of engines, and open issues report keys that were left unmasked ([#114939][gh-114939], [#123315][gh-123315]).
- **`EXPLAIN ANALYZE` executes the SELECT.** ESTIMATE, PLAN, PIPELINE, SYNTAX, QUERY TREE and AST do not ([EXPLAIN][d-explain], [InterpreterExplainQuery.cpp][src-explain]).

---

## 1. Catalog

### 1.1 Tables and columns

| Table | Columns dbscope needs | Source |
|---|---|---|
| `system.databases` | `name`, `engine`, `engine_full`, `comment`, `is_external`, `uuid` | [databases][d-st-databases] |
| `system.tables` | `database`, `name`, `engine`, `engine_full`, `create_table_query`, `as_select`, `partition_key`, `sorting_key`, `primary_key`, `sampling_key`, `unique_key`, `skipping_indices_types`, `storage_policy`, `total_rows`, `total_bytes`, `total_bytes_uncompressed`, `parts`, `active_parts`, `total_marks`, `active_on_fly_data_mutations`, `dependencies_database/table`, `target_database/table`, `has_own_data`, `is_temporary`, `comment` | [tables][d-st-tables] |
| `system.columns` | `type`, `position`, `default_kind` (DEFAULT, MATERIALIZED, ALIAS or empty), `default_expression`, `compression_codec`, `subcolumn_codecs`, `data_compressed_bytes`, `data_uncompressed_bytes`, `marks_bytes`, `is_in_partition_key`, `is_in_sorting_key`, `is_in_primary_key`, `is_in_sampling_key`, `statistics`, `comment` | [columns][d-st-columns] |
| `system.parts` | `partition`, `partition_id`, `name`, `active`, `part_type`, `rows`, `marks`, `bytes_on_disk`, `data_compressed_bytes`, `data_uncompressed_bytes`, `level`, `modification_time`, `delete_ttl_info_min/max`, `projections`, `has_lightweight_delete`, `disk_name` | [parts][d-st-parts] |
| `system.data_skipping_indices` | `name`, `type`, `type_full`, `expr`, `creation` (Explicit or Implicit), `granularity`, compressed, uncompressed and marks bytes | [data_skipping_indices][d-st-dsi] |
| `system.projections` | `name`, `type` (Normal or Aggregate), `sorting_key`, `query`, `settings` | [projections][d-st-proj] |
| `system.projection_parts` | `parent_name`, `active`, `rows`, `bytes_on_disk`, `parent_rows`, `is_broken`, `exception` | [projection_parts][d-st-projparts] |
| `system.mutations` | `mutation_id`, `command`, `create_time`, `parts_to_do`, `parts_to_do_names`, `parts_postpone_reasons`, `is_done`, `latest_failed_part`, `latest_fail_time`, `latest_fail_reason`, `latest_fail_error_code_name` | [mutations][d-st-mutations] |
| `system.replication_queue` | `type`, `create_time`, `is_currently_executing`, `num_tries`, `last_exception`, `num_postponed`, `postpone_reason` | [replication_queue][d-st-rq] |
| `system.replicas` | `is_readonly`, `is_session_expired`, `queue_size`, `inserts_in_queue`, `merges_in_queue`, `part_mutations_in_queue`, `queue_oldest_time`, `absolute_delay`, `total_replicas`, `active_replicas`, `lost_part_count`, `zookeeper_exception` | [replicas][d-st-replicas] |
| `system.dictionaries` | `name`, `status`, `type`, `key.names/types`, `attribute.names/types`, `bytes_allocated`, `element_count`, `source`, `lifetime_min/max`, `last_exception` | [dictionaries][d-st-dict] |

- **Materialized views:** `target_database` and `target_table` name the destination table of a materialized view, whether that is the `TO` target or the implicit `.inner.*` table. `dependencies_table` lists the views that read from a table ([tables][d-st-tables]).
- **Size columns can be NULL.** `total_rows` and `total_bytes` are filled only when the server can compute them quickly. `total_bytes` is the compressed size on disk ([tables][d-st-tables]).
- **Inactive parts:** these are leftovers from merges and mutations. The server deletes them about 10 minutes after the merge ([partitioning][d-partitioning]). **Design:** filter on `active = 1` everywhere.
- **Part names:** a name encodes the partition, the block range, the merge level and the mutation version ([partitioning][d-partitioning]). `level = 0` means the part has not been merged yet ([parts][d-st-parts]).
- **Patch parts:** lightweight UPDATE writes patch parts into partitions named `patch-<hash>-<partition_id>` ([UPDATE][d-update]). **Design:** count them separately from normal partitions.
- **Hidden tables:** detached tables do not appear in `system.tables`. Temporary tables appear only in the session that created them ([tables][d-st-tables]).

### 1.2 Grants

- With `select_from_system_db_requires_grant = true`, which is the default, reading a system table needs `GRANT SELECT ON system.<table>`. The exceptions are `tables`, `columns`, `databases` and constant tables. A SHOW grant, such as SHOW USERS, also opens the matching system table ([access control settings][d-acs]).
- `system.databases` lists only the databases available to the current user ([databases][d-st-databases]). Whether `system.tables` and `system.columns` filter rows by grant is unverified.
- **Design:** grant SELECT on `system.parts`, `mutations`, `replicas`, `replication_queue`, `data_skipping_indices`, `projections`, `projection_parts`, `dictionaries` and `merge_tree_settings`. Probe each table and degrade the UI per table when access is denied.

### 1.3 Cost on big servers

- I found no primary source on what it costs to read `system.parts`, `system.columns` or `system.replicas` on a large server. Unverified.
- `system.parts` has one row per part, and inactive parts are included ([parts][d-st-parts]). A single table can have up to `max_parts_in_total`, which defaults to 100,000 active parts ([MergeTreeSettings.cpp][src-mts]).
- Some `system.replicas` columns are non-zero only when there is a Keeper session ([replicas][d-st-replicas]). Whether reading them adds Keeper round trips is unverified.
- **Design:** always filter by database and table, and name columns explicitly. Use `system.tables.active_parts`, `total_rows` and `total_bytes` for the overview. Read `system.parts` per table on demand. Apply the same execution and result limits as user queries.

### 1.4 Secrets in catalog strings

- SHOW CREATE hides secrets unless the server setting `display_secrets_in_show_and_select`, the format setting `format_display_secrets_in_show_and_select` and the `displaySecretsInShowAndSelect` grant are all in place ([SHOW][d-show]).
- Masking has had gaps. Open issues report engines whose credentials leaked through `engine_full` and SHOW CREATE ([#114939][gh-114939], [#123315][gh-123315]). Another open issue reports a view's encryption key appearing in `EXPLAIN AST optimize = 1` output ([#119383][gh-119383]).
- **Design:** treat `create_table_query`, `engine_full`, `as_select`, `dictionaries.source` and EXPLAIN text as secret-bearing. Redact them before they reach the NL model, a share link or a log.

---

## 2. Table-design risk thresholds

### 2.1 Part limits (MergeTree settings, master)

| Setting | Default | Effect | Source |
|---|---|---|---|
| `parts_to_delay_insert` | 1000 | When a partition has more active parts than this, INSERTs are slowed down. | [MergeTreeSettings.cpp][src-mts] |
| `parts_to_throw_insert` | 3000 (300 before 23.6) | When a partition has more active parts than this, INSERT fails with "Too many parts (N). Merges are processing significantly slower than inserts". | [MergeTreeSettings.cpp][src-mts] |
| `inactive_parts_to_delay_insert` | 0 (off) | The same slowdown, counted on inactive parts per partition. | [MergeTreeSettings.cpp][src-mts] |
| `max_parts_in_total` | 100000 | When the table has more active parts than this in total, INSERT fails with "Too many parts". | [MergeTreeSettings.cpp][src-mts] |
| `min_delay_to_insert_ms` | 10 | Minimum delay. Since 23.1 the delay grows linearly between the two thresholds. | [MergeTreeSettings.cpp][src-mts] |
| `max_partitions_per_insert_block` | 100 | An inserted block that touches more partitions than this fails, or only warns if `throw_on_max_partitions_per_insert_block = false`. | [Settings.cpp][src-settings] |
| `max_partitions_to_read` | -1 (unlimited) | Limits how many partitions one query may read. Can be set per query or per table. | [Settings.cpp][src-settings] |
| `allow_nullable_key` | false | Nullable columns are refused in keys unless this is enabled. | [MergeTreeSettings.cpp][src-mts] |

Global values are in `system.merge_tree_settings`. Per-table overrides live in the table's `SETTINGS` clause or are set with `MODIFY SETTING` ([MergeTree settings][d-mts]). **Design:** take the effective threshold from the table's `SETTINGS`, then `merge_tree_settings`, then the defaults above. Cloud defaults are unverified.

### 2.2 Partitions

- The docs describe partitioning mainly as a data-management tool. A query that spans many partitions can be slower than the same query on an unpartitioned table. A key with fewer than 100 to 1,000 distinct values is usually best, because parts never merge across partitions ([partitioning key][d-bp-partition]).
- The error text of `max_partitions_per_insert_block` recommends keeping a table under 1,000 to 10,000 partitions in total ([Settings.cpp][src-settings]).
- **Design flags:** warn above 1,000 active partitions, and raise an error above 10,000. Flag "very granular partitions" when the partition key is at day or finer resolution and the median rows per partition are far below `index_granularity`. The default granularity is 8,192 ([sparse index][d-sparse]). The ratio that counts as "far below" is a dbscope choice.

### 2.3 ORDER BY and primary key

- Prefer ORDER BY columns that WHERE clauses use to exclude many rows. Four or five key columns are usually enough ([primary key][d-bp-pk]).
- Ordering key columns by ascending cardinality helps both filtering and compression. A non-first key column is searched with "generic exclusion search", which works well only when the previous key column has lower cardinality ([sparse index][d-sparse]). A UUID-like column belongs last in the key ([sparse index][d-sparse]).
- `ORDER BY tuple()` means queries scan the whole table ([primary key][d-bp-pk]).
- **Design:** flag a key column whose sampled `uniq()` estimate is much higher than the next key column's. Run that check only on request, because it reads data. Also flag Nullable columns that have `is_in_sorting_key = 1`, and tables with `allow_nullable_key` enabled.

### 2.4 Types

- LowCardinality works for String, FixedString, Date, DateTime and numbers, but not Decimal. It is usually efficient below about 10,000 distinct values and can be slower than the plain type above about 100,000 ([LowCardinality][d-lowcard], [data types][d-bp-types]).
- A Nullable column adds a hidden UInt8 column that is read on every access. The docs advise a default value instead when possible ([data types][d-bp-types]).

### 2.5 TTL, mutations and replication

- `system.parts` exposes each part's TTL state ([parts][d-st-parts]). I found no official rule that a time-series table must have a TTL. **Design:** report an info-level heuristic when the partition key or first ORDER BY column is a date and the table has no TTL.
- A mutation with `is_done = 0` and a non-empty `latest_fail_reason` is failing. For replicated tables, `parts_to_do = 0` does not prove the mutation is done ([mutations][d-st-mutations]). Mutations on a table run in order. `KILL MUTATION` does not roll back parts that were already changed ([ALTER][d-alter], [KILL][d-kill]).
- In `system.replicas`, a large `inserts_in_queue` or a `log_pointer` far behind `log_max_index` signals a problem. `absolute_delay` is the replica lag in seconds ([replicas][d-st-replicas]).
- **Design thresholds:** these are dbscope's choices, not ClickHouse guidance. Flag a pending mutation older than 1 hour or one that has failed. Flag `absolute_delay` above 300 s, `queue_size` above 100, `is_readonly = 1`, and `num_tries` above 10.

---

## 3. Read-only mode and limits

### 3.1 What `readonly` allows

| Level | Allowed | Source |
|---|---|---|
| 0 | No restriction | [Settings.cpp][src-settings] |
| 1 | SELECT, SHOW, DESCRIBE, EXISTS and USE. Settings can change only if marked `CHANGEABLE_IN_READONLY` or on the built-in list. Most table functions are refused because they need CREATE TEMPORARY TABLE, but `numbers` is allowed. RESTORE is refused. | [permissions][d-perm], [SettingsConstraints.cpp][src-constraints] |
| 2 | Everything level 1 allows, plus SET (except of `readonly` itself), CREATE TEMPORARY TABLE, RESTORE and table functions | [permissions][d-perm], [SettingsConstraints.cpp][src-constraints] |

- **Blocked at any level above 0:** INSERT, OPTIMIZE, DELETE, UPDATE, ALTER (including ALTER DELETE and ALTER UPDATE), CREATE, DROP, RENAME, ATTACH, DETACH, TRUNCATE, privileged SYSTEM statements, user and role management, and GRANT ([permissions][d-perm]). In the source, the blocked flag set also includes KILL_QUERY ([ContextAccess.cpp][src-ctxaccess]).
- **Not blocked:**
  - BACKUP.
  - CREATE, ALTER and DROP NAMED COLLECTION.
  - A local REVOKE, and GRANT CURRENT GRANTS.
  - Writes to temporary tables ([permissions][d-perm]).
- **HTTP GET:** a GET request is forced to `readonly = 2` only when the effective value would otherwise be 0 ([constraints][d-constraints]).
- **Reverting readonly:** if `readonly` is itself marked changeable in read-only mode, a session can run `SET readonly = 0`. Also, any session can switch to any settings profile with `SET profile`, without an access check ([constraints][d-constraints]).
- **EXPLAIN under readonly:** the readonly check is applied to the privileges a statement needs ([ContextAccess.cpp][src-ctxaccess]). EXPLAIN of a SELECT therefore appears to need only SELECT. This is inferred from the source and has not been tested. EXPLAIN PIPELINE of an INSERT builds the INSERT interpreter ([InterpreterExplainQuery.cpp][src-explain]). Whether readonly refuses that is unverified.
- **KILL QUERY:** a read-only user can stop only their own queries, by `query_id`, without extra grants ([KILL][d-kill]).

### 3.2 Per user vs per query

- **Per user:** settings profiles accept the constraint keywords `READONLY`/`CONST`, `WRITABLE` and `CHANGEABLE_IN_READONLY`, plus `MIN`/`MAX` ([parser source][src-profile-parser], [constraints][d-constraints]). The safe pattern is a profile with `readonly = 1 CONST`, the limits marked `CHANGEABLE_IN_READONLY` with a `MAX`, and `GRANT SELECT` on the user's databases. The exact statement text should be checked in an integration test.
- **Per query from the client:** if the user's profile has `readonly = 0`, dbscope can send `readonly = 1` in each query's settings. It is unverified whether the server checks a batch of setting changes against the session's settings from before or after `readonly = 1` is applied within the same batch. That determines whether limits sent in the same batch are accepted. The Go driver keeps settings in a map, so the order in which they are sent is not fixed.
- **Detection:** `getSetting('readonly')` returns the current value ([functions][d-functions]).

### 3.3 Query limits

| Setting | Default | Notes | Source |
|---|---|---|---|
| `max_execution_time` | 0 | After `timeout_before_checking_execution_speed` (10 s by default), the server stops a query whose projected run time exceeds the limit. Some phases are not interruptible, so the real run time can be longer. | [Settings.cpp][src-settings] |
| `timeout_overflow_mode` | throw | With `break`, some operations stop without returning a partial result. | [query complexity][d-qc] |
| `max_result_rows`, `max_result_bytes` | 0 | Checked per block, so the result can exceed the limit | [Settings.cpp][src-settings] |
| `result_overflow_mode` | throw | `break` behaves like LIMIT but stops only at a block boundary. The row count can be a multiple of `max_block_size` and depends on `max_threads`. | [Settings.cpp][src-settings] |
| `max_rows_to_read`, `max_bytes_to_read` | 0 | Checked per chunk, on the deepest table expression only. Bytes are uncompressed. | [Settings.cpp][src-settings] |
| `read_overflow_mode` | throw | | [Settings.cpp][src-settings] |
| `max_rows_to_read_leaf`, `max_memory_usage`, `max_rows_in_join`, `max_partitions_to_read` | | Also available | [query complexity][d-qc] |

Limits are generally checked only after a block or part has been processed, so a query can briefly exceed them ([query complexity][d-qc]). **Design:** keep dbscope's own row cap on the client side as well.

---

## 4. EXPLAIN

- **Kinds:** AST, SYNTAX, QUERY TREE, PLAN, PIPELINE, ANALYZE (executes the query), ESTIMATE, WHATIF and TABLE OVERRIDE ([EXPLAIN][d-explain]).
- **`EXPLAIN PLAN indexes = 1, json = 1`:** every node has `Node Type`, `Node Id` and `Plans` ([EXPLAIN][d-explain]).
  - **Index entries:** `Indexes` is an array of objects. Each object's `Type` is one of `Partition Min-Max`, `Partition`, `Statistics`, `PrimaryKey` or `Skip`. Each object can also have `Name` (skip indexes only), `Keys`, `Condition`, `Description`, `Parts` and `Granules` (each given as after/before), and `Ranges` ([EXPLAIN][d-explain]).
  - **Projections:** `projections = 1` adds `Selected Parts/Marks/Ranges/Rows` and `Filtered Parts` ([EXPLAIN][d-explain]).
  - **Output format:** TSVRaw is recommended for JSON output ([EXPLAIN][d-explain]).
  - **Version caveat:** on ClickHouse 25.9 and later, `indexes` output is meaningful only with `use_query_condition_cache = 0` and `use_skip_indexes_on_data_read = 0` ([EXPLAIN][d-explain]). Under `readonly = 1` this needs `CHANGEABLE_IN_READONLY` constraints on those two settings.
- **`EXPLAIN ESTIMATE`:** works only for SELECT on MergeTree tables. It returns the columns `database`, `table`, `parts`, `rows` and `marks` ([InterpreterExplainQuery.cpp][src-explain], [EXPLAIN][d-explain]). It builds the pipeline but does not run it ([InterpreterExplainQuery.cpp][src-explain]).
- **`EXPLAIN ANALYZE`:** SELECT only. It runs the query and discards the rows. It is refused for plans that stay distributed ([InterpreterExplainQuery.cpp][src-explain]). **Design:** never run it automatically. Ask for confirmation and apply the same limits as a normal query.
- **FINAL:** merges data at query time for Replacing, Summing, Aggregating, Collapsing and VersionedCollapsing MergeTree tables. This uses extra CPU and memory and may read primary-key columns. The `final` setting can apply it to every table in a query ([FROM/FINAL][d-final]).
- **Design flags:**
  - `PrimaryKey` index with granules after/before above 0.9, or no PrimaryKey entry at all: likely full scan.
  - `Partition` index pruning nothing on a partitioned table.
  - ESTIMATE rows above the dbscope cap.
  - FINAL on a large table.
  - Joins whose right side has no filter. For join cost, the plan's join estimates appear in `pretty` output only when statistics exist ([EXPLAIN][d-explain]).
  - The `final` setting turned on through a profile.

---

## 5. ALTER semantics

The definitive list of operations that rewrite data comes from `allow_non_metadata_alters`. When that setting is off, these are refused on MergeTree tables ([Settings.cpp][src-settings]):

| Operation | Rewrites data? | Wait governed by | Source |
|---|---|---|---|
| ADD COLUMN | No. Old parts return the default at read time. | `alter_sync` | [ALTER][d-alter], [ALTER COLUMN][d-alter-col] |
| COMMENT COLUMN, MODIFY SETTING, Enum extension, DROP of an ALIAS column | No | `alter_sync` | [Settings.cpp][src-settings] |
| MODIFY COLUMN changing only the default, or a type change that keeps the same on-disk form (Date↔UInt16, DateTime↔UInt32, Enum extension) | No | `alter_sync` | [ALTER COLUMN][d-alter-col] |
| Any other MODIFY COLUMN type change | Yes. Creates a READ_COLUMN mutation. | `alter_sync` (default 1, Cloud default 0) | [ALTER COLUMN][d-alter-col] |
| DROP COLUMN, CLEAR COLUMN | Yes. Removes files; not purely metadata. | `alter_sync` | [ALTER][d-alter], [Settings.cpp][src-settings] |
| RENAME COLUMN | Listed as rewriting. Key columns cannot be renamed. | `alter_sync` | [Settings.cpp][src-settings], [ALTER COLUMN][d-alter-col] |
| DROP or CLEAR INDEX, DROP or CLEAR PROJECTION, DROP or CLEAR STATISTICS | Yes | | [Settings.cpp][src-settings] |
| ADD INDEX, ADD PROJECTION | Not in the rewriting list, so they appear to be metadata-only. Unverified. | | [Settings.cpp][src-settings] |
| MODIFY TTL | Yes when `materialize_ttl_after_modify = 1`, which is the default | | [Settings.cpp][src-settings] |
| MATERIALIZE INDEX, PROJECTION, COLUMN, STATISTICS or TTL; APPLY DELETED MASK; APPLY PATCHES; REWRITE PARTS | Yes (mutations) | `mutations_sync` | [Settings.cpp][src-settings], [ALTER][d-alter] |
| ALTER UPDATE (heavy), ALTER DELETE | Yes. Rewrites whole parts. | `mutations_sync` (default 0, async) | [ALTER][d-alter], [Settings.cpp][src-settings] |
| `DELETE FROM` (lightweight) | A mutation that writes the `_row_exists` mask. Rows are removed at a later merge. Compact parts are rewritten in full. | `lightweight_deletes_sync` (default 2) | [DELETE][d-delete], [Settings.cpp][src-settings] |
| `UPDATE ... SET` (lightweight) | Writes patch parts, about 40 bytes per updated row. Intended for up to about 10% of rows. Needs block-number and block-offset columns enabled on the table. | Returns when the patch is written | [UPDATE][d-update] |
| MODIFY ORDER BY | Metadata only. Can only append columns added in the same ALTER without a default. Cannot change sort direction. The primary key stays the same. | | [ORDER BY][d-orderby] |

- **mutations_sync values:** 0 means async. 1 waits on the current server. 2 waits on all replicas. 3 waits on active replicas only ([Settings.cpp][src-settings]).
- **Mutation behavior:** mutations are not atomic. A running SELECT can see a mix of mutated and unmutated parts. A mutation cannot be rolled back once submitted ([ALTER][d-alter]).
- **Concurrent ALTERs:** several ALTERs on a replicated table in quick succession can fail with CANNOT_ASSIGN_ALTER (code 517). The error is retryable ([ALTER][d-alter]).
- **ON CLUSTER:** the statement is queued in Keeper and runs on every host. A host that stays down long enough may miss it ([ON CLUSTER][d-ddl]). Each host chooses the lightweight-delete mode from its own settings unless the session sets it ([Settings.cpp][src-settings]).
- **Docs contradiction:** the ALTER COLUMN page says ALTER on MergeTree is lock-free, and also says ALTER blocks all reads and writes ([ALTER COLUMN][d-alter-col]). This should be checked against a server.
- **Cost estimate (design):** the docs' cost drivers are affected parts and bytes, conversion cost and background load ([ALTER][d-alter]). dbscope can compute `count()`, `sum(rows)` and `sum(bytes_on_disk)` from `system.parts WHERE active` for the affected partitions, and list pending mutations from `system.mutations`. A lightweight DELETE runs a `count()` per part to find affected parts ([DELETE][d-delete]), so a bounded `SELECT count() … WHERE cond` is a fair estimate.

---

## 6. Go driver and test servers

### 6.1 Driver
- **Version:** v2.50.0 ([proxy][go-proxy]). It supports Go 1.26 and 1.27 ([README][go-readme]).
- **APIs:** two APIs, native `clickhouse.Open` and `database/sql`. Each works over two transports: native TCP (9000, or 9440 with TLS) and HTTP (8123/8443). Both transports use native binary encoding ([Go docs][d-go], [README][go-readme]).
- **TLS:** set `Options.TLS`, or use the DSN parameters `secure`, `skip_verify` and `tls_server_name` ([README][go-readme]).
- **Settings:** connection-level settings go in `Options.Settings` or DSN query parameters. Per-query settings use `clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{...}))`. Server-side parameters use `WithParameters` ([context.go][go-context], [README][go-readme]). The README lists Query ID among the context options, but I did not capture the exact function name.
- **Cancel:** when the context is cancelled, the native connection sends a `ClientCancel` packet and closes the connection without reusing it ([conn_process.go][go-process]). Whether the server stops the query promptly, and how HTTP cancellation behaves, are unverified.
- **Dependencies:** direct requirements include `ClickHouse/ch-go`, `klauspost/compress`, `pierrec/lz4`, `andybalholm/brotli`, `shopspring/decimal`, `google/uuid`, `paulmach/orb` and OpenTelemetry trace. The go.mod also lists testcontainers and moby ([go.mod][go-mod]). Those appear to be used by the driver's own tests. Whether any of them end up in a dbscope binary, and the binary size, are unverified. I found no CGO requirement in the README or go.mod, but this needs a build check with `CGO_ENABLED=0`.
- **Type mapping** ([TYPES.md][go-types], [data types][d-go-types]):
  - UInt64 maps to `uint64`. Int128, Int256, UInt128 and UInt256 map to `big.Int`.
  - Decimal maps to `shopspring/decimal.Decimal`.
  - Date, Date32, DateTime and DateTime64 map to `time.Time`. Time and Time64 map to `time.Duration`.
  - Nullable needs a pointer that can hold nil, or an `sql.Null*` type. Scanning NULL into `*int64` fails.
  - Array maps to a slice, and Map to a Go map. An unnamed Tuple maps to a slice, and a named Tuple also accepts a map.
  - UUID maps to `google/uuid`. Geo types map to `orb`.
  - LowCardinality mapping: unverified.
  - **Design:** send UInt64 and big integers to the UI as strings.

### 6.2 Test servers
- **Image:** `clickhouse/clickhouse-server`. `latest` is the newest release of the newest stable branch, and `-alpine` and `-distroless` variants exist ([image README][docker-readme], [hub tags][hub-tags]). The Hub API reports 307,561,585 bytes for `latest` on amd64 ([hub latest][hub-latest]).
- **CPU requirements:** the amd64 image needs x86-64-v3 (AVX2 and related). The arm64 image needs ARMv8.2-A plus RCpc ([image README][docker-readme]).
- **Environment variables:** `CLICKHOUSE_DB`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` and `CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1`. The last one enables SQL user management. If none of these is set, the `default` user has no network access. `CLICKHOUSE_SKIP_USER_SETUP=1` opens it without a password. Init scripts go in `/docker-entrypoint-initdb.d` ([image README][docker-readme]).
- **Not found:** a `public.ecr.aws` mirror, startup time and memory needs. The ECR gallery page could not be read. All three are unverified.
- **Read-only test user (design):** create a settings profile with `readonly = 1 CONST` and `max_execution_time` marked `CHANGEABLE_IN_READONLY` with a MAX. Then run `CREATE USER ... IDENTIFIED WITH sha256_password BY ... SETTINGS PROFILE ...`, `GRANT SELECT ON db.*` and the system-table grants from 1.2. The keywords are verified in [CREATE USER][d-create-user] and [parser source][src-profile-parser]. The full statements should be tested.

---

## Facts dbscope must encode

- [ ] Detect the effective `readonly` with `getSetting('readonly')`. When it is 0, send `readonly = 1` per query. When it is 1, send only settings marked CHANGEABLE_IN_READONLY. Handle the "Cannot modify 'X' setting in readonly mode" error.
- [ ] Never treat `readonly = 2` as safe on its own, because RESTORE and BACKUP remain possible. Refuse RESTORE and BACKUP in the parser.
- [ ] Treat ANALYZE as execution. ESTIMATE, PLAN and PIPELINE do not run the query.
- [ ] Set `use_query_condition_cache = 0` and `use_skip_indexes_on_data_read = 0` for `EXPLAIN indexes = 1` on 25.9 and later.
- [ ] Parse `Indexes[].Type/Parts/Granules` from `json = 1` output. ESTIMATE returns the columns database, table, parts, rows and marks.
- [ ] `max_result_rows` with `break` can overshoot the limit by a block. Keep a client-side cap.
- [ ] The default part thresholds are 1000, 3000 and 100000. Read per-table overrides.
- [ ] Partition guidance is fewer than 100 to 1,000 distinct values, and under 1,000 to 10,000 partitions in total.
- [ ] Key columns should be in ascending cardinality order. Nullable columns in keys need `allow_nullable_key`. LowCardinality is good below about 10,000 distinct values and can hurt above about 100,000.
- [ ] Use the metadata vs rewrite table in section 5, with `allow_non_metadata_alters` as the authoritative rewrite list.
- [ ] Defaults: `mutations_sync` 0, `alter_sync` 1, `lightweight_deletes_sync` 2, `materialize_ttl_after_modify` 1.
- [ ] Grant SELECT on each needed system table. Only `tables`, `columns` and `databases` are open to all users.
- [ ] Redact `create_table_query`, `engine_full`, `as_select` and dictionary `source` before they reach the NL model, a share link or a log.
- [ ] On cancel, the native driver sends a cancel packet and drops the connection. Fall back to `KILL QUERY WHERE query_id = …`.
- [ ] For tests, the image needs one of the auth env vars before the `default` user can connect over the network. The amd64 image needs x86-64-v3.

## Sources

[hub-latest]: https://hub.docker.com/v2/repositories/clickhouse/clickhouse-server/tags/latest
[hub-2610]: https://hub.docker.com/v2/repositories/clickhouse/clickhouse-server/tags/26.10
[hub-261]: https://hub.docker.com/v2/repositories/clickhouse/clickhouse-server/tags?page_size=20&name=26.1
[hub-tags]: https://hub.docker.com/v2/repositories/clickhouse/clickhouse-server/tags?page_size=15&ordering=last_updated
[docker-readme]: https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/docker/server/README.md
[src-settings]: https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/src/Core/Settings.cpp
[src-mts]: https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/src/Storages/MergeTree/MergeTreeSettings.cpp
[src-ctxaccess]: https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/src/Access/ContextAccess.cpp
[src-constraints]: https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/src/Access/SettingsConstraints.cpp
[src-explain]: https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/src/Interpreters/InterpreterExplainQuery.cpp
[src-profile-parser]: https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/src/Parsers/Access/ParserSettingsProfileElement.cpp
[d-perm]: https://clickhouse.com/docs/operations/settings/permissions-for-queries
[d-constraints]: https://clickhouse.com/docs/operations/settings/constraints-on-settings
[d-acs]: https://clickhouse.com/docs/reference/settings/server-settings/settings/access-control
[d-qc]: https://clickhouse.com/docs/operations/settings/query-complexity
[d-explain]: https://clickhouse.com/docs/sql-reference/statements/explain
[d-final]: https://clickhouse.com/docs/sql-reference/statements/select/from
[d-kill]: https://clickhouse.com/docs/sql-reference/statements/kill
[d-show]: https://clickhouse.com/docs/sql-reference/statements/show
[d-functions]: https://clickhouse.com/docs/sql-reference/functions/other-functions
[d-create-user]: https://clickhouse.com/docs/sql-reference/statements/create/user
[d-alter]: https://clickhouse.com/docs/sql-reference/statements/alter
[d-alter-col]: https://clickhouse.com/docs/sql-reference/statements/alter/column
[d-orderby]: https://clickhouse.com/docs/sql-reference/statements/alter/order-by
[d-delete]: https://clickhouse.com/docs/sql-reference/statements/delete
[d-update]: https://clickhouse.com/docs/sql-reference/statements/update
[d-ddl]: https://clickhouse.com/docs/sql-reference/distributed-ddl
[d-mts]: https://clickhouse.com/docs/operations/settings/merge-tree-settings
[d-partitioning]: https://clickhouse.com/docs/engines/table-engines/mergetree-family/custom-partitioning-key
[d-bp-partition]: https://clickhouse.com/docs/best-practices/choosing-a-partitioning-key
[d-bp-pk]: https://clickhouse.com/docs/best-practices/choosing-a-primary-key
[d-bp-types]: https://clickhouse.com/docs/best-practices/select-data-types
[d-sparse]: https://clickhouse.com/docs/guides/best-practices/sparse-primary-indexes
[d-lowcard]: https://clickhouse.com/docs/sql-reference/data-types/lowcardinality
[d-st-index]: https://clickhouse.com/docs/operations/system-tables
[d-st-databases]: https://clickhouse.com/docs/operations/system-tables/databases
[d-st-tables]: https://clickhouse.com/docs/operations/system-tables/tables
[d-st-columns]: https://clickhouse.com/docs/operations/system-tables/columns
[d-st-parts]: https://clickhouse.com/docs/operations/system-tables/parts
[d-st-dsi]: https://clickhouse.com/docs/operations/system-tables/data_skipping_indices
[d-st-proj]: https://clickhouse.com/docs/operations/system-tables/projections
[d-st-projparts]: https://clickhouse.com/docs/operations/system-tables/projection_parts
[d-st-mutations]: https://clickhouse.com/docs/operations/system-tables/mutations
[d-st-rq]: https://clickhouse.com/docs/operations/system-tables/replication_queue
[d-st-replicas]: https://clickhouse.com/docs/operations/system-tables/replicas
[d-st-dict]: https://clickhouse.com/docs/operations/system-tables/dictionaries
[gh-114939]: https://github.com/ClickHouse/ClickHouse/issues/114939
[gh-123315]: https://github.com/ClickHouse/ClickHouse/issues/123315
[gh-119383]: https://github.com/ClickHouse/ClickHouse/issues/119383
[go-proxy]: https://proxy.golang.org/github.com/!click!house/clickhouse-go/v2/@latest
[go-mod]: https://raw.githubusercontent.com/ClickHouse/clickhouse-go/v2.50.0/go.mod
[go-readme]: https://raw.githubusercontent.com/ClickHouse/clickhouse-go/main/README.md
[go-context]: https://raw.githubusercontent.com/ClickHouse/clickhouse-go/main/context.go
[go-process]: https://raw.githubusercontent.com/ClickHouse/clickhouse-go/main/conn_process.go
[go-types]: https://raw.githubusercontent.com/ClickHouse/clickhouse-go/main/TYPES.md
[d-go]: https://clickhouse.com/docs/integrations/go
[d-go-types]: https://clickhouse.com/docs/integrations/language-clients/go/data-types


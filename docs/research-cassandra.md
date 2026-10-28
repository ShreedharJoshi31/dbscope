# Research: Apache Cassandra and ScyllaDB support

Collected 2026-10-28 by a research worker; the design dbscope adopted is in docs/ENGINES.md.

Collected 2026-10-28 from primary sources. Sources are cited inline as reference links, and the URL list is at the end. Nothing was run against a server and no Go build was run, because shell access was unavailable. Claims without a primary source are marked "unverified". Lines marked **design** are proposals for dbscope, not facts about Cassandra or ScyllaDB. Content from the cited pages was rephrased for compliance with licensing restrictions.

**Versions seen on the research date:**
- **Cassandra:** the download page names 5.0 as the latest release series, with 4.1 and older still listed ([download][c-download]). The newest Docker tags are 5.0.9 and 4.1.12 ([hub 5.0][hub-c50], [hub 4.1][hub-c41]). A `6.0` tag also exists on Docker Hub ([hub 6.x][hub-c6]). Whether 6.0 is GA is unverified. The 5.0 doc pages show a "prerelease version" banner even though 5.0 is the latest series ([SAI concepts][c-sai-concepts]).
- **ScyllaDB:** the stable docs are 2026.3 ([Docker best practices][s-docker]). The `latest` and `2026.3` Docker tags share one digest, and a `2026.4.0-rc0` tag exists ([hub scylla][hub-s]).
- **Go drivers:** `github.com/apache/cassandra-gocql-driver/v2` v2.1.2 ([proxy][g-proxy]). `github.com/gocql/gocql` v1.7.0, from 2024-09-25 ([proxy][g-old]). `github.com/scylladb/gocql` v1.20.0, from 2026-10-06 ([proxy][sg-proxy]).

**These findings matter most for dbscope's design:**
- **There is no read-only connection mode.** Read-only has to come from a role with only SELECT. Both engines ship with `AllowAllAuthorizer` by default, and then any GRANT is ignored ([cassandra.yaml 5.0][c-yaml50], [Scylla config][s-config]). dbscope cannot meet the ENGINES.md rule "the engine enforces safety" on a cluster without `CassandraAuthorizer`. It must detect that and say so.
- **The server can classify a SELECT without running it.** Cassandra checks whether ALLOW FILTERING is needed while it prepares the statement, not while it executes it ([SelectStatement.java][c-select]). A PREPARE therefore tells dbscope, against the real schema, whether a query is rejected.
- **The schema catalog is the same on both engines.** Both expose `system_schema.keyspaces/tables/columns/indexes/views/types`. ScyllaDB adds a hidden materialized view named `<index>_index` for every global secondary index ([Scylla CQL extensions][s-ext]).
- **Size and health signals are per node.** `system.size_estimates`, `system.table_estimates`, the `system_views.*` metric tables and Scylla's `system.large_*` tables describe only the node that answers ([Scylla large partitions][s-lp]). A cluster view needs one query per host.
- **Some "system" tables accept writes or hold secrets.** Scylla's `system.config` accepts UPDATE ([Scylla system keyspace][s-sysks]). Scylla's `system.roles.salted_hash` is the Alternator secret key ([Alternator][s-alt]). dbscope must never touch either.

---

## 1. Catalog

### 1.1 Cassandra `system_schema` tables (5.0 source)

All definitions come from [SchemaKeyspace.java (5.0)][c-schemaks].

| Table | Primary key | Columns dbscope needs |
|---|---|---|
| `keyspaces` | `(keyspace_name)` | `durable_writes boolean`, `replication frozen<map<text,text>>` |
| `tables` | `((keyspace_name), table_name)` | `id`, `comment`, `compaction` (map), `compression` (map), `caching` (map), `default_time_to_live int`, `gc_grace_seconds int`, `flags frozen<set<text>>`, `cdc`, `read_repair`, `speculative_retry`, `bloom_filter_fp_chance`, `memtable`, `extensions` |
| `columns` | `((keyspace_name), table_name, column_name)` | `kind text`, `position int`, `clustering_order text`, `type text`, `column_name_bytes blob` |
| `column_masks` | `((keyspace_name), table_name, column_name)` | masking function name, argument types and values (5.0) |
| `dropped_columns` | `((keyspace_name), table_name, column_name)` | `dropped_time`, `kind`, `type` |
| `indexes` | `((keyspace_name), table_name, index_name)` | `kind text`, `options frozen<map<text,text>>` |
| `views` | `((keyspace_name), view_name)` | `base_table_id`, `base_table_name`, `where_clause`, `include_all_columns`, plus the same options as `tables` |
| `types` | `((keyspace_name), type_name)` | `field_names frozen<list<text>>`, `field_types frozen<list<text>>` |
| `functions`, `aggregates`, `triggers` | per keyspace | UDF and UDA definitions, triggers |

- **`flags`** holds values from the set SUPER, COUNTER, DENSE, COMPOUND ([SchemaKeyspace.java][c-schemaks]).
- **`auto_repair`** appears in `tables` and `views` only when automated repair is enabled. It was added in 5.0.8 ([SchemaKeyspace.java][c-schemaks]). **Design:** use `SELECT *` and read columns by name.
- **`dclocal_read_repair_chance` and `read_repair_chance`** are kept only for driver compatibility and are not used ([SchemaKeyspace.java][c-schemaks]).

### 1.2 Column `kind`, `position`, `clustering_order`, `type`

- **`kind`** is the lowercase name of the enum PARTITION_KEY, CLUSTERING, REGULAR, STATIC. On disk it reads `partition_key`, `clustering`, `regular`, `static` ([ColumnMetadata.java][c-colmeta], [SchemaKeyspace.java][c-schemaks]).
- **`position`** numbers partition key columns and clustering columns separately. It is -1 for regular and static columns ([ColumnMetadata.java][c-colmeta]).
- **`clustering_order`** is `asc`, `desc` or `none`. Only clustering columns get `asc` or `desc` ([ColumnMetadata.java][c-colmeta]).
- **`type`** is the CQL type text with any reversal for DESC removed before it is stored ([SchemaKeyspace.java][c-schemaks]). **Design:** dbscope needs a parser for nested `frozen<...>`, `list`, `set`, `map`, `tuple`, `vector` and UDT names.
- **Static columns** need at least one clustering column. Only non-primary-key columns can be static ([DDL 5.0][c-ddl50]).
- **Partition key.** The first element of PRIMARY KEY is the partition key, and it may be composite. The rest are clustering columns, and their order defines the clustering order ([DDL 5.0][c-ddl50]).

### 1.3 Indexes, SAI and materialized views

- **Index `kind`** is KEYS, CUSTOM or COMPOSITES ([IndexMetadata.java][c-idxmeta]).
- **Index options.** Every index stores `options['target']`. CUSTOM indexes store `options['class_name']` ([IndexMetadata.java][c-idxmeta]).
- **Recognising SAI.** The aliases `sai` and `storageattachedindex` resolve to `org.apache.cassandra.index.sai.StorageAttachedIndex`. `SASIIndex` is the other built-in custom class ([IndexMetadata.java][c-idxmeta]).
- **CREATE INDEX syntax.** `USING 'sai' | 'legacy_local_table' | class`. Targets are a column, `KEYS`, `VALUES`, `ENTRIES` or `FULL` ([indexes 5.0][c-idx50]).
- **Default index type.** The default stays `legacy_local_table` unless `default_secondary_index: sai` is set ([cassandra.yaml 5.0][c-yaml50]).
- **SAI types.** SAI covers ascii, bigint, date, double, float, int, smallint, text, time, timestamp, timeuuid, tinyint, uuid, varchar, inet, varint, decimal and boolean. Frozen types and float vectors are also supported ([StorageAttachedIndex.java][c-saijava]).
- **SAI limits.** One SAI index covers one column, and there is at most one SAI index per column and target ([StorageAttachedIndex.java][c-saijava]).
- **SAI vector search.** ANN on vectors is experimental. It refuses consistency above ONE/LOCAL_ONE, paging, a missing LIMIT, GROUP BY and aggregates ([StorageAttachedIndex.java][c-saijava]).
- **SAI status.** `system_views.sai_column_indexes` lists each SAI index with `is_queryable` and `is_building` (5.0) ([ColumnIndexesSystemView.java][c-saivt]).
- **Materialized views are off by default.** They are experimental and disabled in 4.1 and 5.0 (`materialized_views_enabled: false`). So is SASI (`sasi_indexes_enabled: false`) ([cassandra.yaml 4.1][c-yaml41], [cassandra.yaml 5.0][c-yaml50]).
- **MV definition.** The view's primary key must contain every base primary key column plus at most one other column. Static columns cannot be included ([MVs 5.0][c-mv50]).

### 1.4 Replication

- **Where it lives.** `keyspaces.replication` is a frozen text map ([SchemaKeyspace.java][c-schemaks]). The example shows `class` and `replication_factor` keys ([DDL 5.0][c-ddl50]). The full key set is unverified.
- **Guardrails.** 4.1 added `minimum_replication_factor_warn/fail_threshold` ([cassandra.yaml 4.1][c-yaml41]). 5.0 added `simplestrategy_enabled` ([cassandra.yaml 5.0][c-yaml50]).
- **Schema version.** `system.local` and `system.peers_v2` carry `schema_version`, `data_center`, `rack` and `release_version` ([SystemKeyspace.java 5.0][c-syskeyspace]).

### 1.5 ScyllaDB differences

- **Compatibility baseline.** ScyllaDB calls itself a drop-in replacement for Cassandra 3.11 with some 4.0 features. It is fully compatible with CQL 3.3.1 and protocol v4, plus some v5 features ([Scylla compatibility][s-compat]). The per-feature tick marks did not survive text extraction, so row-level support is unverified.
- **Global secondary indexes are materialized views** ([Scylla GSI][s-si]). The index appears in `system_schema.indexes` as `kind = COMPOSITES`, `options = {'target': 'v'}`. A view named `<index_name>_index` appears in `system_schema.views` ([Scylla CQL extensions][s-ext]). **Design:** fold these views into their index.
- **SAI on ScyllaDB.** SAI DDL on vector columns is rewritten to `vector_index`. SAI on `ENTRIES(map)` becomes a regular secondary index when `enable_cassio_compatibility: true` ([Scylla compatibility][s-compat], [Scylla config][s-config]). Whether general SAI DDL is accepted is unverified.
- **Scylla-only schema tables.** `system_schema.scylla_keyspaces` holds keyspace storage options, and not every keyspace has a row there ([Scylla CQL extensions][s-ext]).
- **Tablets.** Tablets are the default for new keyspaces. A keyspace cannot be altered to switch tablets on or off. Tablet keyspaces with MVs or indexes must stay RF-rack-valid ([Scylla tablets][s-tablets]).
- **CDC.** `WITH cdc = {'enabled': true}` creates `<table>_scylla_cdc_log` in the same keyspace. Its key is `("cdc$stream_id"), "cdc$time", "cdc$batch_seq_no"` ([Scylla CDC log][s-cdc]).
- **Alternator.** A DynamoDB table `xyz` is visible to CQL as `alternator_xyz.xyz` ([Alternator][s-alt]).
- **Schema changes go through Raft.** They are applied in the same order on all nodes and need a quorum ([Scylla Raft][s-raft]).
- **Version detection.** `system.versions` is a virtual table with the real Scylla version ([Scylla system keyspace][s-sysks]). What Scylla reports in `system.local.release_version` is unverified.

---

## 2. Size and health signals over CQL

### 2.1 Partition counts and sizes

- **`system.size_estimates`** (`keyspace_name, table_name, range_start, range_end, mean_partition_size, partitions_count`) is deprecated since 4.0 in favour of `system.table_estimates` (CASSANDRA-15637). It still exists in 5.0 ([SystemKeyspace.java 5.0][c-syskeyspace]).
- **`system.table_estimates`** adds `range_type` to the key, with values `primary` and `local_primary` ([SystemKeyspace.java 5.0][c-syskeyspace]).
- **Default permissions.** Both tables, plus `local`, `peers`, `peers_v2` and all `system_schema` tables, are readable by any authenticated user without a GRANT ([ClientState.java][c-clientstate]).
- **Per-node data.** **Design:** sum the `primary` ranges across nodes for a cluster total. Refresh interval and accuracy are unverified.
- **ScyllaDB `system.size_estimates`** is a virtual table with the legacy Cassandra columns ([Scylla system keyspace][s-sysks]). Scylla does not appear to have `table_estimates` (unverified).
- **`system.top_partitions` (5.0)** stores `top frozen<list<tuple<text,bigint>>>` per `top_type` ([SystemKeyspace.java 5.0][c-syskeyspace]). What fills it is unverified.

### 2.2 `system_views` (Cassandra 4.1 and 5.0)

These tables are in the source for both versions ([SystemViewsKeyspace 4.1][c-svk41], [SystemViewsKeyspace 5.0][c-svk50]): caches, clients, settings, system_properties, sstable_tasks, thread_pools, internode_inbound/outbound, pending_hints, the table metric tables, the cache-key tables, cql_metrics, batch_metrics, streaming, gossip_info, queries and the local repair tables.

**5.0 adds** `system_logs`, `snapshots`, the CIDR metrics and the SAI tables ([SystemViewsKeyspace 5.0][c-svk50]).

**Table metric tables, each keyed by `(keyspace_name, table_name)`:**

| Table | Value | 4.1 | 5.0 |
|---|---|---|---|
| `local_read_latency`, `local_scan_latency`, `local_write_latency` | p50th, p99th, max, per_second | yes | yes |
| `coordinator_read_latency`, `coordinator_scan_latency`, `coordinator_write_latency` | same | yes | yes |
| `tombstones_per_read`, `rows_per_read` | histogram | yes | yes |
| `disk_usage`, `max_partition_size` | `mebibytes`, rounded up | yes | yes |
| `max_sstable_size`, `max_sstable_duration` | size, duration | no | yes |

Sources: [TableMetricTables 4.1][c-tmt41], [TableMetricTables 5.0][c-tmt50].

- **Names differ between source and docs.** The source uses `*_scan_latency`. The docs list `local_scan` and `coordinator_scan` ([virtual tables 5.0][c-vt50]). **Design:** discover names from `system_virtual_schema`.
- **Implicit ALLOW FILTERING.** From CASSANDRA-18238, every virtual table except `system_logs` adds ALLOW FILTERING when needed ([virtual tables 5.0][c-vt50]). The 4.1 docs do not say this ([virtual tables 4.1][c-vt41]).
- **`settings`** shows the running configuration with encryption secrets hidden, and DML cannot change it ([virtual tables 5.0][c-vt50]).
- **Grants are needed.** `system_views` is a system keyspace, so `ALL KEYSPACES` grants do not cover it ([ClientState.java][c-clientstate], [SchemaConstants.java][c-schemaconst]). That `system_views` is in the virtual set is inferred and unverified.
- **Node-local data.** **Design:** query each host with `Query.SetHostID()` ([gocql README][g-readme]).

### 2.3 Tombstone and large-partition thresholds

| Setting | 4.1 | 5.0 |
|---|---|---|
| `tombstone_warn_threshold` | 1000 | 1000 |
| `tombstone_failure_threshold` | 100000 | 100000 |
| `compaction_large_partition_warning_threshold` | 100MiB | replaced by guardrail |
| `compaction_tombstone_warning_threshold` | 100000 | replaced by guardrail |
| `partition_size_warn/fail_threshold` | n/a | null (off) |
| `partition_tombstones_warn/fail_threshold` | n/a | -1 (off) |

Sources: [cassandra.yaml 4.1][c-yaml41], [cassandra.yaml 5.0][c-yaml50].

- **Per-SSTable checks.** The 5.0 guardrails check each SSTable separately, so a partition spread over several SSTables can stay under the threshold ([cassandra.yaml 5.0][c-yaml50]).
- **Read thresholds.** `read_thresholds_enabled` is off by default ([cassandra.yaml 4.1][c-yaml41]).
- **Replica filtering protection** warns at 2000 cached rows and fails at 32000 ([cassandra.yaml 5.0][c-yaml50]).
- **Tombstone expiry.** Tombstones are dropped only by compaction after `gc_grace_seconds`, default 864000 (10 days). A node down for longer can bring deleted data back ([compaction overview 5.0][c-compov]).

### 2.4 ScyllaDB large data tables

- **`system.large_partitions`** has the key `((keyspace_name, table_name), sstable_name, partition_size DESC, partition_key)`, plus `rows`, `dead_rows` and `range_tombstones` ([Scylla large partitions][s-lp]).
- **Thresholds.** The defaults are 1000 MB and 100000 rows ([Scylla large partitions][s-lp]).
- **`system.large_rows` and `large_cells`.** The defaults are 10 MB per row, 1 MB per cell and 10000 collection elements ([Scylla large rows][s-lr]).
- **Detection limits.** Rows have a 30-day TTL, the table is per node, and a partition counts only when it is in one SSTable ([Scylla large partitions][s-lp]).
- **2026.2 and later.** These are virtual tables built from SSTable metadata (top 10 per SSTable). They can look empty after an upgrade until old SSTables are rewritten ([Scylla large partitions][s-lp]).
- **Tombstones in ScyllaDB.** `tombstone_warn_threshold` 1000 is logged only for single-partition queries. `query_tombstone_page_limit` 10000 cuts pages early ([Scylla config][s-config]). Empty pages are possible on range and filtering queries ([Scylla gocql README][sg-readme]).

---

## 3. Query rules

### 3.1 What the primary key allows

- **Partition key.** Only equality is allowed, and IN counts as equality. Non-equality needs `token()` ([DML 5.0][c-dml50]).
- **Order of restrictions.** The partition key must be restricted before clustering columns, and the clustering restrictions must select a contiguous row set ([DML 5.0][c-dml50]).
- **Ranges.** Only the last restricted clustering column may have a range. The tuple form `(c1, c2) > (x, y)` is allowed and means something different ([DML 5.0][c-dml50]).
- **IN.** The page says IN is allowed only on the last partition key column or the last primary key column ([DML 5.0][c-dml50]). That may be outdated (unverified).
- **ORDER BY** works only on clustering columns, in declared order or full reverse ([DML 5.0][c-dml50], [SelectStatement.java][c-select]).
- **Other operators.** CONTAINS and CONTAINS KEY apply to collections ([DML 5.0][c-dml50]). LIKE needs an index ([StatementRestrictions.java][c-restrictions]).

### 3.2 ALLOW FILTERING

- **Restrictions not covered by the key.** A restriction on a regular column without a supporting index needs ALLOW FILTERING, even with the partition key fully restricted ([StatementRestrictions.java][c-restrictions]).
- **Range and index reads** need ALLOW FILTERING unless one index group supports every expression ([SelectStatement.java][c-select]).
- **The error text** starts "Cannot execute this query as it might involve data filtering...". A different message appears when `allow_filtering_enabled: false` ([StatementRestrictions.java][c-restrictions]). The guardrail exists in 4.1 and 5.0 and defaults to true ([cassandra.yaml 4.1][c-yaml41]).
- **Cost.** It is a full scan whose latency grows with table size ([DML 5.0][c-dml50]).

### 3.3 Token range scans

- **Syntax.** `token(pk) > token(x)` selects by token, not by value. RandomPartitioner gives no meaningful order ([DML 5.0][c-dml50]).
- **Partitioners.** Murmur3 is the default in both engines, and the only option in ScyllaDB ([cassandra.yaml 5.0][c-yaml50], [Scylla config][s-config]).
- **Timeouts.** Range and index scans use `range_request_timeout` 10000 ms, and reads use `read_request_timeout` 5000 ms ([cassandra.yaml 5.0][c-yaml50]). ScyllaDB has the same defaults ([Scylla config][s-config]).

### 3.4 Secondary index and SAI behaviour

- **Legacy 2i is node-local** ([DML 5.0][c-dml50]). It is slow, and SAI is advised for new indexes ([2i concepts][c-2i]). A high-cardinality index may reduce performance ([glossary][c-glossary]).
- **One 2i per query.** A second non-indexed expression needs ALLOW FILTERING ([DML 5.0][c-dml50]).
- **SAI uses every index in the query.** It intersects AND-ed indexes and then post-filters ([SAI concepts][c-sai-concepts]). All-SAI expressions need no ALLOW FILTERING ([SelectStatement.java][c-select]).
- **OR support is unclear.** The concepts page lists OR, but the 5.0 grammar shows only AND ([DML 5.0][c-dml50]). Unverified.
- **Guardrails.** `non_partition_restricted_index_query_enabled` defaults to true. `sai_sstable_indexes_per_query_warn_threshold` defaults to 32 ([cassandra.yaml 5.0][c-yaml50]).
- **ScyllaDB global indexes** read the index view first, then the base table ([Scylla GSI][s-si]).

### 3.5 LIMIT, paging and consistency

- **LIMIT and PER PARTITION LIMIT** can be combined ([DML 5.0][c-dml50]).
- **Paging.** gocql's page size defaults to 5000, with prefetch at 0.25 ([gocql cluster.go][g-cluster]). 5.0 has page-size guardrails, off by default ([cassandra.yaml 5.0][c-yaml50]).
- **Read consistency levels** are ONE, TWO, THREE, QUORUM, ALL, LOCAL_QUORUM, EACH_QUORUM and LOCAL_ONE. ANY is write-only ([Dynamo 5.0][c-dynamo]). EACH_QUORUM is write-only in ScyllaDB ([Scylla compatibility][s-compat]).
- **gocql default.** The default consistency is QUORUM ([gocql cluster.go][g-cluster]).
- **Reads above ONE can write.** A digest mismatch triggers repair writes ([read repair 5.0][c-readrepair]), and `read_repair` defaults to BLOCKING ([DDL 5.0][c-ddl50]). **Design:** read at LOCAL_ONE. That ONE never repairs is inferred and unverified.
- **Guardrails can reject consistency levels** through `read_consistency_levels_disallowed` ([cassandra.yaml 4.1][c-yaml41]).

### 3.6 Design: a query-vs-model classifier

- **Design, step 1 (static rules).** Map each restriction to its column kind. Then classify the query:
  - **Single partition:** every partition key column is restricted with `=`.
  - **Multi-partition IN:** some partition key column uses IN.
  - **Clustering slice:** equalities on a clustering prefix, then at most one range.
  - **Index read:** a restriction on an indexed column, with the index kind (2i, SAI, Scylla GSI).
  - **Token range scan:** `token()` bounds.
  - **Full scan:** no partition key restriction and no index.
  - **Needs ALLOW FILTERING:** any remaining restriction.
- **Design, step 2 (server check).** PREPARE the statement without executing it. The filtering check runs in prepare ([SelectStatement.java][c-select]). gocql `Session.StatementMetadata` returns `PKBindColumnIndexes` ([gocql pkg.go.dev][g-pkg]). Whether PREPARE needs SELECT permission is unverified.

---

## 4. Read-only safety

### 4.1 Statements that write or change schema

- **Data writes:** INSERT, UPDATE, DELETE and BATCH ([DML 5.0][c-dml50]), plus TRUNCATE, which needs MODIFY ([security 5.0][c-sec50]).
- **Schema changes:** CREATE, ALTER and DROP for KEYSPACE, TABLE, INDEX, MATERIALIZED VIEW, TYPE, FUNCTION, AGGREGATE and TRIGGER ([DDL 5.0][c-ddl50], [indexes 5.0][c-idx50], [MVs 5.0][c-mv50], [security 5.0][c-sec50]).
- **Access control:** CREATE, ALTER and DROP ROLE or USER, GRANT and REVOKE ([security 5.0][c-sec50]).
- **ScyllaDB extras:** `PRUNE MATERIALIZED VIEW` deletes rows ([Scylla CQL extensions][s-ext]). `UPDATE system.config` changes configuration ([Scylla system keyspace][s-sysks]). `DESCRIBE SCHEMA WITH INTERNALS AND PASSWORDS` returns password hashes ([Scylla CQL extensions][s-ext]).
- **gocql refuses `USE`** ([gocql README][g-readme]).
- **Design:** allow only a single SELECT. Block `system.config`, `system_auth.*`, `system.roles*` and `AND PASSWORDS`.

### 4.2 Permissions and a read-only role

- **Permissions:** CREATE, ALTER, DROP, SELECT, MODIFY, AUTHORIZE, DESCRIBE, EXECUTE, UNMASK and SELECT_MASKED. SELECT on a keyspace covers its tables ([security 5.0][c-sec50]).
- **System keyspaces need explicit grants.** `ALL KEYSPACES` grants do not reach them ([ClientState.java][c-clientstate]).
- **Readable without a grant:** `system.local`, `peers`, `peers_v2`, `size_estimates`, `table_estimates`, `system_schema.*`, the `system_traces` tables and `system_virtual_schema` ([ClientState.java][c-clientstate]).
- **Grants are ignored by default.** The authenticator and authorizer default to AllowAll in Cassandra 5.0 and in ScyllaDB ([cassandra.yaml 5.0][c-yaml50], [Scylla config][s-config]).
- **Dynamic data masking.** If it is enabled, a role without UNMASK sees masked values ([cassandra.yaml 5.0][c-yaml50]).
- **Design, role DDL for the operator:**
  ```cql
  CREATE ROLE dbscope_ro WITH LOGIN = true AND PASSWORD = '<set by operator>';
  GRANT SELECT ON KEYSPACE app_ks TO dbscope_ro;
  GRANT SELECT ON KEYSPACE system_views TO dbscope_ro; -- syntax on a virtual keyspace unverified
  ```
- **Design, startup check.** Detect an AllowAll authorizer from `system_views.settings` or Scylla `system.config` (SELECT only) and warn.

### 4.3 Timeouts in gocql v2

- **`Timeout`** (default 11 s) bounds each query on the client and should exceed the server timeouts ([gocql cluster.go][g-cluster]).
- **`ConnectTimeout`** defaults to 11 s, and **`WriteTimeout`** defaults to Timeout ([gocql cluster.go][g-cluster]).
- **No retries by default** ([gocql cluster.go][g-cluster]).
- **Per-query context.** `Query.IterContext(ctx)` and `ExecContext(ctx)` cancel with the context. `WithContext` is deprecated ([gocql session.go][g-session]).
- **ScyllaDB `USING TIMEOUT`.** `SELECT ... USING TIMEOUT 200ms` sets a server-side timeout ([Scylla CQL extensions][s-ext]). Cassandra 5.0's grammar has no such clause ([DML 5.0][c-dml50]).

---

## 5. Schema-change risks

- **ADD column** is constant-time. Primary key columns cannot be added or changed ([DDL 5.0][c-ddl50]).
- **DROP column:**
  - The column disappears at once, but its data is removed by compaction. Correct behaviour relies on real microsecond timestamps ([DDL 5.0][c-ddl50]).
  - A drop is refused for PK columns, non-frozen UDT columns, indexed columns, and columns of tables with MVs ([AlterTableStatement.java][c-alter]).
  - The drop is recorded in `dropped_columns` ([SchemaKeyspace.java][c-schemaks]).
- **Re-add after drop:**
  - Re-adding fails if the new type is not serialization-compatible with the dropped one, if the static or regular kind changes, or if the column was a counter ([AlterTableStatement.java][c-alter]).
  - A non-frozen collection cannot be re-added under the same name ([DDL 5.0][c-ddl50]).
- **ALTER TYPE** is unsupported: "Altering column types is no longer supported" ([AlterTableStatement.java][c-alter]).
- **RENAME** works on PK columns only ([DDL 5.0][c-ddl50]).
- **The `alter_table_enabled` guardrail** can block column changes ([cassandra.yaml 5.0][c-yaml50]).
- **`WITH compaction` replaces all sub-options**, and compression behaves the same way ([DDL 5.0][c-ddl50]).
- **Cost of a strategy change:** unverified (no primary source, and secondary sources disagree). LCS needs about 10% free disk and more IO and CPU. UCS is recommended from 5.0 ([LCS 5.0][c-lcs]).
- **New index on existing data** builds asynchronously ([indexes 5.0][c-idx50]). For SAI, watch `is_building` and `is_queryable` ([ColumnIndexesSystemView.java][c-saivt]).
- **MV build** is single-threaded by default: `concurrent_materialized_view_builders` is 1 ([MVs 5.0][c-mv50], [cassandra.yaml 5.0][c-yaml50]). On ScyllaDB, every global index adds a view and slows writes ([Scylla GSI][s-si]).
- **Schema agreement in Cassandra:**
  - Each node reports `schema_version` in `system.local` and `peers_v2` ([SystemKeyspace.java 5.0][c-syskeyspace]).
  - gocql waits up to `MaxWaitSchemaAgreement` (60 s) ([gocql cluster.go][g-cluster]).
  - That gossip-based schema conflicts under concurrent DDL is described by ScyllaDB ([Scylla Raft][s-raft]). There is no Cassandra-side primary source, so this is unverified for Cassandra.
- **Schema agreement in ScyllaDB.** Raft makes concurrent DDL safe, given a quorum ([Scylla Raft][s-raft]).

---

## 6. Go driver and test servers

### 6.1 Driver

| | Upstream | Old path | Scylla fork |
|---|---|---|---|
| Module | `github.com/apache/cassandra-gocql-driver/v2` | `github.com/gocql/gocql` | `github.com/scylladb/gocql` via `replace github.com/gocql/gocql =>` |
| Latest | v2.1.2 | v1.7.0 (2024-09-25) | v1.20.0 (2026-10-06) |
| Go directive | 1.19 | n/a | 1.25.0 |
| Sources | [proxy][g-proxy], [go.mod][g-mod] | [proxy][g-old] | [proxy][sg-proxy], [go.mod][sg-mod], [README][sg-readme] |

- **Testing scope.** Upstream CI tests Go 1.25 and 1.26 against Cassandra 4.1.x and 5.0.x, with protocols v3 to v5 and vectors ([gocql README][g-readme]).
- **The fork.** It is shard-aware, ignores `NumConns`, and is `v1` only, so `v2.x` tags are not valid replacements ([Scylla gocql README][sg-readme]).
- **Upstream driver on ScyllaDB.** Any protocol v4 driver works, and ScyllaDB recommends its shard-aware drivers ([Scylla third-party drivers][s-3p]). Upstream v2 against ScyllaDB 2026.x was not tested (unverified).
- **CGO.** The v2 dependencies are snappy, go-hostpool, lz4/v4, zerolog, zap and inf.v0 ([go.mod][g-mod]). No cgo is expected. Unverified, because no `CGO_ENABLED=0` build was run.
- **Binary size impact:** not documented. Unverified.
- **TLS.** `SslOpts` embeds `*tls.Config`. It is insecure by default when Config is nil and `EnableHostVerification` is false ([gocql pkg.go.dev][g-pkg]).
- **Authentication.** `PasswordAuthenticator{Username, Password, AllowedAuthenticators}` ([gocql pkg.go.dev][g-pkg]).
- **Token awareness.** The default policy is round-robin ([gocql cluster.go][g-cluster]). Use `TokenAwareHostPolicy(DCAwareRoundRobinPolicy(dc))`. Routing needs every partition key column bound ([gocql pkg.go.dev][g-pkg]).
- **Metadata cache.** `Metadata.CacheMode` is Full (the default), KeyspaceOnly or Disabled ([gocql cluster.go][g-cluster]).
- **Design:** start with upstream v2 for both engines. The fork changes the import path for the whole binary.

### 6.2 Test servers

| Image | Tag | amd64 compressed size | Memory guidance |
|---|---|---|---|
| `cassandra` | 5.0.9 | 168,277,802 B | none documented by the image |
| `cassandra` | 4.1.12 | 149,019,639 B | none documented by the image |
| `scylladb/scylla` | 2026.3 / latest | 275,015,429 B | at least 1.5 GB RAM per container |

Sources: [hub 5.0][hub-c50], [hub 4.1][hub-c41], [hub scylla][hub-s], [Scylla Docker][s-docker].

- **Cassandra heap sizing.** The heap is half of what `free -m` reports, capped at 15872M with CMS or 31744M otherwise. `MAX_HEAP_SIZE` overrides it ([cassandra-env.sh 5.0][c-env50]). Whether a container's cgroup limit is seen is unverified.
- **Cassandra startup.** No connections are accepted until first initialisation finishes ([image docs][c-docker]). Startup time is unverified.
- **ScyllaDB startup flags.** The image runs in developer mode by default. Use `--smp 1 --overprovisioned 1`, and `--reactor-backend=epoll` on macOS with three or more nodes ([Scylla Docker][s-docker], [Scylla gocql README][sg-readme]). Startup time is unverified.
- **MV and SASI tests need a custom `cassandra.yaml`**, because both are off by default ([cassandra.yaml 5.0][c-yaml50], [image docs][c-docker]).

---

## Facts dbscope must encode

- [ ] `columns.kind` is `partition_key`, `clustering`, `regular` or `static`. `position` counts per kind. `clustering_order` is `asc`, `desc` or `none`.
- [ ] Read `tables` and `views` with `SELECT *`. Columns vary by version (for example `auto_repair` from 5.0.8).
- [ ] Parse nested CQL types: frozen, list, set, map, tuple, vector and UDT names.
- [ ] Index classification: not CUSTOM means legacy 2i. CUSTOM with class `sai` or `StorageAttachedIndex` means SAI. SASIIndex means SASI. On Scylla, COMPOSITES plus a `<index>_index` view means a global index.
- [ ] Attach Scylla index views and `_scylla_cdc_log` tables to their base. Label `alternator_*` keyspaces.
- [ ] Detect ScyllaDB by the presence of `system.versions`.
- [ ] Size and health tables are per node. Query each host.
- [ ] Use `table_estimates` (`primary`) on Cassandra 4.0+ and `size_estimates` on ScyllaDB.
- [ ] Discover `system_views` names. The scan-latency names differ between docs and code. MiB values are rounded up.
- [ ] Defaults: tombstone warn 1000, fail 100000. 4.1 large partition 100MiB. 5.0 partition guardrails off. Scylla large partition 1000 MB or 100000 rows, row 10 MB, cell 1 MB. `gc_grace_seconds` 864000.
- [ ] Query rules: partition key `=` or IN, otherwise `token()`. Clustering equality prefix with at most one final range. ORDER BY on clustering order or its reverse. A non-indexed regular column needs ALLOW FILTERING. One legacy 2i per query. All-SAI expressions need no ALLOW FILTERING.
- [ ] Recognise both ALLOW FILTERING error texts.
- [ ] Optional PREPARE check to get the server's ALLOW FILTERING verdict.
- [ ] Read at LOCAL_ONE. The gocql default is QUORUM.
- [ ] Single SELECT only. Block `system.config`, `PRUNE MATERIALIZED VIEW`, `AND PASSWORDS`, `system_auth`, `system.roles` and `salted_hash`.
- [ ] Warn when the authorizer is AllowAll.
- [ ] `system_views` needs an explicit GRANT. `system_schema` and the estimates tables need none.
- [ ] Give every query an `IterContext` deadline. Keep `Timeout` above 5 s for reads and 10 s for range scans. Scylla has `USING TIMEOUT`.
- [ ] TLS with verification on. Set `AllowedAuthenticators`.
- [ ] Schema-change rules from section 5: no ALTER TYPE, the re-add rules, the drop refusals, `WITH compaction` replacing all sub-options, asynchronous index and MV builds.
- [ ] Check `schema_version` agreement across nodes on Cassandra. ScyllaDB orders schema through Raft.
- [ ] Flag MVs and SASI as experimental on Cassandra.
- [ ] Tests: Cassandra 4.1.12 and 5.0.9 with `MAX_HEAP_SIZE` set. ScyllaDB 2026.3 with `--smp 1 --overprovisioned 1` and at least 1.5 GB RAM. Wait for a CQL query to succeed, not for the open port.

[c-download]: https://cassandra.apache.org/_/download.html
[c-ddl50]: https://cassandra.apache.org/doc/5.0/cassandra/developing/cql/ddl.html
[c-dml50]: https://cassandra.apache.org/doc/5.0/cassandra/developing/cql/dml.html
[c-idx50]: https://cassandra.apache.org/doc/5.0/cassandra/developing/cql/indexes.html
[c-mv50]: https://cassandra.apache.org/doc/5.0/cassandra/developing/cql/mvs.html
[c-sec50]: https://cassandra.apache.org/doc/5.0/cassandra/developing/cql/security.html
[c-sai-concepts]: https://cassandra.apache.org/doc/5.0/cassandra/developing/cql/indexing/sai/sai-concepts.html
[c-2i]: https://cassandra.apache.org/doc/5.0/cassandra/developing/cql/indexing/2i/2i-concepts.html
[c-glossary]: https://cassandra.apache.org/_/glossary.html
[c-vt50]: https://cassandra.apache.org/doc/5.0/cassandra/managing/operating/virtualtables.html
[c-vt41]: https://cassandra.apache.org/doc/4.1/cassandra/new/virtualtables.html
[c-dynamo]: https://cassandra.apache.org/doc/5.0/cassandra/architecture/dynamo.html
[c-readrepair]: https://cassandra.apache.org/doc/5.0/cassandra/managing/operating/read_repair.html
[c-compov]: https://cassandra.apache.org/doc/5.0/cassandra/managing/operating/compaction/overview.html
[c-lcs]: https://cassandra.apache.org/doc/5.0/cassandra/managing/operating/compaction/lcs.html
[c-schemaks]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/schema/SchemaKeyspace.java
[c-colmeta]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/schema/ColumnMetadata.java
[c-idxmeta]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/schema/IndexMetadata.java
[c-schemaconst]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/schema/SchemaConstants.java
[c-syskeyspace]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/db/SystemKeyspace.java
[c-svk50]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/db/virtual/SystemViewsKeyspace.java
[c-svk41]: https://raw.githubusercontent.com/apache/cassandra/cassandra-4.1/src/java/org/apache/cassandra/db/virtual/SystemViewsKeyspace.java
[c-tmt50]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/db/virtual/TableMetricTables.java
[c-tmt41]: https://raw.githubusercontent.com/apache/cassandra/cassandra-4.1/src/java/org/apache/cassandra/db/virtual/TableMetricTables.java
[c-saijava]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/index/sai/StorageAttachedIndex.java
[c-saivt]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/index/sai/virtual/ColumnIndexesSystemView.java
[c-alter]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/cql3/statements/schema/AlterTableStatement.java
[c-select]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/cql3/statements/SelectStatement.java
[c-restrictions]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/cql3/restrictions/StatementRestrictions.java
[c-clientstate]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/src/java/org/apache/cassandra/service/ClientState.java
[c-yaml50]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/conf/cassandra.yaml
[c-yaml41]: https://raw.githubusercontent.com/apache/cassandra/cassandra-4.1/conf/cassandra.yaml
[c-env50]: https://raw.githubusercontent.com/apache/cassandra/cassandra-5.0/conf/cassandra-env.sh
[c-docker]: https://raw.githubusercontent.com/docker-library/docs/master/cassandra/content.md
[hub-c50]: https://hub.docker.com/v2/repositories/library/cassandra/tags?page_size=8&name=5.0.
[hub-c41]: https://hub.docker.com/v2/repositories/library/cassandra/tags?page_size=1&name=4.1.
[hub-c6]: https://hub.docker.com/v2/repositories/library/cassandra/tags?page_size=1&name=6.
[hub-s]: https://hub.docker.com/v2/repositories/scylladb/scylla/tags?page_size=25&ordering=last_updated
[g-readme]: https://raw.githubusercontent.com/apache/cassandra-gocql-driver/trunk/README.md
[g-proxy]: https://proxy.golang.org/github.com/apache/cassandra-gocql-driver/v2/@latest
[g-mod]: https://proxy.golang.org/github.com/apache/cassandra-gocql-driver/v2/@v/v2.1.2.mod
[g-cluster]: https://raw.githubusercontent.com/apache/cassandra-gocql-driver/v2.1.2/cluster.go
[g-session]: https://raw.githubusercontent.com/apache/cassandra-gocql-driver/v2.1.2/session.go
[g-pkg]: https://pkg.go.dev/github.com/apache/cassandra-gocql-driver/v2
[g-old]: https://proxy.golang.org/github.com/gocql/gocql/@latest
[sg-proxy]: https://proxy.golang.org/github.com/scylladb/gocql/@latest
[sg-mod]: https://proxy.golang.org/github.com/scylladb/gocql/@v/v1.20.0.mod
[sg-readme]: https://raw.githubusercontent.com/scylladb/gocql/master/README.md
[s-3p]: https://docs.scylladb.com/stable/drivers/third-party-drivers.html
[s-docker]: https://docs.scylladb.com/manual/stable/operating-scylla/procedures/tips/best-practices-scylla-on-docker.html
[s-compat]: https://docs.scylladb.com/manual/stable/using-scylla/cassandra-compatibility.html
[s-si]: https://docs.scylladb.com/manual/stable/features/secondary-indexes.html
[s-ext]: https://docs.scylladb.com/manual/stable/cql/cql-extensions.html
[s-lp]: https://docs.scylladb.com/manual/stable/troubleshooting/large-partition-table.html
[s-lr]: https://docs.scylladb.com/manual/stable/troubleshooting/large-rows-large-cells-tables.html
[s-sysks]: https://raw.githubusercontent.com/scylladb/scylladb/master/docs/dev/system_keyspace.md
[s-cdc]: https://docs.scylladb.com/manual/stable/features/cdc/cdc-log-table.html
[s-alt]: https://docs.scylladb.com/manual/stable/alternator/compatibility.html
[s-raft]: https://docs.scylladb.com/manual/stable/architecture/raft.html
[s-tablets]: https://docs.scylladb.com/manual/stable/architecture/tablets.html
[s-config]: https://docs.scylladb.com/manual/stable/reference/configuration-parameters.html

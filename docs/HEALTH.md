# Server health

The dock's **Health** tab lists a connection's server health signals as
checks, worst first: each says what it measured, whether that is fine
(`ok`), worth watching (`warn`) or urgent (`danger`), and what to do. A
check the role may not read comes back `unknown` and names the grant it
needs, instead of reporting a healthy value it could not see.

The agent serves them at `GET /api/profiles/{id}/health` for engines whose
profile has the `health` capability (`internal/health`). Every check is a
read of the server's statistics or catalog; nothing is changed.

## PostgreSQL

Checked on PostgreSQL 14, 17 and 18 by building each condition
(`internal/health/postgres_test.go`): an open transaction, a session waiting
on a `LOCK TABLE`, the invalid index a failed `CREATE UNIQUE INDEX
CONCURRENTLY` leaves, an inactive physical slot, a table with most of its
rows deleted, and a role without `pg_read_all_stats`.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Connections | 80% of `max_connections` less the superuser reserve | 95% | |
| Oldest open transaction | 5 min | 1 h | it holds back vacuum on every table |
| Sessions idle in a transaction | 1 for over a minute | 5 | |
| Transaction ID age | 50% of 2^31 | 75% | at the limit the server refuses writes |
| Replication lag | 30 s | 5 min | on a replica, time since the last replayed transaction; on a primary, each standby's replay lag |
| Inactive replication slots | any | 10 GiB of retained WAL | an inactive slot keeps WAL until the disk fills |
| Sessions waiting on locks | 1 | 10 | |
| Invalid indexes | any | | maintained on every write, never used |
| Tables with many dead rows | over 20% dead and over 10,000 | | autovacuum is behind or blocked |
| Cache hit ratio | under 95% | under 80% | only after 100,000 block reads |

Without `pg_read_all_stats` (or `pg_monitor`) a role sees only its own
sessions: the session checks say so, and standby replication is `unknown`.

## MySQL and MariaDB

Checked on MySQL 8.4 and MariaDB 11.8 (`internal/mysql/health_test.go`) with
a transaction holding a row lock, a second one waiting on it, and a user
with only SELECT.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Connections | 80% of `max_connections` | 95% | also the most at once since start |
| Oldest open transaction | 5 min | 1 h | it holds back purge |
| Transactions waiting on locks | 1 | 10 | `LOCK WAIT` in `INNODB_TRX` |
| Undo history not yet purged | 100,000 | 1,000,000 | `trx_rseg_history_len` |
| Replication | 30 s behind | 5 min, or a stopped thread | `SHOW REPLICA STATUS`; "not a replica" otherwise |
| Buffer pool hit ratio | under 99% | under 95% | only after 100,000 page reads |

Both servers refuse `INNODB_TRX` and `INNODB_METRICS` without `PROCESS`, and
replica status without `REPLICATION CLIENT` (MySQL) or `SLAVE MONITOR`
(MariaDB); those checks then say which grant is missing.

## MongoDB

Checked on MongoDB 8.0 standalone and as a one-member replica set
(`internal/mongo/health_test.go`), with a slow `$where` query running and a
user with only the `read` role.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Connections | 80% of current plus available | 95% | `serverStatus.connections` |
| WiredTiger cache | 5% dirty | 20% dirty | above 5% eviction slows writes; above 20% application threads evict |
| Replication | a secondary 30 s behind | 5 min, or a member not PRIMARY, SECONDARY or ARBITER | a standalone is reported as such |
| Oplog window | under 24 h | under 6 h | rated only once the oplog is 90% full; a young set's window is just its age |
| Long-running operations | 1 over a minute | 5 | from `currentOp`, without internal ones |

`serverStatus`, `replSetGetStatus` and `currentOp` need the `clusterMonitor`
role; without it those checks name it.

## Elasticsearch and OpenSearch

Checked on Elasticsearch 9.5 and OpenSearch 3.9 (`internal/elastic/health_test.go`)
with an index whose replica a single node cannot assign and an index with a
write block.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Cluster status | yellow | red | with unassigned and relocating shards |
| Pending cluster tasks | 10 | 100 | |
| Disk use | 85% (low watermark) | 90% (high watermark) | the busiest node |
| Shards per node | 80% of `cluster.max_shards_per_node` | 95% | at the limit, creating an index fails |
| JVM heap | 75% | 90% | the busiest node |
| Indices blocked for writes | | any | `read_only_allow_delete` (flood stage), `read_only` or `write` blocks |

Every check needs the `monitor` cluster privilege (`cluster_monitor` on
OpenSearch). Without it the clusters refuse even `GET /`, so dbscope cannot
connect as that user at all.

## SQL Server

Checked on SQL Server 2022 (`internal/mssql/health_test.go`) with a session
blocked behind another's update and a `db_datareader` login.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Sessions waiting on locks | 1 | 10 | |
| Oldest open transaction | 5 min | 1 h | it keeps the log from being truncated |
| Transaction log | 70% used | 90% | with `log_reuse_wait_desc` and what to do about it |
| Last full backup | 7 days, or never | 30 days | from `msdb.dbo.backupset` |
| Availability group | a partially healthy replica | a replica not healthy | "not in an availability group" otherwise |
| Page life expectancy | under 300 s | under 60 s | not rated in the first hour after start |

The DMV checks need `VIEW SERVER PERFORMANCE STATE` (`VIEW SERVER STATE`
before 2022). Without it, `sys.dm_exec_requests` shows only the login's own
requests rather than failing, so dbscope checks the permission and reports
the check as not readable instead of a reassuring zero.

## Oracle

Checked on Oracle AI Database 26ai Free (`internal/oracle/health_test.go`) with
a session waiting on another's row lock, a view left invalid by a dropped
column, and a user with only `CREATE SESSION` and `READ`.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Processes and sessions | 80% of the limit | 95% | `v$resource_limit` |
| Sessions waiting on locks | 1 | 10 | |
| Oldest open transaction | 5 min | 1 h | its undo is kept; long queries risk ORA-01555 |
| Tablespace use | 85% | 95% | counting room to autoextend |
| Fast Recovery Area | 80% | 90% | when full, archiving and so every write stops |
| Invalid objects | any | | in user schemas the user can see |
| Data Guard | 30 s apply lag | 5 min | on a standby; the role otherwise |

The V$ and DBA_ views need `SELECT_CATALOG_ROLE`; invalid objects are read
from `ALL_OBJECTS`, which every user can query.

## ClickHouse

Checked on ClickHouse 26.8 (`internal/clickhouse/health_test.go`) with 350
single-row inserts into a table whose merges were stopped, a mutation that
fails on purpose, and a user without grants on system tables.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Parts per partition | 300 | 1,000 | inserts are delayed at 1,000 and fail at 3,000 |
| Unfinished mutations | oldest 30 min | 4 h, or any failing | a failing mutation blocks the ones after it |
| Replication | 30 s behind | 5 min, or a read-only replica | from `system.replicas` |
| Disk use | 80% | 90% | merges need room for their output |
| Memory | 80% of the machine | 90% | resident memory |
| Detached parts | 1 | 100 | broken or unexpected parts only, not ones detached on purpose |

Each check reads one system table; without `GRANT SELECT ON system.<table>`
it says which grant to add.

## Neo4j

Checked on Neo4j 2026.10 Community and Enterprise (`internal/neo4j/health_test.go`)
with a write transaction left open past the threshold.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Databases | | any not in its requested status | `SHOW DATABASES` |
| Servers | | any not Available and Enabled | Enterprise; Community is one server and has no `SHOW SERVERS` |
| Long-running transactions | 1 over a minute | 5 | `SHOW TRANSACTIONS` |
| Indexes not online | populating | failed | `SHOW INDEXES` |

## Cassandra and ScyllaDB

Checked on Cassandra 5.0 and ScyllaDB 2025.1 (`internal/cassandra/health_test.go`);
on Cassandra the test reads a partition through 1,500 deleted rows.
Cassandra 5 exposes these as `system_views` virtual tables; ScyllaDB has none
of them, so it gets the checks its own system tables allow.

| Check | Warn | Danger | Notes |
|---|---|---|---|
| Nodes | Cassandra: a node not NORMAL in gossip | ScyllaDB: a node down or not NORMAL | gossip does not say whether a Cassandra node is up |
| Thread pools | 100 pending, or any blocked | 1,000 pending | Cassandra only |
| Pending hints | any | | Cassandra only: writes held for replicas that were down |
| Largest partitions | 100 MiB | 1 GiB | `max_partition_size` on Cassandra, `large_partitions` on ScyllaDB (which records only partitions above its warning threshold) |
| Tombstones per read | 1,000 | 100,000 | Cassandra only; at 100,000 reads fail |

Redis and Valkey have no Health tab: their server risks (memory policy,
persistence, eviction) are findings in the keyspace report.

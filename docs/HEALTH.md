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

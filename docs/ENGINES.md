# Database engines

dbscope started as a PostgreSQL tool and kept "Postgres only, no driver
abstraction" as a fixed decision (D5) through Phase 5. In October 2026 the user
chose to grow it per engine instead: each engine gets the modes that are useful
for it, built on that engine's own semantics, rather than one generic layer that
fits every engine poorly. This document records the shape that takes.

## Principles

- **Depth per engine, not breadth.** An engine ships a mode only when that mode
  is right for it. A "safe" verdict on a migration that is in fact dangerous for
  that engine is worse than no verdict, so migration rules for an engine come
  from its documentation and are tested against real servers of the versions
  they claim.
- **The engine enforces safety, the parser explains it.** Every engine opens
  its connection in a mode the engine itself keeps read-only. Statement checks in
  dbscope exist to refuse early with a clear message, not as the barrier.
- **Capabilities, not engine checks in the UI.** A profile carries
  `capabilities` (query, ask, migrations, workload); the ERD is always there.
  The agent answers `501 unsupported` for a mode outside them before doing any
  work, and the UI hides what an engine lacks.

## Layout

```
internal/conn      Profile.Engine, Profile.Path, Capabilities; Postgres connections
internal/agent     engineSource routes each SchemaSource call by profile engine
  pools.go         PostgreSQL (pgx pools)
  sqlite_source.go SQLite (one read-only open per call)
  mysql_source.go  MySQL and MariaDB (database/sql pool per profile)
  mongo_source.go  MongoDB (a client per profile)
  redis_source.go  Redis and Valkey (a client per profile; reports cached 30 s)
internal/sqlite    SQLite: Open, Introspect, Check, Run, Test, Split, Analyze
internal/mysql     MySQL and MariaDB: Open, Introspect, Check, Run, Test
internal/mongo     MongoDB: Connect, Introspect (inferred), Parse, Check, Run, Test
internal/redis     Redis and Valkey: Connect, Analyze (keyspace report), Model, Test
internal/mytest    MySQL, MariaDB, MongoDB, Redis and Valkey containers for integration tests
internal/schema    the shared relational model; Model.Engine is empty for PostgreSQL
```

Relational engines map onto `schema.Model`. Non-relational engines will get
models of their own rather than being forced into tables and columns.

## Engines

| Engine | ERD | Query | Ask | Migrations | Workload / keyspace |
|---|---|---|---|---|---|
| PostgreSQL | yes | yes | yes | yes | yes |
| SQLite | yes | yes | not yet | yes | n/a |
| MySQL / MariaDB | yes | yes | not yet | yes | not yet |
| MongoDB | inferred | yes | not yet | not yet | n/a |
| Redis / Valkey | key patterns | n/a | n/a | n/a | keyspace report |
| DynamoDB | key schemas + sampled attributes | PartiQL SELECT | not yet | n/a | not yet |
| Cassandra / ScyllaDB | keyspace tables | CQL SELECT | not yet | not yet | not yet |
| Neo4j | labels and relationship types | read-only Cypher | not yet | not yet | not yet |
| Elasticsearch / OpenSearch | indices, aliases, data streams | search JSON and SQL | not yet | n/a | not yet |
| ClickHouse | tables, keys, parts | SQL under readonly | not yet | not yet | not yet |

### SQLite

A profile names an absolute file path; there is no host, user or password, so
saving one never needs the secrets vault. Research with sources:
[research-sqlite.md](research-sqlite.md).

**Opening.** `sqlite.Open` refuses a missing file (it never creates one) and
opens `file:...?mode=ro` with `query_only` and a busy timeout. Every connection
also gets an SQLite authorizer that allows reads only: a read-only open still
permits `ATTACH`, which reaches other files on the machine, so the authorizer
denies it along with every write, schema change, pragma assignment to
`query_only`/`writable_schema`/`trusted_schema`, and `load_extension`.

**WAL files.** Readers of a WAL database need its `-shm` file. In a writable
folder SQLite creates `-wal` and `-shm` next to the database, as it does for
any reader; the database file itself is never written. In a folder dbscope
cannot write, the read-only open fails with `SQLITE_READONLY_DIRECTORY`, so
`Open` retries with `immutable=1` and the connection test warns: SQLite then
ignores the `-wal` file (changes not yet checkpointed are not shown) and
assumes no other program writes during a read.

**ERD.** Read in one read transaction from `pragma_table_list` (which marks
virtual and shadow tables, skipped, and `WITHOUT ROWID`), `pragma_table_xinfo`
(hidden 2 and 3 are virtual and stored generated columns),
`pragma_foreign_key_list`, `pragma_index_list` and `pragma_index_xinfo`.

- `REFERENCES parent` without columns resolves to the parent's primary key.
- SQLite keeps foreign key names only in the CREATE text, so they are numbered (`orders_fk0`).
- A rowid alias is a single-column `INTEGER` primary key of a rowid table with no
  primary-key index (`INTEGER PRIMARY KEY DESC` has one and is not an alias).
- Row estimates come from `sqlite_stat1` after `ANALYZE`; without it they are
  unknown, since counting would read the whole file.
- Not read yet: CHECK constraints and generated-column expressions (they exist only in the CREATE text), triggers, attached databases.

**Queries.** `sqlite.Check` accepts one `SELECT`, `WITH`, `VALUES` or
`EXPLAIN` statement and refuses `load_extension`. The plan is
`EXPLAIN QUERY PLAN` as a tree of steps; SQLite gives no costs, so the UI shows
steps only and hides "with ANALYZE". The profile's statement timeout interrupts
the statement. Values follow the PostgreSQL mapping: integers beyond 2^53 and
non-finite floats stay strings, and blobs are `\x` hex.

**Migrations.** `sqlite.Analyze` replays the base schema (from the profile's
file, or a `.schema` script for `migrate check --sqlite-schema`) into a private
in-memory database and runs each migration statement there, in file order, so
SQLite's own grammar decides what is valid: a rejected statement becomes
`DBS-SQLITE-REJECTED` with the 12-step rebuild as the alternative. Statements
are split with a port of `sqlite3_complete`, tested against the real function,
so trigger bodies stay whole. The scratch database has no rows, so rules cover
what depends on data:

| Rule | When |
|---|---|
| `DBS-SQLITE-NOT-NULL-NO-DEFAULT` | ADD COLUMN NOT NULL without a non-NULL default on a table with rows (SQLite checks it row by row) |
| `DBS-SQLITE-TABLE-WORK` | DROP COLUMN, an index build, a table copy (`INSERT ... SELECT`) or an unfiltered UPDATE/DELETE, rated by the table's rows: 100,000 medium, 1,000,000 high, unknown medium |
| `DBS-SQLITE-VERSION` | RENAME COLUMN (3.25.0), DROP COLUMN (3.35.0, 3.35.5 for the corruption fixes), ALTER COLUMN and ADD/DROP CONSTRAINT (3.53.0) |
| `DBS-SQLITE-FK-PRAGMA-IN-TX` | `PRAGMA foreign_keys=OFF` inside BEGIN (high: a silent no-op), or outside one (medium: a no-op if the runner wraps files) |
| `DBS-SQLITE-DROP-CASCADE` / `-DROP-REFERENCED` | DROP TABLE of a parent while foreign keys may be on |
| `DBS-SQLITE-RENAME-FIRST` | a table renamed away and created again in the same file |
| `DBS-SQLITE-UNSAFE-PRAGMA` | `writable_schema`, `schema_version=`, `journal_mode=OFF/MEMORY`, `defer_foreign_keys=0` |
| `DBS-SQLITE-VACUUM` | VACUUM (twice the file size in free disk, no transaction, may renumber rowids) |
| `DBS-SQLITE-UNIQUE-DATA` | a unique index on a table with rows, which duplicates would make fail |

Every write holds SQLite's database-wide write lock (`DATABASE WRITE` in the
report). `ATTACH`, `VACUUM INTO` and `load_extension` are never run on the
scratch database (`DBS-SQLITE-NOT-RUN`), and its authorizer refuses attaching
any named file. Rules are checked with dbscope's SQLite (3.54.1); the version
an application links may accept fewer ALTER forms, which the version rule
calls out.

### MySQL and MariaDB

One profile engine, `mysql`, covers both; the server's `VERSION()` tells them
apart, and test results report `mysql` or `mariadb`. Network fields and
`sslMode` mean what they mean for PostgreSQL (`prefer` uses the driver's
preferred TLS; `verify-ca` and `verify-full` check the CA, the latter also the
host). Research with sources: [research-mysql.md](research-mysql.md). Tested
against MySQL 8.4 and MariaDB 11.8 containers (`internal/mytest`, images from
`public.ecr.aws/docker/library`).

**ERD.** From `information_schema` in one `START TRANSACTION READ ONLY`
(REPEATABLE READ): TABLES (InnoDB's `TABLE_ROWS` is an estimate that can be
off by 40-50%), COLUMNS (`auto_increment`, generated columns and their
expressions), STATISTICS (expression indexes on MySQL 8.0.13+; MariaDB has
none), KEY_COLUMN_USAGE with REFERENTIAL_CONSTRAINTS, and CHECK constraints
(TABLE_CONSTRAINTS on MySQL 8.0.16+, CHECK_CONSTRAINTS.TABLE_NAME on
MariaDB). MariaDB quotes literal defaults and spells DEFAULT NULL as `NULL`;
both are normalized. A profile's schemas are databases; empty means its own.

**Queries.** Every statement runs alone (`multiStatements` off) in a
`START TRANSACTION READ ONLY`, which the server enforces, after a session
limit: `max_execution_time` in milliseconds on MySQL (SELECT only) or
`max_statement_time` in seconds on MariaDB. `mysql.Check` refuses what a
read-only transaction still allows: `INTO OUTFILE`/`DUMPFILE`/`@var`, locking
reads, `:=` assignments, and `SLEEP`, `BENCHMARK`, `GET_LOCK`, `LOAD_FILE` and
similar. Its tokenizer treats `/*! ... */` and `/*M! ... */` as code, since the
servers execute them. The connection test lists write privileges from
`SHOW GRANTS`: a dedicated read-only user is the stronger guard. Plans are
tabular `EXPLAIN`, shown as steps.

**Migrations.** `mysql.Analyze` predicts, per ALTER clause, the cheapest
algorithm the server accepts (INSTANT, MariaDB's NOCOPY, INPLACE, COPY),
whether writes continue, and whether the table is rebuilt, from the server's
flavor and version and the base's column types, charsets, row formats, sizes
and foreign keys. There is no SQL parser dependency: the candidates (vitess
and TiDB's parser) added 11 to 17 MB to the binary and both reject MariaDB's
`ALGORITHM=NOCOPY`, `ALTER ONLINE TABLE` and `WAIT n`. A clause scanner on the
query tokenizer classifies statements instead; the server stays the judge of
exact grammar. Scripts are split on `;` and the client's `DELIMITER` command.

`TestDDLPredictions` (internal/mysql/ddl_verify_test.go) checks every
prediction against real servers: it runs each operation with each ALGORITHM,
then with `LOCK=NONE`, and compares InnoDB's table id before and after. All 28
cases match on MySQL 8.4 and MariaDB 11.8. Two research claims were corrected
this way: `CONVERT TO CHARACTER SET` copies on MySQL 8.4, and MariaDB renames
under `LOCK=EXCLUSIVE`. Rules for versions before MySQL 8.0.29 and MariaDB 10.4
come from the manuals and are not checked against a server.

| Rule | When |
|---|---|
| `DBS-MYSQL-BLOCKS-WRITES` | the operation blocks writes (COPY on MySQL and on MariaDB before 11.2 or with cascading foreign keys, FULLTEXT and SPATIAL index builds, auto-increment columns); critical from 1M rows or 1 GiB, high from 100k rows or 100 MiB, high when the size is unknown |
| `DBS-MYSQL-TABLE-WORK` | a rebuild or index build that lets writes continue; replicas apply it only after it finishes |
| `DBS-MYSQL-LOCK-WAIT` | DDL without a short `lock_wait_timeout` (the default is a year) or MariaDB's `WAIT n`/`NOWAIT` |
| `DBS-MYSQL-ALGORITHM` / `-LOCK-NONE` | an explicit ALGORITHM or LOCK=NONE the operation cannot honour, so the statement fails (ALGORITHM is exact on MySQL, a floor on MariaDB) |
| `DBS-MYSQL-NOT-NULL` / `-NULLABLE` | NOT NULL that fails on existing NULLs, or a MODIFY that drops NOT NULL by restating the column |
| `DBS-MYSQL-UNIQUE-DATA`, `-FK-UNCHECKED`, `-PARTITION-DATA`, `-TRUNCATE`, `-ROW-VERSIONS` | data-dependent failures, unchecked foreign keys, partition and table data loss, INSTANT's row-version limit |

The safe alternative names gh-ost or pt-online-schema-change by the table's
foreign keys and primary key. The CLI takes a live base (`--mysql URL` or
`DBSCOPE_MYSQL_DSN`) or only a version (`--mysql-version 8.4`, `mariadb-11.8`),
which assumes type changes copy the table.

### MongoDB

Research with sources: [research-mongodb.md](research-mongodb.md). Tested
against MongoDB 8.0 in a container, as a user with only the `read` role.

A profile has the usual network fields plus `options`, a URL query for the
connection string (`authSource=admin&replicaSet=rs0`; `srv=true` selects
`mongodb+srv`). Credentials never enter the URI; `authSource` defaults to
`admin`. Reads prefer secondaries, and every operation is tagged
`appName=dbscope` and `comment: dbscope`.

**Inferred schema.** Collections have no declared columns, so each is
sampled: up to 200 documents, with `$sample` (a cheap random cursor when it
asks for under 5% of more than 100 documents; disk use refused otherwise) or
a plain `find` for small collections and views, where `$sample` would scan.
Every field path (`address.city`, `items[].sku`, four levels deep) becomes a
column whose type is the mix of BSON types seen, e.g. `int 76% | decimal 24%`,
and whose comment gives its coverage when not every document has it. `_id` is
the primary key; indexes, unique indexes and the validator come from the
catalog; counts are `estimatedDocumentCount`. A reference is inferred from a
top-level field holding only ObjectIds and named after another collection
(`customer`, `customerId`, `customer_id` for `customers`) when most of its
distinct sampled values exist as that collection's `_id`; the share found is
the confidence.

**Queries.** A query is a command document in Extended JSON: `find`,
`aggregate`, `count` or `distinct`. `mongo.Check` refuses `$out`, `$merge`,
`$where`, `$function`, `$accumulator`, `mapReduce`, `bypassDocumentValidation`
and `writeConcern` at any depth (pipelines nest in `$lookup`, `$facet` and
`$unionWith`); server-side JavaScript is still on by default in 8.0. The
`read` role is the real guard: `$out` fails for it even past Check. Plans use
`explain` with `verbosity: "queryPlanner"`, since the command's default
executes the query, and a `COLLSCAN` is flagged. The statement timeout is the
call's context deadline, which the v2 driver also sends as `maxTimeMS` (adding
it by hand is a duplicate field); whichever side gives up first, the server
leaves no dbscope operation running, which the tests check with `$currentOp`.

### Redis and Valkey

Research with sources: [research-redis.md](research-redis.md). Tested against
Redis 7, Redis 8 and Valkey 8 in containers. One `redis` engine covers both;
Valkey reports `redis_version:7.2.4`, so `server_name`/`valkey_version` decide.
A profile's database is its number (default 0) and the user is optional.

There is no schema to draw, so the capability is a **keyspace report**
(`GET /profiles/{id}/keyspace`, the Keyspace tab of the dock) and the canvas
shows a **map of key patterns**: keys whose names differ only in id-like
segments (numbers, hex, UUIDs, long tokens between `:` or `/`) group into one
node, `user:*:profile`, with its type, memory and expiry.

**Reading safely.** The scan is SCAN with COUNT 100, deduplicated, then two
pipelined round trips per page: TYPE, PTTL and MEMORY USAGE (default samples),
then the O(1) length command for the type. Values are never read, and every
connection sends `CLIENT NO-TOUCH ON` so dbscope does not change keys' LRU or
LFU data. A scan stops at 20,000 keys or 10 seconds and says so; it backs off
when a page answers four times slower than the fastest one. Exact key and
expiry counts come from `INFO keyspace`.

**Read-only user.** Verified on all three servers: this user can produce the
whole report and is refused GET and SET.

```
ACL SETUSER dbscope on >PASSWORD resetkeys %R~* resetchannels -@all
  +ping +hello +info +dbsize +scan +type +pttl +strlen +llen +hlen +scard +zcard +xlen
  +memory|usage +client|no-touch
```

The connection test checks write access with `ACL DRYRUN`, which only a user
allowed to run it can use; otherwise it says it could not check.

| Rule | When |
|---|---|
| `DBS-REDIS-NO-MAXMEMORY` | maxmemory is 0 on a primary |
| `DBS-REDIS-NOEVICTION-FULL` | above 90% of maxmemory under noeviction |
| `DBS-REDIS-VOLATILE-NO-TTL` | a volatile-* policy while most scanned keys have no expiry, so it acts like noeviction |
| `DBS-REDIS-ALLKEYS-EVICTS-DATA` | an allkeys-* policy with no key expiring: primary data can be evicted |
| `DBS-REDIS-PATTERN-NO-TTL` | 100+ keys of a session, cache or tmp pattern with no expiry |
| `DBS-REDIS-BIG-KEY` | a string of 10 MiB or a collection of 10,000 elements or 100 MiB (cloud vendors' thresholds; Redis sets none) |
| `DBS-REDIS-KEYS-USED`, `-FLUSH-USED` | KEYS, FLUSHALL or FLUSHDB in INFO commandstats |
| `DBS-REDIS-BGSAVE-FAILED`, `-AOF-FAILED`, `-UNSAVED` | persistence failures, or many changes since the last snapshot with no AOF |
| `DBS-REDIS-FRAGMENTATION`, `-SWAP`, `-EVICTING` | fragmentation over 1.5 and 100 MiB, RSS below used memory, evictions |

Not yet: cluster mode (SCAN is per node), hot keys (LFU's OBJECT FREQ or Redis
8.6's HOTKEYS), and SLOWLOG, which needs an admin grant.

### DynamoDB

Research with sources: [research-dynamodb.md](research-dynamodb.md). Tested
against DynamoDB Local; nothing has been run against AWS itself.

A profile has no host, user or password, only `options`: `region`
(required), `profile` (a name from the AWS shared config) and `endpoint` (an
http(s) URL, for DynamoDB Local). Credentials come from the SDK's own chain
(environment, shared config and SSO, instance roles); dbscope stores none.
For a loopback endpoint with no credentials found, dummy ones are used, as
DynamoDB Local accepts any. The connection test warns when
`AWS_ACCESS_KEY_ID` in dbscope's environment overrides the named profile, or
when `AWS_ENDPOINT_URL(_DYNAMODB)` redirects requests.

**Table map.** Every table in the region becomes a relation in the `tables`
schema: its key schema is the primary key (partition and sort key in the
column comments), GSIs and LSIs are indexes (`gsi keys_only`, `lsi all`),
billing mode and size are the comment, and the TTL attribute is marked.
Other attributes have no declared type, so each table is sampled with an
eventually consistent Scan of at most 100 items or 25 read units, and every
attribute seen gets its type mix and coverage, as for MongoDB. Item counts
and sizes are DynamoDB's own, refreshed about every six hours.

**Queries.** A query is one PartiQL `SELECT`; anything else, or a second
statement, is refused before it is sent, and batch and transaction APIs are
never used. Rows are paged with `Limit` and the consumed read units are
reported. The plan says whether the statement reads by key or scans: it is a
`Query` only when the WHERE clause gives every partition-key attribute of the
table or index (a GSI can have up to four) with `=` or `IN`; otherwise it is
flagged as a full scan, which reads and bills every item before any filter.

**Read-only role.** IAM is the real guard; dbscope's checks only refuse early.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "dynamodb:ListTables", "Resource": "*" },
    { "Effect": "Allow",
      "Action": ["dynamodb:DescribeTable", "dynamodb:DescribeTimeToLive", "dynamodb:Scan",
                 "dynamodb:Query", "dynamodb:PartiQLSelect"],
      "Resource": ["arn:aws:dynamodb:REGION:ACCOUNT:table/*"] },
    { "Effect": "Deny",
      "Action": ["dynamodb:PartiQLInsert", "dynamodb:PartiQLUpdate", "dynamodb:PartiQLDelete"],
      "Resource": "*" }
  ]
}
```

To forbid full scans through PartiQL as well, add a Deny of `dynamodb:PartiQLSelect`
with the condition `"Bool": {"dynamodb:FullTableScan": "true"}`; dbscope's
attribute sampling uses `Scan`, which that condition does not cover.

**DynamoDB Local.** Without `-sharedDb` (or `-inMemory`), Local keeps a
separate database per access key and region, so dbscope with dummy
credentials sees none of the tables another client created with its own.
Start it with `-sharedDb` when sharing data with other tools.

Not yet: access-pattern findings (hot or low-cardinality partition keys,
large items, missing TTL, GSI back-pressure, provisioned capacity without
auto scaling) and CloudWatch or Contributor Insights reports.

### Cassandra and ScyllaDB

Research with sources: [research-cassandra.md](research-cassandra.md). Tested
against Cassandra 5.0 and ScyllaDB 2025.1 in containers, as a role with only
SELECT on the keyspace and with grants enforced. One `cassandra` engine
covers both; ScyllaDB reports `release_version` 3.0.8, so it is recognised
by `system.versions`, which exists only there (a plain role may not read it,
and that refusal is the signal). The driver is upstream gocql v2.

A profile is a contact point, port (9042) and keyspace; the user is optional
because a cluster may run without authentication. `options` may hold
`dc=NAME`, the local datacenter, for token-aware routing within it. The
keyspace is set on the session, since ScyllaDB lacks protocol v5's
per-query keyspace.

**Reading safely.** There is no read-only connection mode: grants are the
guard, and both servers ship with `AllowAllAuthorizer`, under which every
connection may write. The connection test lists the role's grants with
`LIST ROLES OF` and `LIST ALL PERMISSIONS OF`, which a role may run on
itself, reports MODIFY, CREATE, ALTER, DROP and AUTHORIZE grants and
superusers, and warns when LIST fails the way it does under AllowAll.
Statements are checked too: one `SELECT`, never `system_auth`, ScyllaDB's
writable `system.config`, the role tables or `salted_hash` (ScyllaDB's
Alternator secret). Every read is at `LOCAL_ONE`, because a read at a higher
level can trigger blocking read repair, which writes.

```cql
-- with authenticator: PasswordAuthenticator and authorizer: CassandraAuthorizer
CREATE ROLE dbscope_ro WITH LOGIN = true AND PASSWORD = '...';
GRANT SELECT ON KEYSPACE app TO dbscope_ro;
```

**Table map.** `system_schema` gives each table's columns by kind: the
partition key, clustering columns with their order, static columns, and the
primary key in that order. Indexes are named by kind: `secondary index`
(Cassandra's legacy, node-local kind), `sai`, `sasi`, or `global index` on
ScyllaDB, whose hidden `<index>_index` view is folded into it. Materialized
views appear with their base table. The table comment gives the compaction
strategy, default TTL and `gc_grace_seconds`.

**Queries and their plans.** Before a SELECT runs, the plan names the read
from the table's keys and indexes (docs/research-cassandra.md, 3):

| Plan | When | Flag |
|---|---|---|
| Single-partition read | every partition-key column with `=` | info if it also filters other columns |
| Multi-partition read | a partition-key column with `IN` | info: one read per value |
| Index read | an indexed column, no full partition key | warn for a legacy secondary index (asks every node) |
| Token range scan | `token(...)` bounds | warn |
| Full scan | no partition key, nothing to filter | warn |
| Full scan with filtering | non-key columns without the partition key | danger: needs ALLOW FILTERING |

Explain also prepares the statement without running it: both servers decide
at prepare time whether ALLOW FILTERING is required, so the server's own
refusal is added to the plan. Values render as JSON; varint, decimal, UUIDs,
blobs and times as text, and collections as JSON text.

Not yet: per-node health signals (`system_views` tombstones per read and
partition sizes on Cassandra, `system.large_partitions` on ScyllaDB),
schema-change risk for CQL DDL, and schema agreement across nodes.

### Neo4j

Research with sources: [research-neo4j.md](research-neo4j.md). Tested against
Neo4j 5.26 and 2026.10 Community and 2026.10 Enterprise (evaluation license)
in containers, the last also as a user with only the built-in `reader` role.
The driver is neo4j-go-driver v6, with telemetry off.

A profile is a host, port (7687), database (default `neo4j`) and an
optional user. It connects to one server with `bolt://`; `options`
`routing=true` uses a cluster's routing table (`neo4j://`). sslMode
`require` accepts any certificate (`+ssc`), `verify-ca` and `verify-full`
check it (`+s`); `disable` and `prefer` are plaintext, since Bolt has no
"TLS if offered".

**Reading safely.** Neo4j's read access mode routes a transaction to a
reader; it is not a permission. Community Edition has no roles at all, so
every Community user can write, and the connection test says so. On
Enterprise it reports the `admin`, `architect`, `publisher` and `editor`
roles as write access. dbscope therefore layers its own checks:

1. Before sending: no `CREATE`, `MERGE`, `SET`, `DELETE`, `REMOVE`,
   `FOREACH`, `DROP`, `ALTER`, user or privilege commands, `LOAD CSV`,
   `CALL { } IN TRANSACTIONS`, `USE`, `PROFILE` (which runs the query) or a
   second statement. Words inside strings, comments and backquoted names
   do not count.
2. Every `CALL`ed procedure must have mode `READ` in `SHOW PROCEDURES`; a
   `DBMS`-mode procedure such as `db.clearQueryCaches` is refused too.
3. `EXPLAIN` must report the query type `r` (read only).
4. The query runs in a read transaction with the profile's timeout that is
   always rolled back, never committed; a single server also refuses a
   write in it (`Neo.ClientError.Statement.AccessMode`, seen on 5.26).

A read-only Enterprise user: `CREATE USER dbscope SET PASSWORD '...'` then
`GRANT ROLE reader TO dbscope`.

**Graph map.** Each node label is a card with its count (from the count
store), its properties with their types (`db.schema.nodeTypeProperties`;
Cypher 5's Java type names are shown as Cypher's), uniqueness and key
constraints, and its indexes by type. Each relationship type leaving a
label is a row on its card, `-[:WORKS_AT]-> Company`, with its count and
properties, and an edge to the label at its other end.
`db.schema.visualization` reads the count store and can list label pairs no
relationship joins, so each pair is checked with a `LIMIT 1` match under a
2-second timeout; a pair that cannot be checked in time is kept and marked
unchecked.

**Plans.** Explain shows Neo4j's own plan (operators without the `@neo4j`
suffix, details and estimated rows) and flags:

| Flag | When |
|---|---|
| `all_nodes_scan`, `all_relationships_scan` (danger) | `AllNodesScan` or an all-relationships scan |
| `unbounded_path` (danger) | a variable-length pattern with no upper bound, such as `[:KNOWS*]` or `[*2..]` |
| `cartesian_product` (warn, danger from a million rows) | `CartesianProduct` |
| `eager` (warn) | `Eager`, which holds earlier rows in memory |
| `label_scan` (info, warn from 100,000 rows) | `NodeByLabelScan` |
| `index_scan` (info) | reading a whole index rather than seeking |
| `gql_03N9x`, `gql_01N5x` | the server's performance notifications and unknown label, type or property warnings, by GQLSTATUS code |

Results render nodes and relationships as JSON with their labels or type,
paths as their nodes and relationships, and temporal values as text.

Not yet: risks of schema-changing Cypher (constraint creation on existing
duplicates, index population), and relationship types between labels the
count store does not connect.

### Elasticsearch and OpenSearch

Research with sources: [research-elasticsearch.md](research-elasticsearch.md).
Tested against Elasticsearch 9.5 and 8.19 and OpenSearch 3.9 and 2.19 in
containers, all with security on, as a user with only read access. One
`elasticsearch` engine covers both; `GET /` tells them apart. dbscope speaks
plain HTTP rather than a client library: Elastic's Go client refuses
OpenSearch servers, and dbscope needs only a dozen read endpoints.

A profile is a host, port (9200) and an index pattern in the database field
(default `*`). The user is optional: with a user the password is basic
auth; with none, the password is an API key's `encoded` value. sslMode
`disable` and `prefer` use http, `require` https without checking the
certificate (OpenSearch's demo certificate), `verify-ca` and `verify-full`
https with it. AWS SigV4 signing for Amazon OpenSearch Service is not yet
supported.

**Reading safely.** HTTP has no read-only mode; the user's privileges are
the guard, and dbscope only ever calls read endpoints. The connection test
reports write access: on Elasticsearch with `_has_privileges` (`all` and
`manage` on the cluster, `write`, `delete`, `create_index`, `manage` on the
pattern), on OpenSearch from the roles in `authinfo`. OpenSearch 2.x's demo
security configuration gives every user `own_index`, which writes to an
index named after the user, and the test says so. A cluster with security
off is warned about.

```
# Elasticsearch
POST _security/role/dbscope_ro
{"cluster": ["monitor"],
 "indices": [{"names": ["*"], "privileges": ["read", "view_index_metadata", "monitor"]}]}

# OpenSearch: readall_and_monitor is not enough; it lacks indices:admin/mappings/get,
# which mappings and SQL need (seen on 3.9)
PUT _plugins/_security/api/roles/dbscope_ro
{"cluster_permissions": ["cluster_monitor"],
 "index_permissions": [{"index_patterns": ["*"], "allowed_actions": ["read", "indices_monitor",
   "indices:admin/mappings/get", "indices:admin/get", "indices:admin/resolve/index",
   "indices:admin/data_stream/get", "indices:admin/validate/query"]}]}
```

**Index map.** `_resolve/index` lists the indices, aliases and data streams
the pattern matches; dot-prefixed system and backing indices are left out
unless the pattern starts with a dot. Each index is a card whose columns are
its mapped fields: objects flattened to dotted paths, `nested` fields marked
(each object is a hidden document), multi-fields under their parent
(`title.keyword`) and runtime fields. Document counts come from a `size: 0`
search on `_index`, because index stats count nested objects as documents.
The comment gives shards, replicas, size, the field count against
`index.mapping.total_fields.limit` (counted as Elasticsearch does: every
object level, leaf, multi-field and runtime field), the `dynamic` setting
and aliases, then risks: within 80% of the field limit, no replicas, any
`index.blocks.*` (`read_only_allow_delete` usually means the disk
flood-stage watermark), shards above 50 GB or 200M documents. A data stream
is one card with its write index's fields. An alias is a view over its
indices whose columns come from `_field_caps`; a field with more than one
type across them is a **type conflict**, named per index, since it breaks
sorting and aggregations across the alias.

**Queries.** The query tab takes a search request as JSON, with an optional
`"index"` (default: the profile's pattern), or SQL (`SELECT`, `SHOW`,
`DESCRIBE`; Elasticsearch `_sql`, OpenSearch `_plugins/_sql`). Before
sending: `from + size` above 10,000 (`index.max_result_window`) is refused,
as are `scroll` and point-in-time requests, which hold search contexts
open. Searches always send a `timeout` and `allow_partial_search_results=false`,
and `size` is capped at the row limit. Results show `_index`, `_id`,
`_score` and the top-level `_source` fields; aggregations show one row each.
Elasticsearch SQL cursors are closed at once when more rows remain.

**Explain** checks a search without running it: `_validate/query` with
`explain` shows the rewritten Lucene query, and flags mark searches of every
index and expensive queries (script, wildcard, regexp, fuzzy, prefix,
percolate, joins, `query_string`, `more_like_this`) and terms aggregations
asking for more than 10,000 buckets. For SQL it shows the search request
Elasticsearch would send (`_sql/translate`) or OpenSearch's plan
(`_plugins/_sql/_explain`), checked the same way.

Not yet: AWS SigV4, PPL (it needs another cluster permission), cluster
health and shard allocation, and risks of mapping changes.

### ClickHouse

Research with sources: [research-clickhouse.md](research-clickhouse.md).
Tested against ClickHouse 26.8 and 25.8 (the previous LTS) in containers,
as users at readonly 0, 1 and 2 and as one with INSERT. The driver is
clickhouse-go v2 over the native protocol.

A profile is a host, port (9000; 9440 with TLS), database (default
`default`) and user (default `default`). sslMode `disable` and `prefer`
are plaintext, since the native protocol has no "TLS if offered".

**Reading safely.** The server enforces `readonly`, by the user's own
level (all verified on 26.8):

| User's readonly | What dbscope sends with each query |
|---|---|
| 0 | `readonly = 1`, `max_execution_time`, `max_result_rows` with `break`; a query's own `SETTINGS readonly = 0` is then refused |
| 1 | nothing: such a user may change no setting, so the limit is dbscope's own deadline and row cap |
| 2 | the limits only: `readonly` itself may not change. Level 2 still allows SET, RESTORE and temporary tables, which dbscope's check refuses |

clickhouse-go turns any context deadline over a second into a
`max_execution_time` setting, which a readonly-1 user may not send, so
dbscope passes the driver a context without the deadline and cancels it
itself. `max_result_rows` is checked per block (a 100-row block came back
under a limit of 11), so dbscope caps the rows too. Before sending, only
`SELECT`, `WITH`, `SHOW`, `DESCRIBE` and `EXISTS` are accepted, never a
second statement, `INTO OUTFILE`, or a table function that reaches outside
the database (`url`, `s3`, `file`, `remote`, `mysql`, `postgresql` and the
like). The connection test reads `SHOW GRANTS` and reports `INSERT`,
`ALTER`, `CREATE`, `DROP` and similar grants.

```sql
CREATE USER dbscope IDENTIFIED BY '...' SETTINGS readonly = 1;
GRANT SELECT ON app.* TO dbscope;
-- optional, for part and partition counts, pending mutations and skip indexes:
GRANT SELECT ON system.parts TO dbscope;
GRANT SELECT ON system.mutations TO dbscope;
GRANT SELECT ON system.data_skipping_indices TO dbscope;
```

**Table map.** `system.tables` and `system.columns` (readable by every
user) give each table's engine, `ORDER BY`, primary key (in key order),
partition key, rows, compressed size, active parts and TTL, and each
column's role in the keys, codec and DEFAULT, MATERIALIZED or ALIAS
expression. A materialized view points at its `TO` table (read from its
CREATE statement; `system.tables.target_table` is new in 26.x). Risks:

| Risk | When |
|---|---|
| no primary index | a MergeTree with `ORDER BY tuple()`: every query reads the whole table |
| Nullable sorting key | a Nullable column in `ORDER BY` |
| too many parts | a partition above 1,000 active parts (inserts slowed) or 3,000 (inserts fail) |
| too many partitions | above 1,000: parts never merge across partitions |
| mutations | pending, with the oldest start, and any failing with its reason |

Part, partition and mutation figures need the grants above; without them
the map says so rather than guessing.

**Plans.** Explain runs `EXPLAIN PLAN indexes = 1, json = 1` and `EXPLAIN
ESTIMATE`, neither of which executes the query (`EXPLAIN ANALYZE` does,
and is refused). Each read shows its table, estimated rows and parts, and
how many granules the primary key selects out of how many; a read that
keeps 90% or more of at least 8 granules is flagged as not using the
sorting key. A filter on the second key column is not flagged when the
first has few values, since the server's generic exclusion search prunes
it. Skip indexes that skip nothing and `FINAL` are flagged too. From 25.9
the index figures need the query condition cache off, which dbscope sets
except for readonly-1 users, who may not.

Not yet: migration risk of ALTER (which ALTERs rewrite parts), replication
lag and queues, and cardinality order of key columns, which needs reading
data.

## Next engines

The remaining candidates from the October 2026 list: SQL Server and Oracle. Each gets research first, then a build tested against a real server.

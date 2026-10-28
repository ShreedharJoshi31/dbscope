# Research: MongoDB support

Collected 2026-10-28 by a research worker from primary sources (cited); nothing was run against a server during research, and claims it could not verify are marked "unverified". The design dbscope adopted is in docs/ENGINES.md.

Collected 2026-10-28 from primary sources, cited inline as reference links (the URL list is at the end). Nothing was run against a MongoDB server. Claims without a primary source are marked "unverified".

Read this first:
- **The unversioned manual now tracks the 9.0 series.** The [release notes index][relnotes] lists a 9.0 series, and several `/docs/manual/` pages carry "New in version 9.0" notes. Where 7.0 and 8.0 differ, I cite the `/docs/v7.0/` or `/docs/v8.0/` page. For the other facts I read the current page, did not diff it against older versions, and flagged any version notes on it.
- **The read role has gaps.** The built-in `read` role covers sampling, the catalog, `$collStats` and explain. It does not cover `$indexStats`, the profiler collection, or other users' `$currentOp` entries. Those need `clusterMonitor` or `dbAdmin` ([roles source][roles-src]).
- **The `explain` command defaults to executing the query.** Its default verbosity is `allPlansExecution`, while the shell helper defaults to `queryPlanner` ([explain][explain]).
- **Server-side JavaScript is on by default in 8.0.** It is deprecated in 8.0 but still enabled (`security.javascriptEnabled` defaults to true) ([function v8.0][function-8.0], [config source][config-src]).
- **The Go driver v2 has no `SetMaxTime`.** Timeouts come from the client `Timeout` option or a context deadline, and the driver derives `maxTimeMS` from them ([migration 2.0][migration-2.0], [CSOT spec][csot-spec]).
- **dbscope already lists the driver.** `go.mongodb.org/mongo-driver/v2 v2.9.2` appears in dbscope's go.mod as an `// indirect` requirement (`/Users/shreedharjoshi/Flash/dbscope/go.mod`). I could not run `go mod why`, so the module that pulls it in, and whether it is linked into the binary today, are unverified.

---

## 1. Sampling

### 1.1 `$sample` semantics and cost

The 7.0 and 8.0 pages give the same rules ([sample v8.0][sample-8.0], [sample v7.0][sample-7.0]). `$sample` uses a **pseudo-random cursor** only when all of these hold:
1. `$sample` is the first stage of the pipeline.
2. N is less than 5% of the documents in the collection.
3. The collection has more than 100 documents.

The 5% rule:
- You cannot configure the 5% threshold ([sample v8.0][sample-8.0]).
- Above 5%, `$sample` does a top-k sort by a generated random value. That sort can spill to disk if the sampled documents exceed 100 MB ([sample v8.0][sample-8.0]).
- If any of the three conditions fails, `$sample` reads every document from the previous stage or a collection scan, then does a random sort. That sort is subject to the sort memory limits ([sample v8.0][sample-8.0]).

The pseudo-random cursor is not uniform. The 8.0 page says it descends the internal B-tree at random, so results can skew ([sample v8.0][sample-8.0]). Skew is more likely when:
- documents are unevenly distributed,
- the collection has many deleted documents,
- many documents are invisible to the cursor's transaction.

The 7.0 page does not include this skew paragraph ([sample v7.0][sample-7.0]). To get a uniform sample, the 8.0 page says to put `$sample` after another stage or to ask for more than 5% ([sample v8.0][sample-8.0]).

Views:
- The server appends `$sample` to the end of the view's pipeline. So on a view it is never the first stage and always causes a collection scan ([sample v8.0][sample-8.0]).

Sharded clusters:
- Each shard samples independently. mongos then samples the merged results down to N ([sample v8.0][sample-8.0]).

Time-series collections:
- I found no primary source on how `$sample` behaves on a time-series collection. **Unverified.** The [time-series limitations page][ts-limits] does not mention `$sample`.
- Related verified fact: MongoDB does not create an `_id` index on time-series collections ([ts-limits][ts-limits]). dbscope should run `explain` before sampling a time-series collection rather than assume the pseudo-random path.

### 1.2 Bounding the work

| Control | Behaviour | Source |
|---|---|---|
| `maxTimeMS` (find and aggregate) | Without it, operations never time out. `0` also means unbounded. The server stops the operation only at designated interrupt points, using the same mechanism as killOp. | [aggregate][aggregate], [find][find] |
| `cursor.batchSize` (aggregate) | `batchSize: 0` returns a cursor with an empty first batch, with little server work. | [aggregate][aggregate] |
| `batchSize` (find) | The default first batch is the lesser of 101 documents or 16 MiB. Later batches are capped at 16 MiB. | [find][find] |
| `limit` (find) | `0` means no limit. | [find][find] |
| `allowDiskUse` | Overrides `allowDiskUseByDefault` for one command. | [aggregate][aggregate], [find][find] |
| `allowDiskUseByDefault` | Defaults to true on mongod. Stages that need more than 100 MB write temporary files unless the command sets `allowDiskUse: false`. mongos never writes temporary files. | [parameters][params] |
| `usedDisk` | Appears in profiler and log entries when a stage spilled to disk. | [aggregate][aggregate] |
| Disconnect | If the client disconnects, the server marks find, and aggregate without `$out` or `$merge`, for killOp. | [aggregate][aggregate], [find][find] |
| `comment` | Shows up in logs, profiler entries and currentOp, and getMore commands inherit it. dbscope can tag its own operations this way. | [aggregate][aggregate] |

Recommendation (inference): for sampling, send `allowDiskUse: false` so a top-k sort above 5% fails instead of writing temp files on a production host.

### 1.3 Read preference for production

- The modes are `primary` (the default), `primaryPreferred`, `secondary`, `secondaryPreferred` and `nearest` ([read preference][readpref]).
- `secondaryPreferred` reads from eligible secondaries and falls back to the primary if none is eligible ([read preference][readpref]).
- Any mode other than `primary` may return stale data ([read preference][readpref]).
- `maxStalenessSeconds` must be 90 or more. Smaller values raise an error ([staleness][staleness]). With `secondaryPreferred`, if no secondary is within the staleness bound, the client reads from the primary ([read preference][readpref]).
- **Caution:** since 5.0, a pipeline with `$merge` or `$out` can run on a secondary when read preference allows it, but its writes still go to the primary ([read preference][readpref], [merge][merge]). `secondaryPreferred` therefore does not make a pipeline read-only.

---

## 2. Catalog

### 2.1 `listCollections`

Source: [listCollections][listCollections].

- Each result has `name` and `type`. `type` is `collection`, `view` or `timeseries`.
- Results also carry `options`, `info.readOnly`, `info.uuid` and `idIndex`.
- `nameOnly: true` returns only name and type. With `nameOnly`, the filter can use only name and type.
- `options` mirror the `create` options ([create][create]):
  - `capped`, `size`, `max`
  - `timeseries.{timeField, metaField, granularity, bucketMaxSpanSeconds, bucketRoundingSeconds}`
  - `expireAfterSeconds`
  - `clusteredIndex`. Its `key` must be `{_id: 1}` and `unique` must be true (added in 5.3).
  - `validator`, `validationLevel`, `validationAction`
  - `viewOn`, `pipeline`
  - `collation`, `changeStreamPreAndPostImages`, `encryptedFields`
- `validationLevel` values ([create][create]):
  - `off`
  - `strict` (the default)
  - `moderate`
  - `constraint`, which is new in **9.0** and not available in 7.0 or 8.0
- `validationAction` values: `error` (the default) and `warn` ([create][create]). `errorAndLog` is new in **8.1** ([validation action][val-action]).
- View definitions are public. `listCollections` and explain both show the view pipeline ([create][create]).
- Since 5.0, `listCollections` takes no intent lock on collections or the database ([listCollections][listCollections]).
- Privileges: the command needs the `listCollections` action, which the built-in `read` role grants. A user without that action can still call it with both `nameOnly: true` and `authorizedCollections: true`. They then get names and types only for collections they hold privileges on ([listCollections][listCollections]).

### 2.2 `listIndexes`

- `listIndexes` returns every index, including hidden indexes and indexes still being built ([listIndexes][listIndexes]).
- The `hidden` field appears only when it is true ([listIndexes][listIndexes]).
- It does not return Atlas Search indexes. Those need `$listSearchIndexes` ([listIndexes][listIndexes]).
- The `read` role grants it ([listIndexes][listIndexes]).

Index spec fields, from [createIndexes][createIndexes]:

| Field | Meaning |
|---|---|
| `key` | Direction `1` or `-1`, or a type such as `"text"`, `"2dsphere"`, `"2d"` or `"hashed"`. |
| `unique` | Not available for hashed indexes. |
| `sparse` | 2dsphere, 2d and text indexes are sparse by default and ignore this option. |
| `partialFilterExpression` | Allowed operators: equality, `$exists: true`, `$gt`, `$gte`, `$lt`, `$lte`, `$type`, `$and`, `$or`, `$in`, `$geoWithin` and `$geoIntersects`. |
| `expireAfterSeconds` | Marks a TTL index. Range 0 to 2147483647. |
| `hidden` | The planner does not consider a hidden index. |
| `weights`, `default_language`, `language_override`, `textIndexVersion` | Text index options. |
| `2dsphereIndexVersion`, `bits`, `min`, `max` | Geospatial options. |
| `wildcardProjection` | Valid only with key `{"$**": 1}`. |
| `collation` | Index collation. |

- Wildcard indexes are sparse and leave out `_id` by default. Since 7.0 they support descending order ([createIndexes][createIndexes]).
- Since 6.3, 6.0.5 and 5.0.16, `wildcardProjection` is stored exactly as submitted ([listIndexes][listIndexes]).
- **Unverified:** how a text index's `key` is stored and reported, for example as `_fts`/`_ftsx` fields. I did not find this on a current primary page. dbscope should detect text indexes from real `listIndexes` output during testing.

### 2.3 `$collStats` (and the deprecated `collStats` command)

- The `collStats` command has been deprecated since 6.2. Use the `$collStats` stage instead ([collStats command][collStatsCmd]).
- `$collStats` must be the first stage of the pipeline and is not allowed in transactions ([$collStats][collStatsStage]).
- On a sharded collection it returns one document per shard ([$collStats][collStatsStage]).

`storageStats: {}` returns ([$collStats][collStatsStage]):
- `size`, `count`, `avgObjSize`, `storageSize`, `freeStorageSize`, `capped`
- `nindexes`, `indexDetails`, `indexBuilds`
- `totalIndexSize`, `indexSizes`, `totalSize`, `scaleFactor`
- a `timeseries` sub-document on time-series collections

Other outputs ([$collStats][collStatsStage]):
- `count: {}` reads collection metadata. It is fast but can be inaccurate on sharded clusters.
- `storageStats`, `count` and `queryExecStats` all error on a view.
- `queryExecStats.collectionScans.{total, nonTailable}` counts queries that did a collection scan. This is a cheap signal for section 4.
- After an unclean shutdown, size and count can drift until `validate` runs.

Privileges:
- `$collStats` needs the `collStats` action ([privilege actions source][actions-src]). The `read` role includes `collStats` ([roles source][roles-src]).
- The `find` action does not cover `$collStats`, `$out` or `$indexStats` ([privilege actions source][actions-src]).

### 2.4 What a user with only `read` can run

The `read` role grants these actions on the database's non-system collections ([roles source][roles-src]):
- `changeStream`, `collStats`, `dbHash`, `dbStats`, `find`, `killCursors`
- `listCollections`, `listIndexes`, `listSearchIndexes`

So a `read` user can run `find`, `aggregate` (with the exceptions below), `count`, `distinct`, `listCollections`, `listIndexes`, `$collStats`, `dbStats`, and `explain` of those commands. Explain only needs permission to run the underlying command ([explain][explain]).

A `read` user cannot:
- run `$indexStats`. It needs the `indexStats` action, which `clusterMonitor` provides ([indexStats][indexStats], [roles source][roles-src]).
- read `system.profile`. Built-in roles cover system collections only per collection. `dbAdmin` and `clusterMonitor` grant `find` on `system.profile` ([roles source][roles-src]).
- see other users' operations in `$currentOp`. That needs `inprog`, and on sharded clusters `inprog` is needed for any `$currentOp` ([currentOp][currentOp]).

Atlas uses different built-in roles, built from the same privilege actions ([roles source][roles-src]). Caveat: the roles fact comes from the `master` branch of the mongodb/docs GitHub source. The rendered roles page loads its content dynamically and my fetcher could not read it.

---

## 3. Read-only safety for user and LLM-written queries

### 3.1 Stages and operators with side effects

| Item | Effect | Source |
|---|---|---|
| `$out` | Writes the pipeline output to a collection. It must be the last stage. If the target exists, the whole collection is replaced atomically. It can create databases. Since 7.0.3 and 7.1 it can write to time-series collections. | [out][out] |
| `$merge` | Writes into a collection, which can be the source collection itself. It must be the last stage and can create collections and databases. | [merge][merge] |
| `$where`, `$function`, `$accumulator`, `mapReduce` | Run JavaScript on the server. | [server-side JS][ssjs] |
| `mapReduce` with a non-inline `out` | Writes a collection. The `find` action covers only `{out: inline}`. | [privilege actions source][actions-src] |
| `$currentOp` | Must be the first stage and runs only on the admin database. | [currentOp][currentOp] |

- A view pipeline cannot contain `$out` or `$merge`, including inside `$lookup` or `$facet` sub-pipelines ([create][create]). By analogy, dbscope's checker should look inside nested pipelines too (inference).
- The `bypassDocumentValidation` and `writeConcern` aggregate options matter only with `$out` or `$merge` ([aggregate][aggregate]). Seeing either in a submitted command is a red flag (inference).

### 3.2 Server-side JavaScript in 8.0

- **Deprecated starting in 8.0.** `$accumulator`, `$function` and `$where` are deprecated, and the server logs a warning when they run ([function v8.0][function-8.0]).
- **Not disabled by default.** `$function` needs server-side scripting, which is enabled by default ([function v8.0][function-8.0]). `security.javascriptEnabled` defaults to true ([config source][config-src]).
- To disable it, start mongod or mongos with `--noscripting` or set `security.javascriptEnabled: false` ([server-side JS][ssjs]).
- In 9.0, server-side JavaScript is unavailable on ppc64le ([server-side JS][ssjs]). The current (9.0) `$function` page I fetched did not show the deprecation banner, while the 9.0 configuration source still includes a deprecation note ([function][function], [config source][config-src]). Whether 9.0 changed the deprecation status is **unverified**.

dbscope must reject `$where`, `$function` and `$accumulator` itself, because it cannot assume the server has scripting off.

### 3.3 Options that bound cost

`maxTimeMS`, `limit`, `batchSize` and `allowDiskUse: false` are covered in 1.2.

- Since 8.0, `setQuerySettings` can reject a query shape for the whole cluster, and 9.0 adds a per-command `querySettings` field ([aggregate][aggregate], [find][find]). These are admin tools. dbscope should not set them.
- Go driver specifics are in section 6.

### 3.4 Enforcing read-only at the server

- **Primary control:** connect as a user that holds only `read`. Its actions include no `insert`, `update`, `remove`, `createIndex`, `collMod` or `dropCollection` ([roles source][roles-src]).
- With such a user, `$out` and `$merge` fail authorization. `find` does not cover `$out` ([privilege actions source][actions-src]). Writing via `$merge` needs insert or update on the target. The `$merge` privilege text was in a part of the source I did not fully read, so that detail is inferred from the role's action list.
- **readConcern does not enforce read-only.** Read concern controls the consistency and isolation of reads ([read concern][readconcern]). It grants or removes no privileges.
- Use `"local"`, which is the default ([read concern][readconcern]). Avoid `"linearizable"`, which works only on the primary and can wait for replication ([read concern][readconcern]).
- The profiler can write to `system.profile` even in databases that are otherwise read-only ([profiler output][profiler-out]). "Read-only" in dbscope's UI should mean "no data or schema writes by dbscope" (inference).

### 3.5 `explain("queryPlanner")`

- **Verbosity defaults differ.** The `explain` command defaults to `allPlansExecution`. `db.collection.explain()` defaults to `queryPlanner` ([explain][explain], [explain method][explain-method]).
- `executionStats` mode runs the winning plan to completion ([cursor.explain v7.1][cursor-explain-7.1]). `queryPlanner` mode runs only the optimizer and returns the chosen and rejected plans ([explain][explain]). The pages describe execution only for the other two modes, so I read `queryPlanner` as non-executing. This is inferred and not stated in one sentence.
- Explaining a write does not modify data ([explain][explain]). In `allPlansExecution` mode, the update's query predicate does run ([explain][explain]).
- You cannot use `executionStats` or `allPlansExecution` on a pipeline that contains `$out`. `queryPlanner` is allowed ([explain][explain]). Whether a `queryPlanner` explain of `$out` or `$merge` can have side effects is **unverified**. dbscope should reject those stages before calling explain.
- Explain ignores the plan cache and does not add entries to it ([explain][explain], [explain results][explain-results]).
- Output nested deeper than 100 levels is truncated ([explain][explain]).

What `queryPlanner` returns ([explain results][explain-results]):
- `namespace`, `indexFilterSet` and `parsedQuery`
- `planCacheShapeHash`. From 8.0 it duplicates `queryHash`, which will be removed later.
- `planCacheKey`
- `winningPlan`, a tree of `stage`/`inputStage(s)` nodes
- `rejectedPlans`, which have the same shape as `winningPlan` and may be empty
- for aggregations, `optimizedPipeline`, and otherwise a top-level `stages` array

Stage names include ([explain results][explain-results]):
- `COLLSCAN`, `IXSCAN`, `FETCH`, `GROUP`
- `SHARD_MERGE`, `SHARDING_FILTER`, `EOF`
- 8.0 `EXPRESS_*` stages, such as `EXPRESS_IXSCAN` and `EXPRESS_CLUSTERED_IXSCAN`

On sharded collections, the plan for each shard sits under `queryPlanner.winningPlan.shards[]` ([explain results][explain-results]).

The format is not guaranteed stable, even under the Stable API ([explain][explain]).

---

## 4. Index advice

### 4.1 Why a query needs a collection scan

- Without a suitable index, MongoDB must scan every document ([indexes][indexes]).
- A compound index supports queries on its prefixes. For `{item, location, stock}`, a query on `location`, `stock`, or `location` plus `stock` cannot use the index ([compound][compound]).
- An index on an embedded document is used only by queries that match the whole embedded document, not one of its fields ([indexes][indexes]).
- `$ne` and `$nin` are not selective. With an index they often perform no better than a full scan ([query optimization][qopt]).
- An index on a prefix of another index can be removed if neither has a sparse or unique constraint ([compound][compound]). This is useful for redundant-index findings.
- Time-series collections have no `_id` index by default ([ts-limits][ts-limits]).

### 4.2 ESR guideline

Source: [ESR][esr].
- Put equality fields first, in any order among themselves.
- Then put sort fields before range fields, to avoid an in-memory sort.
- If the range predicate is very selective, putting range before sort (ERS) can be better.
- `$ne`, `$nin` and `$regex` count as range operators.
- `$in` alone counts as equality.
- `$in` combined with a sort:
  - fewer than 201 elements behaves like equality, through SORT_MERGE.
  - 201 or more elements behaves like a range.
  - The docs say the 201 threshold is not guaranteed to stay the same across versions.
- Example from the page: for `{directors: "David Lynch", runtime: {$lt: 130}}` sorted by `year`, the recommended index is `{directors: 1, year: 1, runtime: 1}`.

### 4.3 `$indexStats` for unused indexes

Source: [indexStats][indexStats].
- Output per index: `name`, `key`, `host`, `accesses.ops`, `accesses.since`, `shard`, `spec`, and `building` (only when true).
- `accesses.ops` counts user operations only. It excludes TTL deletes, chunk migration and replication.
- The counts are per node. Run `$indexStats` on every node for a full picture.
- Counters reset on mongod restart, on index drop and recreate, and when collMod changes the index ([indexStats][indexStats]). For example, changing `hidden` or `expireAfterSeconds` resets them ([collMod][collMod]).
- `$indexStats` must be the first stage and is not allowed in transactions.
- It needs `clusterMonitor` or the `indexStats` action.

Recommendation (inference): report "0 ops since T on node H" and show T. A recent `since` is not evidence of disuse.

### 4.4 Profiler and `$currentOp` as sources of access patterns

Profiler ([profiler][profiler]):
- The profiler is off by default (level 0).
- Level 1 records operations slower than `slowms` (default 100 ms) or operations matching a filter. Level 2 records everything.
- `slowms` and `sampleRate` apply to the whole process.
- The profiler cannot be enabled on mongos. Each mongod must be enabled separately.
- `system.profile` is a capped collection with a default size of 1 MB.
- The docs warn that profiling can degrade performance. They suggest `$queryStats` as an alternative.

Useful profiler fields ([profiler output][profiler-out]):
- `op`, `ns`, `command`. The command is truncated if it exceeds 50 KB.
- `planSummary`, for example `"IXSCAN { a: 1, _id: -1 }"`.
- `keysExamined`, `docsExamined`, `nreturned`, `hasSortStage`, `usedDisk`, `millis`.
- `queryShapeHash` (8.0+), `planCacheShapeHash`/`queryHash`, `planCacheKey`.
- `appName`, `user`, `ts`.

`$currentOp` ([currentOp][currentOp]):
- It must be the first stage and runs only on the admin database.
- `allUsers: false` (the default) shows only the caller's operations. `allUsers: true` needs `inprog`.
- The `currentOp` command has been deprecated since 6.2 ([currentOp][currentOp]).

`$queryStats` ([queryStats][queryStats]):
- The docs call it unsupported and say its output may change.
- It is enabled on Atlas clusters of tier M10 and above.
- It needs `queryStatsRead`, which `clusterMonitor` provides.
- Starting in 9.0, it samples 1% of operations by default.

`$collStats.queryExecStats.collectionScans` counts collection scans for each collection and needs only `read` (section 2.3).

Privileges summary: profiler reads need `find` on `system.profile` (`dbAdmin` or `clusterMonitor`). `$currentOp` with `allUsers: true` needs `inprog` (`clusterMonitor`). `$indexStats` needs `indexStats` (`clusterMonitor`) ([roles source][roles-src]). dbscope should never change the profiling level itself (inference). Changing it is an admin write, and the docs warn about its overhead.

---

## 5. Risky schema operations

### 5.1 Index builds on populated collections (FCV 4.4 and later)

Lock sequence ([index builds][index-build]):

| Phase | Lock | Effect |
|---|---|---|
| 1. Start | Exclusive X on the collection, not yielded | Blocks all reads and writes, including replicated writes. |
| 2. Scan | Downgraded to IX, yielded periodically | Each document's keys go to an external sorter. Concurrent writes go to a "side writes" table. |
| 3. Wait for votes | (IX continues) | Each member votes. The primary waits for the commit quorum. |
| 4. Drain | Shared S | Blocks writes while the side-writes table drains. Replication may pause in this phase. |
| 5. Commit | Exclusive X | Held only long enough to finish draining, check constraints and mark the index ready. |

- The page describes the S and X locks at the end as usually brief. How long they last depends on how many side writes are left ([index builds][index-build]).
- Builds run simultaneously on all data-bearing members. The primary writes `startIndexBuild`, then `commitIndexBuild` or `abortIndexBuild`, to the oplog ([index builds][index-build]).

`commitQuorum` ([createIndexes][createIndexes], [index builds][index-build]):
- The default is `votingMembers`, meaning all data-bearing voting members. Other values are `majority`, a number, or a tag name.
- If a voting member is unreachable under the default quorum, the build can hang.
- Changed in 8.0: commit quorum counts members ready to commit. Write concern then counts how many members replicated the commit oplog entry. Before 8.0, write concern counted members that had finished the build.

Unique indexes ([index builds][index-build]):
- Constraint violations are checked only at the end of the build. A build can therefore scan the whole collection and then fail.
- On sharded clusters the build can succeed on some shards and fail on others, which leaves inconsistent indexes.

Resource limits ([index builds][index-build]):
- `maxIndexBuildMemoryUsageMegabytes` defaults to 200 MB per `createIndexes` call. The call splits that memory across its indexes and spills to `_tmp` beyond it.
- `maxNumActiveUserIndexBuilds` defaults to 3. Extra builds block until a slot frees.

7.1 and later ([index builds][index-build], [parameters][params]):
- Errors are reported early, except duplicate-key errors.
- A secondary can ask the primary to abort a failing build instead of crashing.
- `indexBuildMinAvailableDiskSpaceMB` (default 500 MB) stops builds when disk space runs low.
- Before 7.1, an index build error could crash a secondary. This applies to 7.0.

Other points ([index builds][index-build]):
- The `background` option is ignored.
- Heavy write load slows the build and lowers write throughput.
- Do not use killOp on an index build. Use `dropIndexes`.

### 5.2 `collMod` validator changes and `$jsonSchema` on existing documents

Source: [collMod][collMod] unless noted.
- Validation happens on inserts and updates. Existing documents are not checked until they are modified.
- Validators cannot use `$near`, `$nearSphere`, `$text` or `$where`.
- Validators are not allowed in the admin, local or config databases, or on `system.*` collections.

Validation levels ([validation level][val-level]):
- `strict`: all inserts and updates are validated. The level does not check documents retroactively, so invalid old documents stay. Updates to them then fail until they are fixed.
- `moderate`: updates to documents that are already invalid skip validation.
- `constraint` (9.0 only):
  - scans every document and fails the collMod if any document is invalid,
  - requires `prepareConstraintValidationLevel` first,
  - requires the action to be `error` or `errorAndLog`,
  - blocks validator changes and `bypassDocumentValidation`.

Validation actions ([validation action][val-action]):
- `warn` allows invalid writes and logs them.
- `errorAndLog` (8.1 and later) rejects invalid writes and logs them. It blocks downgrade until the action changes.

Locks and privileges:
- collMod takes a collection lock for the whole operation. The page does not say which lock mode.
- It needs the `collMod` action, which `dbAdmin` provides.

Pre-flight check:
- To count documents that would fail a validator, run `find({$nor: [<validator>]})` ([validation query][val-query]). That is a full collection scan unless the query is bounded (inference). dbscope should cap it with `limit` and `maxTimeMS`, or run it on a `$sample`.

Converting an index to unique:
- Since 6.0, collMod can convert an index to unique.
- `prepareUnique` makes the index reject new duplicates.
- `unique: true` scans the index and fails with `CannotConvertIndexToUnique` if duplicates exist.
- `dryRun: true` reports violations without converting ([collMod][collMod]).

### 5.3 Dropping indexes

Source: [dropIndexes][dropIndexes].
- `dropIndexes` holds an exclusive collection lock for its whole run.
- It kills queries that use the dropped index, including queries that are only considering it during planning.
- The `_id` index cannot be dropped, and neither can the last index compatible with the shard key.
- Dropping an index that is still building stops the build.
- Before 8.3, dropping a missing index returns `IndexNotFound`. From 8.3 it succeeds. So on 7.0 and 8.0 it errors.
- Safer path: hide the index first. A hidden index stays fully maintained, so unhiding restores it immediately.

### 5.4 `renameCollection`

Source: [renameCollection][rename].
- **Same database:** only the namespace changes. The operation is quick, but it holds exclusive locks on the source and target for its whole run.
- **Different database:** all documents and indexes are copied, so time grows with collection size. Before 9.0 (including 7.0 and 8.0), only an S lock was held on the source during the copy. 9.0 holds exclusive locks on both collections.
- **Sharded clusters:** the source and target are locked exclusively on every shard.
- Without `dropTarget: true`, the rename fails if the target exists. With it, the target is dropped.
- The rename fails with `BackgroundOperationInProgressForNamespace` if an index build is running on the source.
- `mongodump --oplog` fails if a rename happens during the dump.
- Applications that use the old name will break (inference).

---

## 6. Go driver (`go.mongodb.org/mongo-driver/v2`)

### 6.1 Version and support

| Item | Value | Source |
|---|---|---|
| Latest release | v2.9.2. It is at the top of the GitHub releases page fetched 2026-10-28. The newest entry on pkg.go.dev is dated Oct 6, 2026. The tab did not show version labels, so whether that entry is v2.9.2 is **unverified**. | [releases][gh-releases], [pkg versions][pkg-versions], [Go release notes][go-relnotes] |
| v2.9.2 fixes | CVE-2026-81521 (BulkWrite namespace) and CVE-2026-107325 (a `RawArray.Validate` panic). | [releases][gh-releases] |
| Go version | Go 1.25 or higher. The driver supports the last two Go minor versions. The master go.mod says `go 1.25.0`. dbscope uses `go 1.26.0`, which is compatible. | [pkg v2][pkg-v2], [go.mod][gomod] |
| Server version | MongoDB 4.4 and higher. v2.6 deprecated 4.2 support. | [pkg v2][pkg-v2], [Go release notes][go-relnotes] |
| Compatibility table | The source table on docs-golang master lists only up to driver 2.2. It marks 2.1 and 2.2 compatible with 8.0 and 7.0. It says 1.17 and 2.0 are only partly compatible with 8.0 (no client bulk write). A row for 2.9 or for server 9.0 is **unverified**. The rendered page did not load in my fetcher. | [compat source][go-compat-src] |

### 6.2 CGO

- No CGO is needed for normal use. I found no cgo requirement for the core driver (unverified as a blanket statement). The driver's direct dependencies in go.mod are pure-Go modules, such as klauspost/compress, xdg-go/scram and x/crypto ([go.mod][gomod]).
- **In-use encryption** (CSFLE and Queryable Encryption) needs the `cse` build tag and the libmongocrypt C library ([pkg mongo][pkg-mongo]).
- **Kerberos (GSSAPI)** needs the `gssapi` build tag and cgo ([Kerberos][kerberos]). The source links `gssapi_krb5` on Linux and the GSS framework on macOS ([gss.go][gss-src]).
- dbscope should build without either tag. GSSAPI is then unavailable, which only affects MongoDB Enterprise users with Kerberos.

### 6.3 Binary size

- MongoDB does not document a binary size impact. I found no primary source.
- A Jira ticket, GODRIVER-2887, says use of `reflect.Value.MethodByName` stops dead-code elimination and makes binaries larger than needed ([GODRIVER-2887][godriver-2887]). I saw this only in a search snippet, so its resolution status is **unverified**.
- dbscope should measure size with `go build` before and after adding the driver.

### 6.4 Connection-string options

Source: [connection string options][connstr].

| Option | Behaviour |
|---|---|
| `tls` / `ssl` | Defaults to true for `mongodb+srv://` and false for standard strings. |
| `tlsCAFile`, `tlsCertificateKeyFile` | CA bundle and client certificate. |
| `tlsAllowInvalidCertificates`, `tlsAllowInvalidHostnames`, `tlsInsecure` | Each disables certificate or hostname checks. dbscope should warn when any is set (inference). |
| `authSource` | Defaults to the database in the URI path, or to `admin`. GSSAPI, PLAIN and MONGODB-AWS require `$external`. It is ignored without a username. |
| `authMechanism` | Defaults to SCRAM-SHA-256, with fallback to SCRAM-SHA-1. |
| `directConnection` | `true` talks to that one host only and skips discovery. `false` is the default and discovers the replica set. |
| `readPreference` | Default `primary`. Also `primaryPreferred`, `secondary`, `secondaryPreferred`, `nearest`, with `maxStalenessSeconds` (at least 90) and `readPreferenceTags`. |
| `readConcernLevel` | `local`, `majority`, `linearizable` or `available`. |
| `appName` | Appears in mongod and mongos logs, in `currentOp.appName` and in `system.profile.appName`. dbscope should always set it, for example `appName=dbscope` (inference). |
| `timeoutMS` | Client-side operation timeout with no default. It applies to server selection, connection checkout and server execution. |
| `serverSelectionTimeoutMS` | Default 30,000 ms. |
| `connectTimeoutMS` | Default 10,000 ms. |
| `maxPoolSize` | Default 100. |
| `retryReads`, `retryWrites` | Drivers default both to true. |

- Since v2.9 the Go driver adds `tlsDisableCertificateRevocationCheck` ([Go release notes][go-relnotes]).
- In the Go driver, client-side operation timeouts (CSOT) are marked experimental ([CSOT Go][csot-go]).

### 6.5 Timeouts in v2 (relevant to `maxTimeMS`)

- v2 removed `FindOptions.SetMaxTime`, `AggregateOptions.SetMaxTime`, `CountOptions.SetMaxTime`, `ListIndexesOptions.SetMaxTime` and others. Use `ClientOptions.Timeout` or a context deadline instead ([migration 2.0][migration-2.0]).
- Under the CSOT spec, when `timeoutMS` is set, drivers append `maxTimeMS` equal to the remaining timeout minus `minRoundTripTime`. They do not add `maxTimeMS` to `getMore` on non-tailable cursors. The timeout covers the cursor's whole lifetime by default ([CSOT spec][csot-spec]).
- Whether a context deadline alone, with no client `Timeout`, produces a server-side `maxTimeMS` in the Go driver is **unverified** from docs. The Go page says the context timeout is used "for the operation" ([CSOT Go][csot-go]).
- dbscope can make it certain. Either send `find` or `aggregate` through `Database.RunCommand` / `RunCommandCursor` with an explicit `maxTimeMS` field, or check the sent command with an `event.CommandMonitor` in tests (inference).

### 6.6 Getting BSON type information while sampling

- `mongo.Cursor.Current` is a `bson.Raw` holding the raw document bytes ([pkg mongo][pkg-mongo]).
- `bson.Raw.Elements()` returns `[]bson.RawElement`. Each element has `Key()` and `Value()`, and the value is a `bson.RawValue{Type bson.Type, Value []byte}` ([pkg bson][pkg-bson]).
- `RawValue.Document()` and `RawValue.Array()` (and their `OK` variants) let the walker recurse into embedded documents and arrays ([pkg bson][pkg-bson]).
- This gives each field path's BSON type without decoding into Go values.
- `x/bsonx/bsoncore` has the same model: `Document.Elements()`, and `Value{Type, Data}` with type constants 0x01 to 0x13, plus MaxKey 0x7F and MinKey 0xFF ([pkg bsoncore][pkg-bsoncore]). The package says it is for internal use, has no stable API and has no backward-compatibility guarantee ([pkg bsoncore][pkg-bsoncore]). **Use the stable `bson.Raw*` types.**
- To detect references, `bson.ObjectID` exposes `Hex()` and `Timestamp()` ([pkg bson][pkg-bson]).
- To check whether candidate ObjectIDs exist in another collection, run `find({_id: {$in: [...]}})` with a projection of `{_id: 1}` and a limit. On normal collections this uses the default unique `_id` index ([indexes][indexes]). Time-series collections lack that index ([ts-limits][ts-limits]) (inference for the query plan).

---

## Facts dbscope must encode

Sampling
- [ ] `$sample` uses the pseudo-random cursor only if it is the first stage, N < 5% of the count, and the count > 100. Otherwise it scans the whole input and sorts randomly ([sample v8.0][sample-8.0]).
- [ ] The 5% threshold is fixed. Above it, `$sample` does a top-k sort that may spill past 100 MB ([sample v8.0][sample-8.0]).
- [ ] `$sample` on a view always scans the collection ([sample v8.0][sample-8.0]).
- [ ] The pseudo-random cursor can be skewed (documented in 8.0). Label results as an approximate sample ([sample v8.0][sample-8.0]).
- [ ] Use `$collStats` `count` or `storageStats.count` for the 5% decision. It is metadata-based and may be inaccurate on sharded clusters ([$collStats][collStatsStage]).
- [ ] Always send `maxTimeMS`. Its absence or `0` means no timeout ([aggregate][aggregate]).
- [ ] Send `allowDiskUse: false`, because `allowDiskUseByDefault` is true ([parameters][params]).
- [ ] Default to `secondaryPreferred` and offer `maxStalenessSeconds` of at least 90 ([read preference][readpref], [staleness][staleness]).
- [ ] Time-series `$sample` behaviour is unverified. Explain before sampling.

Catalog
- [ ] Map `listCollections.type` values `collection`, `view` and `timeseries`. Read `options.validator`, `validationLevel` (`off`/`strict`/`moderate`, plus `constraint` in 9.0), `validationAction` (`error`/`warn`, plus `errorAndLog` in 8.1), `capped`, `clusteredIndex`, `timeseries`, `viewOn` and `pipeline` ([listCollections][listCollections], [create][create], [validation action][val-action]).
- [ ] Fall back to `nameOnly + authorizedCollections` when `listCollections` is denied ([listCollections][listCollections]).
- [ ] Recognise index kinds from `key` values (1, -1, "text", "2dsphere", "2d", "hashed", `$**`) and options (`unique`, `sparse`, `partialFilterExpression`, `expireAfterSeconds`, `hidden` only when true, `wildcardProjection`, `collation`) ([createIndexes][createIndexes], [listIndexes][listIndexes]).
- [ ] Use `$collStats` rather than the `collStats` command, which is deprecated since 6.2 ([collStats command][collStatsCmd]).
- [ ] Know what `read` grants: `changeStream`, `collStats`, `dbHash`, `dbStats`, `find`, `killCursors`, `listCollections`, `listIndexes`, `listSearchIndexes` ([roles source][roles-src]).

Query safety
- [ ] Reject `$out` and `$merge` at any nesting depth, including in `$lookup`, `$facet` and `$unionWith` sub-pipelines. The nesting rule is inferred from the view rule ([out][out], [merge][merge], [create][create]).
- [ ] Reject `$where`, `$function`, `$accumulator`, `mapReduce`, and the `bypassDocumentValidation` and `writeConcern` options ([server-side JS][ssjs], [aggregate][aggregate]).
- [ ] Do not assume JavaScript is disabled. In 8.0 it is deprecated but on by default ([function v8.0][function-8.0], [config source][config-src]).
- [ ] Recommend a `read`-only user as the real enforcement. readConcern is not a permission control ([roles source][roles-src], [read concern][readconcern]).
- [ ] A secondary read preference does not stop `$merge` or `$out` from writing ([read preference][readpref]).
- [ ] Always send `verbosity: "queryPlanner"` to the `explain` command. Its default is `allPlansExecution` ([explain][explain]).
- [ ] Parse `winningPlan` and `rejectedPlans` trees, plus `shards[]` for sharded collections. Flag `COLLSCAN`. Do not depend on a fixed format ([explain results][explain-results], [explain][explain]).
- [ ] Set `comment` and `appName=dbscope` on every operation ([aggregate][aggregate], [connstr][connstr]).

Index advice
- [ ] ESR: equality first, then sort, then range. `$ne`, `$nin` and `$regex` count as range. `$in` counts as equality unless sorted with 201 or more elements ([ESR][esr]).
- [ ] Compound indexes only help queries on their prefixes. A prefix index can be removed when neither index is sparse or unique ([compound][compound]).
- [ ] `$indexStats` is per node, counts only user operations, resets on restart, rebuild or collMod change, and needs `clusterMonitor` ([indexStats][indexStats], [collMod][collMod]).
- [ ] The profiler is off by default and per mongod (not on mongos). Reading `system.profile` needs `dbAdmin` or `clusterMonitor`. Use `planSummary`, `keysExamined`, `docsExamined`, `hasSortStage` and `queryShapeHash` (8.0+) ([profiler][profiler], [profiler output][profiler-out], [roles source][roles-src]).
- [ ] `$currentOp` runs on admin as the first stage. `allUsers: true` needs `inprog` ([currentOp][currentOp]).
- [ ] `$collStats.queryExecStats.collectionScans` is a collection-scan signal available to `read` users ([$collStats][collStatsStage]).

Risky operations
- [ ] Index build: X lock at start (blocks all), IX while yielding, S during the drain (blocks writes), X at commit. All data-bearing members build at once. The default `votingMembers` quorum can hang if a member is down ([index builds][index-build]).
- [ ] Unique index builds fail only at the end, and may leave inconsistent indexes across shards ([index builds][index-build]).
- [ ] Builds use 200 MB of memory per `createIndexes`, allow 3 concurrent builds, and (7.1+) need 500 MB of free disk. On 7.0, a build error can crash a secondary ([index builds][index-build], [parameters][params]).
- [ ] A validator change leaves existing documents unchecked. `strict` makes updates to invalid documents fail. `moderate` exempts them. `constraint` (9.0) scans everything. Count would-be failures with `$nor` plus a limit and `maxTimeMS` ([collMod][collMod], [validation level][val-level], [validation query][val-query]).
- [ ] `dropIndexes` holds an exclusive lock and kills queries using the index. Suggest hiding the index first. On 7.0 and 8.0 a missing index is an error ([dropIndexes][dropIndexes]).
- [ ] Same-database rename is fast but takes exclusive locks. Cross-database rename copies all data. It fails during an index build ([rename][rename]).

Go driver
- [ ] Use `go.mongodb.org/mongo-driver/v2` (v2.9.2 at research time). It needs Go 1.25+ and MongoDB 4.4+ ([releases][gh-releases], [pkg v2][pkg-v2]).
- [ ] Build without the `cse` and `gssapi` tags so CGO stays off ([pkg mongo][pkg-mongo], [Kerberos][kerberos]).
- [ ] Time out operations with a context or `Timeout`. `SetMaxTime` is gone. Check that `maxTimeMS` is sent, or set it explicitly via RunCommand ([migration 2.0][migration-2.0], [CSOT spec][csot-spec]).
- [ ] Walk `bson.Raw` → `RawElement` → `RawValue.Type` for type inference. Avoid the unstable `x/bsonx/bsoncore` ([pkg bson][pkg-bson], [pkg bsoncore][pkg-bsoncore]).

---

## Sources

[relnotes]: https://www.mongodb.com/docs/manual/release-notes/
[sample-8.0]: https://www.mongodb.com/docs/v8.0/reference/operator/aggregation/sample/
[sample-7.0]: https://www.mongodb.com/docs/v7.0/reference/operator/aggregation/sample/
[ts-limits]: https://www.mongodb.com/docs/manual/core/timeseries/timeseries-limitations/
[aggregate]: https://www.mongodb.com/docs/manual/reference/command/aggregate/
[find]: https://www.mongodb.com/docs/manual/reference/command/find/
[params]: https://www.mongodb.com/docs/manual/reference/parameters/
[readpref]: https://www.mongodb.com/docs/manual/core/read-preference/
[staleness]: https://www.mongodb.com/docs/manual/core/read-preference-staleness/
[listCollections]: https://www.mongodb.com/docs/manual/reference/command/listCollections/
[create]: https://www.mongodb.com/docs/manual/reference/command/create/
[listIndexes]: https://www.mongodb.com/docs/manual/reference/command/listIndexes/
[createIndexes]: https://www.mongodb.com/docs/manual/reference/command/createIndexes/
[collStatsStage]: https://www.mongodb.com/docs/manual/reference/operator/aggregation/collStats/
[collStatsCmd]: https://www.mongodb.com/docs/manual/reference/command/collStats/
[roles-src]: https://raw.githubusercontent.com/mongodb/docs/master/source/reference/built-in-roles.txt
[actions-src]: https://raw.githubusercontent.com/mongodb/docs/master/source/reference/privilege-actions.txt
[out]: https://www.mongodb.com/docs/manual/reference/operator/aggregation/out/
[merge]: https://www.mongodb.com/docs/manual/reference/operator/aggregation/merge/
[ssjs]: https://www.mongodb.com/docs/manual/core/server-side-javascript/
[function-8.0]: https://www.mongodb.com/docs/v8.0/reference/operator/aggregation/function/
[function]: https://www.mongodb.com/docs/manual/reference/operator/aggregation/function/
[config-src]: https://raw.githubusercontent.com/mongodb/docs/master/source/reference/configuration-options.txt
[explain]: https://www.mongodb.com/docs/manual/reference/command/explain/
[explain-method]: https://www.mongodb.com/docs/manual/reference/method/db.collection.explain/
[cursor-explain-7.1]: https://www.mongodb.com/docs/v7.1/reference/method/cursor.explain/
[explain-results]: https://www.mongodb.com/docs/manual/reference/explain-results/
[readconcern]: https://www.mongodb.com/docs/manual/reference/read-concern/
[indexes]: https://www.mongodb.com/docs/manual/indexes/
[compound]: https://www.mongodb.com/docs/manual/core/indexes/index-types/index-compound/
[qopt]: https://www.mongodb.com/docs/manual/core/query-optimization/
[esr]: https://www.mongodb.com/docs/manual/tutorial/equality-sort-range-guideline/
[indexStats]: https://www.mongodb.com/docs/manual/reference/operator/aggregation/indexStats/
[profiler]: https://www.mongodb.com/docs/manual/tutorial/manage-the-database-profiler/
[profiler-out]: https://www.mongodb.com/docs/manual/reference/database-profiler/
[currentOp]: https://www.mongodb.com/docs/manual/reference/operator/aggregation/currentOp/
[queryStats]: https://www.mongodb.com/docs/manual/reference/operator/aggregation/queryStats/
[index-build]: https://www.mongodb.com/docs/manual/core/index-creation/
[collMod]: https://www.mongodb.com/docs/manual/reference/command/collMod/
[val-level]: https://www.mongodb.com/docs/manual/core/schema-validation/specify-validation-level/
[val-action]: https://www.mongodb.com/docs/manual/core/schema-validation/handle-invalid-documents/
[val-query]: https://www.mongodb.com/docs/manual/core/schema-validation/use-json-schema-query-conditions/
[dropIndexes]: https://www.mongodb.com/docs/manual/reference/command/dropIndexes/
[rename]: https://www.mongodb.com/docs/manual/reference/command/renameCollection/
[pkg-v2]: https://pkg.go.dev/go.mongodb.org/mongo-driver/v2
[pkg-versions]: https://pkg.go.dev/go.mongodb.org/mongo-driver/v2?tab=versions
[gh-releases]: https://github.com/mongodb/mongo-go-driver/releases
[go-relnotes]: https://www.mongodb.com/docs/drivers/go/current/reference/release-notes/
[gomod]: https://raw.githubusercontent.com/mongodb/mongo-go-driver/master/go.mod
[go-compat-src]: https://raw.githubusercontent.com/mongodb/docs-golang/master/source/includes/mongodb-compatibility-table-go.rst
[pkg-mongo]: https://pkg.go.dev/go.mongodb.org/mongo-driver/v2/mongo
[kerberos]: https://www.mongodb.com/docs/drivers/go/current/security/authentication/kerberos/
[gss-src]: https://raw.githubusercontent.com/mongodb/mongo-go-driver/master/x/mongo/driver/auth/internal/gssapi/gss.go
[godriver-2887]: https://jira.mongodb.com/browse/GODRIVER-2887
[connstr]: https://www.mongodb.com/docs/manual/reference/connection-string-options/
[migration-2.0]: https://raw.githubusercontent.com/mongodb/mongo-go-driver/master/docs/migration-2.0.md
[csot-go]: https://www.mongodb.com/docs/drivers/go/current/connect/connection-options/csot/
[csot-spec]: https://raw.githubusercontent.com/mongodb/specifications/master/source/client-side-operations-timeout/client-side-operations-timeout.md
[pkg-bson]: https://pkg.go.dev/go.mongodb.org/mongo-driver/v2/bson
[pkg-bsoncore]: https://pkg.go.dev/go.mongodb.org/mongo-driver/v2/x/bsonx/bsoncore

Content from these sources was paraphrased for licensing compliance. Short code identifiers and option names are reproduced as-is.

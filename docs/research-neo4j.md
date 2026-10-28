# Research: Neo4j support

Collected 2026-10-28 by a research worker from primary sources (cited); nothing was run against a server during research, and claims it could not verify are marked "unverified". Lines marked **design** are proposals. The design dbscope adopted is in docs/ENGINES.md.

**Versions on 2026-10-28**
- **Server, current:** Docker tag `latest` has the same digest as `2026.10.1` Community ([hub latest][hub-latest], [hub 2026.10][hub-2026]).
- **Server, 5.x line:** the newest 5.x tag is `5.26.32` ([hub 5.26][hub-526]).
- **Versioning:** calendar versions (YYYY.MM.Patch) started with 2025.01.0, and some releases are LTS ([ops intro][ops-intro]). Which release is the current LTS is unverified.
- **Go driver:** `github.com/neo4j/neo4j-go-driver/v6` is at v6.3.0, published 2026-09-21 ([proxy v6][go-proxy6]). v5 is still patched: v5.28.6, published 2026-10-20 ([proxy v5][go-proxy5]).

**What matters most for the design**
- **EXPLAIN gives the full plan tree without running the query.** `ResultSummary.Plan()` returns, for each node, the operator name, an argument map, identifiers and children ([go summary][go-summary], [go manual summary][gm-summary]).
- **Read access mode is routing, not access control.** The Go manual says a write in read mode errors, but there is no security guarantee that it is refused ([go manual tx][gm-tx]).
- **Community Edition has no roles.** Every user has implied admin privileges ([manage users][ops-users]). On Community, the only server-side read-only control appears to be the `server.databases.read_only` setting ([config][ops-config]).
- **Performance notifications have stable codes, but they don't cover every risk.** Cartesian product is `03N90`. `03N91` (unbounded pattern) is documented only for shortest path. `03N94` (Eager) is documented only for `LOAD CSV` ([notifications][sc-notif]). dbscope still has to walk the plan itself.
- **The count store only covers some count shapes.** It answers counts for all nodes, one label, all relationships, one type, and one type with a label on one end. It cannot answer counts with labels on both ends ([operators][cy-ops]).
- **The driver has no third-party dependencies.** Its v6.3.0 `go.mod` has no `require` lines ([go.mod][go-mod]). That fits CGO off. I did not check the source for `import "C"`.

## 1. Catalog

**Token procedures** ([procedures][ops-procs])

| Procedure | Returns | Mode | Notes |
|---|---|---|---|
| `db.labels()` | `label STRING` | READ | Filtered by the user's access rights. |
| `db.relationshipTypes()` | `relationshipType STRING` | READ | Filtered by access rights. Not supported by the parallel runtime. |
| `db.propertyKeys()` | `propertyKey STRING` | READ | Not supported by the parallel runtime. |

- **Tokens can outlive data.** `db.createLabel/createProperty/createRelationshipType` exist with mode WRITE. A token can therefore exist with no data using it ([procedures][ops-procs]).
- **Design:** show labels with a zero count-store count as "token only".

**Schema inference** ([procedures][ops-procs])

| Procedure | Signature | Notes |
|---|---|---|
| `db.schema.nodeTypeProperties()` | `(nodeType, nodeLabels, propertyName, propertyTypes, mandatory)`, READ | Takes no arguments, so it has no sampling option. |
| `db.schema.relTypeProperties()` | `(relType, propertyName, propertyTypes, mandatory)`, READ | Takes no arguments, so it has no sampling option. |
| `db.schema.visualization(estimate = true)` | `(nodes, relationships)`, READ | Uses count-store statistics, so it can list relationship combinations that don't exist. `estimate => false` scans the data and is slower. `estimate` is new in 2026.10 and Cypher 25 only. In Cypher 5 the procedure takes no arguments. |

- **`propertyTypes` depends on the Cypher version.** Cypher 5 returns Java type names. Cypher 25 returns Cypher type names ([procedures][ops-procs]).
- **Cost on large graphs is unverified.** The reference page doesn't describe it. A community forum answer, a secondary source, says `nodeTypeProperties` computes eagerly and is slow on big graphs ([community][comm-slow]).
- **No deprecations for these procedures.** The current deprecation list has no `db.labels/relationshipTypes/propertyKeys/schema.*` entry. It does deprecate the full-text and vector `query*` procedures (2026.10 and 2026.04, Cypher 25) in favor of `SEARCH` ([deprecations][ops-deprec]).
- **Design:** prefix catalog queries with an explicit `CYPHER 5` or `CYPHER 25`. Run the `*TypeProperties` procedures under a timeout.

**`SHOW INDEXES YIELD *` columns** ([list indexes][cy-list-idx])

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER | |
| `name` | STRING | |
| `state` | STRING | `POPULATING` until `ONLINE` ([create indexes][cy-create-idx]). The name of the failed state (`FAILED`) is unverified. |
| `populationPercent` | FLOAT | Population progress. |
| `type` | STRING | `FULLTEXT`, `LOOKUP`, `POINT`, `RANGE`, `TEXT`, `VECTOR`. |
| `entityType` | STRING | `NODE` or `RELATIONSHIP`. |
| `labelsOrTypes` | LIST<STRING> | |
| `properties` | LIST<STRING> | For vector indexes, the vector property comes first. |
| `indexProvider` | STRING | |
| `owningConstraint` | STRING | The constraint this index backs, or null. |
| `lastRead` | ZONED DATETIME | |
| `readCount` | INTEGER | |
| `trackedSince` | ZONED DATETIME | Not in default output. |
| `options` | MAP | Not in default output. |
| `failureMessage` | STRING | Not in default output. |
| `createStatement` | STRING | Not in default output. |

- Statistics are local to each cluster member.
- `SHOW INDEXES` needs the `SHOW INDEX` privilege. The built-in `reader` role has it ([roles][ops-roles]).
- Combining `SHOW` with other clauses is Cypher 25 only, from 2026.05.

**`SHOW CONSTRAINTS YIELD *` columns** ([list constraints][cy-list-con])

| Column | Notes |
|---|---|
| `id`, `name` | |
| `type` | `NODE_PROPERTY_UNIQUENESS`, `RELATIONSHIP_PROPERTY_UNIQUENESS`, `NODE_PROPERTY_EXISTENCE`, `RELATIONSHIP_PROPERTY_EXISTENCE`, `NODE_PROPERTY_TYPE`, `RELATIONSHIP_PROPERTY_TYPE`, `NODE_LABEL_EXISTENCE`, `RELATIONSHIP_SOURCE_LABEL`, `RELATIONSHIP_TARGET_LABEL`, `NODE_KEY`, `RELATIONSHIP_KEY`. |
| `entityType`, `labelsOrTypes`, `properties` | |
| `enforcedLabel` | Cypher 25 only, from 2026.02. |
| `ownedIndex` | The backing index, or null. |
| `propertyType` | For example `STRING \| LIST<STRING NOT NULL>` or `VECTOR<FLOAT32 NOT NULL>(1536)`. |
| `classification` | `dependent`, `undesignated` or `independent`. Cypher 25 only, from 2026.02. |
| `options`, `createStatement` | |

- **Edition split.** Uniqueness constraints are in both editions. Existence, type and key constraints are Enterprise only ([constraints][cy-constraints]).
- **Graph types.** Constraints are folded into the database's graph type, and the docs recommend graph types for new schemas.

**Count-store counts** ([operators][cy-ops])
- **Nodes.** `MATCH (p:Person) RETURN count(p)` plans as `NodeCountFromCountStore`, with 1 DB hit in the doc example. It works for all nodes or one label, not for multiple labels.
- **Relationships.** `MATCH (:Person)-[r:WORKS_IN]->() RETURN count(r)` plans as `RelationshipCountFromCountStore`. It works for all relationships, one type, or one type with a label on one end, not both ends.
- **"O(1)" is unverified.** The docs say "much faster" and show 1 DB hit, but give no complexity bound.
- **Design:** get per-endpoint counts from the count store. Use `db.schema.visualization()` as a list of candidate triples, then check each with a bounded `EXISTS` probe. Label exact triple counts as "estimate". EXPLAIN every catalog count first and skip it if the plan doesn't use a `*CountFromCountStore` operator.

**APOC**
- **Optional plugin, off by default.** The Docker image installs APOC Core only when `NEO4J_PLUGINS` asks for it, and then sets `dbms.security.procedures.unrestricted=apoc.*` ([docker plugins][dk-plugins]).
- **Restricted on-premise.** APOC procedures must be unrestricted before they can run ([apoc.meta.schema][apoc-schema]).
- **`apoc.meta.*` offers more.** `apoc.meta.stats()` returns the database statistics. `apoc.meta.schema` samples, by default 1000 nodes; `-1` turns sampling off ([apoc.meta][apoc-meta]).
- **Aura availability is unverified.**
- **Design:** don't depend on APOC. Detect it with `SHOW PROCEDURES` and use it as an optional fast path.

## 2. EXPLAIN plans

**Getting the plan from Go**
- **EXPLAIN plans the query without running it** and returns only estimates. PROFILE runs the query and can write ([execution plans][cy-plans]).
- **The v6 `Plan` interface** has `Operator()`, `Arguments() map[string]any`, `Identifiers()` and `Children()` ([go summary][go-summary]).
- **`QueryProfile()`** replaces the deprecated `Profile()`. The Go manual says it exists from 6.2 ([go manual summary][gm-summary]).
- **Argument keys** are documented only in the Query API example ([Query API][qapi-profile]):
  - Root operator: `planner`, `planner-impl`, `planner-version`, `runtime`, `runtime-impl`, `runtime-version`, `batch-size`, `string-representation`, `GlobalMemory`.
  - Every operator: `Details`, `Id`, `EstimatedRows` (a float, for example `0.5`) and `PipelineInfo`.
  - The Go manual prints `Arguments()["string-representation"]` from an EXPLAIN plan. That suggests Bolt carries the same map. The exact Bolt key list is unverified.
- **Operator names carry a suffix.** The Query API shows names like `NodeByLabelScan@neo4j`. Whether Bolt's `Operator()` keeps `@neo4j` is unverified. **Design:** strip everything from `@` on.
- **`QueryType()`** returns `r`, `rw`, `w` or `s` ([go summary][go-summary]). Whether the server fills it for EXPLAIN is unverified.

**Risk operators** ([operators][cy-ops]; the verdicts are **design**)

| Operator | Meaning | Verdict |
|---|---|---|
| `AllNodesScan`, `PartitionedAllNodesScan` | Reads every node. The docs say it likely causes performance problems on a non-trivial database. | High |
| `DirectedAllRelationshipsScan`, `UndirectedAllRelationshipsScan` | Reads every relationship with its end nodes. | High |
| `NodeByLabelScan` | Reads all nodes with one label from the label index. | Scale by the label's count |
| `NodeIndexSeek`, `NodeIndexSeekByRange` | Index seek. `Details` shows the index, for example `RANGE INDEX p:Person(name) WHERE ...`. | Good |
| `NodeIndexScan`, `NodeIndexEndsWithScan` | Reads all entries of an index. | Medium |
| `CartesianProduct` | Every left row times every right row. The docs say it generally performs badly. | High when both sides are large |
| `Eager` | Forces all earlier operators to finish first, which can use a lot of memory. | Flag |
| `EagerAggregation` | Pulls all input to build groups. Lower memory pressure than `Eager`. | Info |
| `VarLengthExpand(All/Into/Pruning/Pruning,BFS,All)`, `Repeat(Trail)` | `Details` shows the bounds, for example `*..2`. `Pruning` is only planned when there is an upper bound. | High with no upper bound |
| `*CountFromCountStore` | Answered from the count store. | Cheap |
| `LoadCSV` | Loads a CSV source. | Block in the read path |

**Runtimes**
- **Slotted** is the Community default ([runtimes][cy-rt]).
- **Pipelined** is the Enterprise and Aura default, and is Enterprise only ([runtimes][cy-rt]).
- **Parallel** is Enterprise only and opt-in with `CYPHER runtime = parallel`. It rejects updating queries with GQLSTATUS `22N46` ([parallel][cy-rt-par]).
- Many built-in procedures don't support the parallel runtime ([procedures][ops-procs]).

**GQLSTATUS notifications.** All the 03N9x codes are PERFORMANCE notifications with severity INFORMATION ([notifications][sc-notif]):

| Code | Neo4j code | When |
|---|---|---|
| 03N90 | `Statement.CartesianProduct` | Disconnected patterns build a cartesian product. |
| 03N91 | `Statement.UnboundedVariableLengthPattern` | Shortest path with no upper bound. |
| 03N92 | `Statement.ExhaustiveShortestPath` | Shortest path may fall back to exhaustive search. |
| 03N93 | `Statement.NoApplicableIndex` | `LOAD CSV` with `MATCH`/`MERGE` on a label with no index. |
| 03N94 | `Statement.EagerOperator` | The plan has `Eager` (documented for `LOAD CSV`). |
| 03N95 | `Statement.DynamicProperty` | A dynamic property key prevents index use. |
| 03N96 | `Statement.CodeGenerationFailed` | Code generation failed and the runtime fell back. |
| 03N97 | `Statement.VirtualGraphPostProcessing` | A plan against a virtual graph materializes intermediate results. |
| 01N50 | `UnknownLabelWarning` | A label in the query is not in the database. |
| 00001 | (general) | Omitted result. The docs give EXPLAIN as the example ([notification index][sc-notif-idx]). |

- **Codes are stable, descriptions are not.** Match on codes, never on text ([notifications][sc-notif]).
- **Driver API.** Use `GqlStatusObjects()`; `Notifications()` is deprecated ([go summary][go-summary]).
- **Filtering.** `NotificationsMinSeverity` and `NotificationsDisabledClassifications` default to the server's settings ([go config][go-config]).
- **Notifications on EXPLAIN are unverified.** Whether every PERFORMANCE notification is sent for EXPLAIN is not documented.

## 3. Read-only safety

**Access mode and roles**
- **Read routing.** `AccessModeRead` and `ExecuteRead()` route to any cluster member. Writes go to primaries ([go manual tx][gm-tx]).
- **No security guarantee.** The manual warns there is no guarantee that a write in read mode is refused ([go manual tx][gm-tx], [go manual query][gm-query]).
- **Error code, unverified mapping.** `Neo.ClientError.Statement.AccessMode` is the "access mode violation" error ([errors][sc-errors]). That it is the error a single Community instance raises for a write in a read transaction appears likely but is unverified. Cluster secondary behavior is also unverified.
- **Server-side read-only databases.** `server.databases.read_only` and `server.databases.default_to_read_only` are dynamic settings with no Enterprise label ([config][ops-config]). They raise `ForbiddenOnReadOnlyDatabase` or `WriteOnReadOnlyAccessDatabase` ([errors][sc-errors]).
- **Community.** There are no roles, and all users are implicitly admins ([manage users][ops-users]). RBAC is Enterprise only ([auth][ops-auth]).
- **The `reader` role** grants `ACCESS ON DATABASE *`, `MATCH {*}` on all nodes and relationships, `SHOW CONSTRAINT` and `SHOW INDEX` ([roles][ops-roles]).
- **`PUBLIC` is broader than it looks.** Every Enterprise user has it, and it grants `EXECUTE PROCEDURE *`, `EXECUTE FUNCTION *` and `LOAD ON ALL DATA` ([roles][ops-roles]).
- **Detecting the edition.** `SHOW CURRENT USER` returns null `roles` on Community ([manage users][ops-users]).

**Statements to block in the read path**
- **Write clauses:** `CREATE`, `DELETE`, `DETACH DELETE`, `SET`, `REMOVE`, `FOREACH`. `MERGE` and `CALL ... YIELD` both read and write ([clauses][cy-clauses]).
- **`CALL { } IN TRANSACTIONS`** is only allowed in auto-commit transactions. Its plan contains `TransactionForeach` ([CIT][cy-citx]).
- **`LOAD CSV`.**
- **Schema DDL** (query type `s`).
- **Admin commands** that run against the `system` database ([auth][ops-auth]).
- **`USE`** ([go manual tx][gm-tx]).
- **Composable `SHOW`.** From 2026.05 in Cypher 25, `SHOW` commands can be combined with updating clauses ([show procedures][ops-showprocs]).

**Procedure mode**
- `SHOW PROCEDURES` returns `name`, `description`, `mode` and `worksOnSystem` by default. `YIELD *` adds `signature`, `admin`, `rolesExecution`, `rolesBoostedExecution`, `isDeprecated`, `deprecatedBy` and `option`. `EXECUTABLE BY CURRENT USER` filters by privileges ([show procedures][ops-showprocs]).
- Known mode values are READ, WRITE and DBMS. `db.clearQueryCaches()` is DBMS and admin-only, so DBMS does not mean read-only ([procedures][ops-procs]). Whether other mode strings exist is unverified.
- **Design:** allow only `mode = READ`.

**Design: layered check for user queries**
1. Scan tokens for the statements above, skipping string literals and comments.
2. Run EXPLAIN and reject plans that contain write operators, `TransactionForeach` or `LoadCSV`.
3. Run with `ExecuteRead` and `WithTxTimeout`.
4. Afterwards, assert that `Counters().ContainsUpdates()` and `ContainsSystemUpdates()` are false ([go manual summary][gm-summary]).
5. On Community, warn that only steps 1 and 2 prevent writes.

**Timeouts, retries and limits**
- **Server timeout.** `db.transaction.timeout` defaults to `0s` and is dynamic ([config][ops-config]). Reading the docs, `0s` appears to mean no timeout.
- **Client timeout.** `WithTxTimeout`: the server ends transactions that run longer, minimum 1 ms ([go manual tx][gm-tx]).
- **Timeout errors.** `TransactionTimedOut` for the server setting, `TransactionTimedOutClientConfiguration` for the client timeout ([errors][sc-errors]).
- **Server cap is unverified.** Whether a client timeout can exceed the server setting is not documented.
- **Retries.** Managed transactions retry for up to `MaxTransactionRetryTime`, default 30 s ([go config][go-config]). `MemoryPoolOutOfMemoryError` is a transient error ([errors][sc-errors]), so a query that hits a memory limit could be rerun. **Design:** avoid managed retries for user queries.
- **No server row cap was found.** A bounded `FetchSize` bounds client memory ([go config][go-config]). Results stream lazily ([go manual tx][gm-tx]). **Design:** stop reading after N records and close the result.
- **Memory limits.** `db.memory.transaction.max` (default `0B`, meaning largest possible) and `db.memory.transaction.total.max` (default `0B`, unlimited) ([config][ops-config]). Errors: `TransactionOutOfMemoryError` and the transient `MemoryPoolOutOfMemoryError` ([errors][sc-errors]).

## 4. Schema-change risks

- **New indexes start POPULATING.** An index can't be used until `ONLINE`, and `populationPercent` shows progress ([create indexes][cy-create-idx]). `db.awaitIndex` and `db.awaitIndexes` (mode READ) wait for indexes to come online ([procedures][ops-procs]).
- **Write blocking during population is unverified.**
- **Index name clashes.** `CREATE INDEX` errors on a duplicate. `IF NOT EXISTS` makes it a no-op, but conflicting constraints can still raise an error ([create indexes][cy-create-idx]).
- **Constraint creation scans all data.** It is atomic and can take a while ([create constraints][cy-create-con]).
- **Constraint creation fails on conflicting data** ([create constraints][cy-create-con]):
  - Uniqueness: duplicate values.
  - Existence: missing properties.
  - Type: wrong-typed values.
  - Key: missing properties or duplicates.
- **Design:** before `CREATE CONSTRAINT`, run a bounded duplicate or missing-value probe under a timeout. Show the label's count first.
- **Dropping indexes.**
  - The plan's `Details` names the index a query uses. PROFILE has an "Indexes Used" column ([execution plans][cy-plans]).
  - `readCount` and `lastRead` show use since `trackedSince` ([list indexes][cy-list-idx]).
  - Dropping a constraint-owned index fails with `ForbiddenOnConstraintIndex` ([errors][sc-errors]).
- **Renames.** No rename command was found. Labels change per node with `SET` and `REMOVE` ([clauses][cy-clauses]). That relationship types can't be changed in place is unverified from a primary source; community answers say so.
- **Large unbatched writes.** `CALL { } IN TRANSACTIONS OF n ROWS` exists to avoid huge garbage collection or out-of-memory errors. The default batch is 1000 rows ([CIT][cy-citx]). `Eager` in write plans adds memory risk ([operators][cy-ops]).
- **Schema and data can't share a transaction.** Mixing them raises `ForbiddenDueToTransactionType` ([errors][sc-errors]).

## 5. Go driver and test servers

**Driver**
- **Install.** `go get github.com/neo4j/neo4j-go-driver/v6` ([README][go-readme]). It needs Go 1.24 and has no `require` lines ([go.mod][go-mod]).
- **Compatibility.** Minor upgrades within a major version don't break the API ([README][go-readme]).
- **v6 vs v5.** In v6 every API takes a context. The old `*WithContext` names remain as deprecated aliases ([go pkg][go-pkg]).
- **URI schemes** ([URI partial][gm-uri]):
  - `neo4j`, `neo4j+s`, `neo4j+ssc` get a routing table, even from one server.
  - `bolt`, `bolt+s`, `bolt+ssc` connect to one machine directly.
  - `+s` accepts only CA-signed certificates. `+ssc` also accepts self-signed ones.
  - URIs with a path are not supported.
- **TLS.** `TlsConfig` applies only to `+s` and `+ssc`. The minimum version defaults to TLS 1.2. `InsecureSkipVerify` is always derived from the scheme ([go config][go-config]).
- **Auth.** `BasicAuth`, `BearerAuth`, `KerberosAuth`, `CustomAuth`, `NoAuth`; rotating tokens through token managers; mTLS, which needs `+s` or `+ssc` ([go pkg][go-pkg], [connect][gm-connect]).
- **Telemetry.** Set `TelemetryDisabled = true` to stop it ([go config][go-config]). The manual's example sets it to `false` to disable it, which looks like a doc error ([connect][gm-connect]).
- **Binary size impact: no figure found.**

**Test servers**
- **Tags.**
  - Community has no suffix, for example `2026.10.1`.
  - Enterprise adds `-enterprise`.
  - Base-image suffixes are `-trixie` (the default since 2026.01) and `-ubi10` ([docker][ops-docker]).
- **Image sizes.** Docker Hub's `size` field for amd64, probably compressed bytes (the unit is unverified) ([hub 2026.10][hub-2026], [hub 5.26][hub-526]):
  - `2026.10.1`: 379,882,820.
  - `2026.10.1-enterprise`: 574,934,932.
  - `5.26.32-trixie`: 351,778,923.
  - `5.26.32-enterprise-trixie`: 702,057,385.
- **Auth.** `NEO4J_AUTH=neo4j/<pw>` sets the initial password, minimum 8 characters by default; `NEO4J_AUTH=none` turns auth off. The username can't be changed. The setting is ignored when an existing `/data` volume is mounted ([docker][ops-docker]).
- **Enterprise license.** `NEO4J_ACCEPT_LICENSE_AGREEMENT=yes` (commercial) or `eval` (evaluation). The entrypoint exits with an error for any other value ([entrypoint][dk-entry], [macOS install][ops-osx]).
- **Memory.**
  - The entrypoint defaults `server.memory.pagecache.size=512M` ([entrypoint][dk-entry]).
  - Development systems need at least 2 GB ([requirements][ops-req]).
  - The default heap is unverified.
- **Startup time is unverified.** **Design:** poll `VerifyConnectivity` with a deadline instead of a fixed sleep.
- **Read-only user for tests.**
  - Not possible on Community.
  - On Enterprise with `eval`: `CREATE USER` plus the `reader` role. The exact `GRANT ROLE` syntax was not fetched and is unverified.
  - The entrypoint maps `NEO4J_` env vars to settings (`_` becomes `.`, `__` becomes `_`) ([entrypoint][dk-entry]). So `NEO4J_server_databases_read__only=neo4j` should make a Community container read-only. That is unverified.

## Facts dbscope must encode
- [ ] Use driver v6 (v6.3.0, Go 1.24, no dependencies). Accept only the six URI schemes and reject URIs with a path. Always set `DatabaseName`. Set `TelemetryDisabled = true`.
- [ ] Get plans with EXPLAIN and `Plan()`. Walk `Children()`. Strip any `@...` from operator names. Read `EstimatedRows` as a float and `Details` as a string.
- [ ] Classify operators:
  - High: `AllNodesScan` and its partitioned variant, `*AllRelationshipsScan`.
  - Scaled by label count: `NodeByLabelScan`.
  - Medium: `NodeIndexScan` and text-index scans.
  - Good: `NodeIndexSeek*`, `*CountFromCountStore`.
- [ ] Flag `CartesianProduct`, `Eager`, and `VarLengthExpand*` or `Repeat` with no upper bound in `Details`.
- [ ] Map GQLSTATUS `03N90`-`03N97` and `01N50`, by code only. Set `NotificationsMinSeverity` to INFORMATION for EXPLAIN.
- [ ] Never treat `AccessModeRead` as safety. Use the layered check: tokens, plan, timeout, then counters.
- [ ] Allow `CALL` only for procedures with `mode = READ`. Block `LOAD CSV`, `CALL {} IN TRANSACTIONS`, `USE` and admin commands.
- [ ] Detect Community through null `roles` from `SHOW CURRENT USER`. Warn that no read-only user is possible and suggest `server.databases.read_only`.
- [ ] Put `WithTxTimeout` on every query and map both timeout error codes. Avoid managed retries for user queries. Bound rows on the client.
- [ ] Catalog:
  - Token procedures.
  - Count-store counts, each checked with EXPLAIN first.
  - `SHOW INDEXES/CONSTRAINTS YIELD *`, read by column name.
  - `db.schema.*TypeProperties` under a timeout (no sampling).
  - `db.schema.visualization` as an estimate.
  - An explicit `CYPHER 5` or `CYPHER 25` prefix.
- [ ] Count store covers all nodes, one label, all relationships, one type, and a type with one endpoint label. Not both endpoint labels.
- [ ] APOC is optional, restricted by default, and only present when installed.
- [ ] Schema warnings:
  - New indexes are `POPULATING` until `ONLINE`.
  - Constraint creation scans all data and fails on conflicting data.
  - Existence, type and key constraints are Enterprise only.
  - Warn on `DROP INDEX` when `readCount > 0`.
  - Warn on unbatched bulk `SET` or `DETACH DELETE` on large labels.
- [ ] Test matrix:
  - `neo4j:2026.10.1`.
  - `neo4j:2026.10.1-enterprise` with `eval`.
  - `neo4j:5.26.32` for Cypher 5 behavior.
  - All with `NEO4J_AUTH=neo4j/<8+ chars>`.

[go-proxy6]: https://proxy.golang.org/github.com/neo4j/neo4j-go-driver/v6/@latest
[go-proxy5]: https://proxy.golang.org/github.com/neo4j/neo4j-go-driver/v5/@latest
[go-mod]: https://raw.githubusercontent.com/neo4j/neo4j-go-driver/v6.3.0/go.mod
[go-readme]: https://github.com/neo4j/neo4j-go-driver
[go-summary]: https://raw.githubusercontent.com/neo4j/neo4j-go-driver/v6.3.0/neo4j/resultsummary.go
[go-config]: https://raw.githubusercontent.com/neo4j/neo4j-go-driver/v6.3.0/neo4j/config/driver.go
[go-pkg]: https://pkg.go.dev/github.com/neo4j/neo4j-go-driver/v6/neo4j
[gm-tx]: https://raw.githubusercontent.com/neo4j/docs-drivers/dev/go-manual/modules/ROOT/pages/transactions.adoc
[gm-query]: https://raw.githubusercontent.com/neo4j/docs-drivers/dev/go-manual/modules/ROOT/pages/query-simple.adoc
[gm-summary]: https://raw.githubusercontent.com/neo4j/docs-drivers/dev/go-manual/modules/ROOT/pages/result-summary.adoc
[gm-connect]: https://raw.githubusercontent.com/neo4j/docs-drivers/dev/go-manual/modules/ROOT/pages/connect-advanced.adoc
[gm-uri]: https://raw.githubusercontent.com/neo4j/docs-drivers/dev/common-content/modules/ROOT/partials/connect-advanced.adoc
[ops-procs]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/procedures/built-in-procedures.adoc
[ops-showprocs]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/procedures/show-procedures.adoc
[ops-deprec]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/deprecations.adoc
[ops-config]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/configuration/configuration-settings.adoc
[ops-roles]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/authentication-authorization/built-in-roles.adoc
[ops-auth]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/authentication-authorization/index.adoc
[ops-users]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/authentication-authorization/manage-users.adoc
[ops-docker]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/docker/introduction.adoc
[ops-osx]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/installation/osx.adoc
[ops-req]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/installation/requirements.adoc
[ops-intro]: https://raw.githubusercontent.com/neo4j/docs-operations/dev/modules/ROOT/pages/introduction.adoc
[cy-list-idx]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/indexes/performance-indexes/list-indexes.adoc
[cy-create-idx]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/indexes/performance-indexes/create-indexes.adoc
[cy-constraints]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/schema/constraints/index.adoc
[cy-create-con]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/schema/constraints/create-constraints.adoc
[cy-list-con]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/schema/constraints/list-constraints.adoc
[cy-ops]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/planning-and-tuning/operators/operators-detail.adoc
[cy-plans]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/planning-and-tuning/execution-plans.adoc
[cy-rt]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/planning-and-tuning/runtimes/concepts.adoc
[cy-rt-par]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/planning-and-tuning/runtimes/reference.adoc
[cy-citx]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/subqueries/subqueries-in-transactions.adoc
[cy-clauses]: https://raw.githubusercontent.com/neo4j/docs-cypher/dev/modules/ROOT/pages/clauses/index.adoc
[sc-notif]: https://raw.githubusercontent.com/neo4j/docs-status-codes/dev/modules/ROOT/pages/notifications/all-notifications.adoc
[sc-notif-idx]: https://raw.githubusercontent.com/neo4j/docs-status-codes/dev/modules/ROOT/pages/notifications/index.adoc
[sc-errors]: https://raw.githubusercontent.com/neo4j/docs-status-codes/dev/modules/ROOT/pages/errors/all-errors.adoc
[qapi-profile]: https://neo4j.com/docs/query-api/current/profile-query/
[apoc-meta]: https://neo4j.com/docs/apoc/current/overview/apoc.meta/
[apoc-schema]: https://neo4j.com/docs/apoc/current/overview/apoc.meta/apoc.meta.schema/
[dk-entry]: https://raw.githubusercontent.com/neo4j/docker-neo4j/master/docker-image-src/calver/coredb/docker-entrypoint.sh
[dk-plugins]: https://raw.githubusercontent.com/neo4j/docker-neo4j/master/docker-image-src/calver/coredb/neo4j-plugins.json
[hub-latest]: https://hub.docker.com/v2/repositories/library/neo4j/tags/latest
[hub-2026]: https://hub.docker.com/v2/repositories/library/neo4j/tags?page_size=8&name=2026.10
[hub-526]: https://hub.docker.com/v2/repositories/library/neo4j/tags?page_size=3&name=5.26
[comm-slow]: https://community.neo4j.com/t/about-the-slow-speed-of-call-db-schema-nodetyproperties/57008/2

## Result

Status: partial. I answered all six sections from primary sources. A few sub-items are still unverified and are listed at the end. I created no files, ran no shell commands and wrote no project code.

### 1. Read-only guard design

**Recommendation.** Use four layers. No single layer is enough.

1. **Least-privilege role.** Grant only CONNECT, USAGE and SELECT. Do not use a superuser. Do not grant `pg_read_server_files`, `pg_write_server_files` or `pg_execute_server_program`. Revoke EXECUTE on dblink and `lo_*` where possible.
2. **Read-only transaction per query.** Use `BEGIN READ ONLY`. On a hot standby the server forces this regardless of the client.
3. **One statement over the extended protocol.** Use pgx `QueryExecModeExec`. The server then refuses multi-statement strings itself.
4. **A pg_query allowlist that fails closed.**

Real incidents show why each layer matters:
- Anthropic's reference Postgres MCP server wrapped queries in `BEGIN TRANSACTION READ ONLY`. A payload of `COMMIT; DROP ...` ended that transaction early, because the driver accepted multi-statement strings ([Datadog Security Labs](https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/)).
- crystaldba postgres-mcp checked function names only on `FuncCall` nodes. `SELECT * FROM pg_read_file(...)` parses as a `RangeFunction` and was never checked ([issue #178](https://github.com/crystaldba/postgres-mcp/issues/178)).
- awslabs postgres-mcp-server before 1.1.7 let `COPY ... TO PROGRAM` through in read-only mode. AWS's main fix advice is a minimal-privilege role ([GHSA-fph8-pg5w-78fv](https://github.com/awslabs/mcp/security/advisories/GHSA-fph8-pg5w-78fv)).

**(a) AST rules for pg_query v6.** I checked these field names in [pg_query.proto (17-latest)](https://raw.githubusercontent.com/pganalyze/libpg_query/17-latest/protobuf/pg_query.proto).

Accept:
- Exactly one `RawStmt`.
- The top-level statement must be a `SelectStmt`. This covers SELECT, VALUES and TABLE.
- Optionally, an `ExplainStmt` wrapping an allowed `SelectStmt`, with `ANALYZE` behind a separate opt-in.
- Optionally, `VariableShowStmt`.

Walk every node, including `larg`/`rarg`, `RangeSubselect.subquery`, `SubLink.subselect`, `CommonTableExpr.ctequery` and `RangeFunction.functions`. Reject the query if you find any of these:
- `SelectStmt.into_clause` is set (field 2, `IntoClause`). This is SELECT INTO.
- `SelectStmt.locking_clause` is non-empty (field 15). `LockingClause{locked_rels, strength, wait_policy}` covers FOR UPDATE, FOR NO KEY UPDATE, FOR SHARE and FOR KEY SHARE.
- An `InsertStmt`, `UpdateStmt`, `DeleteStmt` or `MergeStmt` anywhere. Inside a SELECT these appear as `SelectStmt.with_clause` → `ctes` → `CommonTableExpr.ctequery` (field 4). Each of these statements also has its own `with_clause`.
- Any other statement type. The allowlist already rejects these, and some matter more than they look:
  - `CopyStmt` (`is_from`, `is_program`, `filename`). A read-only transaction does not stop COPY TO a file or program (see 1c).
  - `CallStmt` and `DoStmt`.
  - `VariableSetStmt` (`is_local`). This covers SET, RESET, SET LOCAL and SET ROLE.
  - `TransactionStmt`.
  - Cursor statements, PREPARE, EXECUTE, LISTEN, NOTIFY, LOCK, LOAD, `CreateTableAsStmt` and all DDL.
- A function not on the allowlist. Check both `FuncCall.funcname` and the function calls inside `RangeFunction.functions`.

What the AST cannot see:
- Functions called inside views.
- Functions called through operators (`A_Expr`) and casts (`TypeCast`).
- Domain checks and row-level security policies.
- Which overload `search_path` actually picks.

So the AST function check is a usability layer. The role and the read-only transaction are what enforce safety.

**(b) Is the volatility rule sound?** It is mostly sound for built-in functions. It is not sound in general.

Two background facts:
- In `pg_proc.h`, the catalog default is `provolatile BKI_DEFAULT(i)`. A built-in function with no explicit marking is immutable ([pg_proc.h](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/include/catalog/pg_proc.h)).
- The volatility values below come from [REL_18 pg_proc.dat](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/include/catalog/pg_proc.dat). I also checked `query_to_xml` in REL_14.

| Function(s) | provolatile |
|---|---|
| pg_sleep, pg_sleep_for, pg_sleep_until | v |
| set_config | v |
| nextval, currval, setval | v |
| pg_terminate_backend, pg_cancel_backend, pg_reload_conf | v |
| pg_read_file, pg_read_binary_file, pg_ls_dir, pg_stat_file | v |
| lo_import, lo_export, lo_creat, lo_create, lo_from_bytea, lo_get, lo_put, lo_unlink, lo_truncate | v |
| pg_advisory_lock and pg_try_advisory_lock, all variants including xact and shared | v |
| pg_notify | v |
| query_to_xml, cursor_to_xml (14 and 18) | v |
| pg_stat_reset and all pg_stat_reset_* | v |
| pg_logical_emit_message, pg_logical_slot_* | v |
| pg_switch_wal, pg_create_restore_point | v |
| dblink, dblink_exec and other dblink functions | no marking in [dblink--1.2.sql](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/contrib/dblink/dblink--1.2.sql), so VOLATILE, which is the [CREATE FUNCTION default](https://www.postgresql.org/docs/current/xfunc-volatility.html) |
| **txid_current, pg_current_xact_id** | **s (STABLE)** |
| **table_to_xml, schema_to_xml, database_to_xml** and their `*_and_xmlschema` forms | **s (STABLE)** |

The two bold groups break the rule:
- `pg_current_xact_id` assigns a transaction ID if the transaction has none. It calls `PreventCommandDuringRecovery`, so it fails on a standby but works on a primary ([xid8funcs.c](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/backend/utils/adt/xid8funcs.c)).
- The STABLE XML functions return a whole table, schema or database in a single value. That defeats any row limit.

Put both groups on an explicit denylist.

User-defined functions marked STABLE can still write. The docs say STABLE and IMMUTABLE functions may contain only SELECT. They also say this is "not a completely bulletproof test", because such a function can call VOLATILE functions ([xfunc-volatility](https://www.postgresql.org/docs/current/xfunc-volatility.html)). C functions have no such limit. PostgREST's docs say Postgres lets you mark a modifying function STABLE ([PostgREST transactions](https://docs.postgrest.org/en/stable/references/transactions.html)). In a read-only transaction, local writes still fail, but `dblink_exec` from inside a STABLE function would not.

To make the rule stronger:
- Pin `search_path` with `SET LOCAL`.
- Look up every overload of each called name in `pg_proc`. Reject if any overload is volatile and not on the allowlist.
- Reject functions outside `pg_catalog` unless the user approves them.
- Keep the explicit denylist above.

How other tools do it:
- **PostgREST** runs GET and HEAD requests in READ ONLY transactions. It also uses READ ONLY for STABLE and IMMUTABLE RPC functions, even on POST.
- **Metabase** recommends a dedicated read-only database user ([docs](https://www.metabase.com/docs/latest/databases/users-roles-privileges)).
- **Supabase MCP** has a `read_only=true` option that excludes mutating tools ([repo](https://github.com/supabase-community/supabase-mcp)). I could not find a primary source for how it enforces this inside Postgres.
- The MCP incidents above all point the same way: a least-privilege role is the control that holds.

**(c) What a read-only transaction actually blocks.** Sources are [SET TRANSACTION](https://www.postgresql.org/docs/current/sql-set-transaction.html), [Hot Standby](https://www.postgresql.org/docs/18/hot-standby.html), [utility.c](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/backend/tcop/utility.c) and [sequence.c](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/backend/commands/sequence.c).

| Action | Blocked in a READ ONLY transaction on a primary? |
|---|---|
| nextval / setval | Yes, unless the sequence is temporary (`PreventCommandIfReadOnly`). Error 25006. |
| INSERT, UPDATE, DELETE, MERGE, COPY FROM into non-temp tables, including writes from functions | Yes |
| CREATE TEMP TABLE | Yes (all CREATE commands are blocked) |
| Writes into an already existing temp table | No |
| LISTEN / NOTIFY | No. They are `COMMAND_OK_IN_READ_ONLY_TXN`. They are blocked on a standby. |
| Advisory locks | No. They also work during recovery. |
| dblink writes to another server, or back to the same one | No. The docs say this explicitly. |
| COPY TO a file or PROGRAM | No. It is classed as strictly read-only. It needs the privileged roles listed above. |
| SET, including `default_transaction_read_only = off` for later transactions, and SET ROLE | No |

Two more notes:
- `default_transaction_read_only` is only a default that the client can override. Enforce read-only per transaction instead.
- The docs call this "a high-level notion of read-only that does not prevent all writes to disk".

### 2. Bounded result fetching in pgx v5 (pgx 5.11.0, released 2026-09-07, [CHANGELOG](https://raw.githubusercontent.com/jackc/pgx/master/CHANGELOG.md))

**(a) Wrapping in `SELECT * FROM (<sql>) q LIMIT n+1`.** Postgres does not guarantee that the inner ORDER BY survives. The docs say a particular output ordering "can only be guaranteed if the sort step is explicitly chosen" ([queries-order](https://www.postgresql.org/docs/18/queries-order.html)). Wrapping the raw text also breaks on trailing comments and semicolons. If you want a LIMIT, set `SelectStmt.limit_count` on the top-level node in the AST, only when there is no existing limit, and deparse.

**(b) Server-side cursor.**
- The semantics are exact, and the server stops after FETCH returns n+1 rows.
- Cursors are planned with `cursor_tuple_fraction` (default 0.1), which favours fast-start plans ([runtime-config-query](https://www.postgresql.org/docs/18/runtime-config-query.html)). Set it to 1.0 with SET LOCAL if the plan must match the plain EXPLAIN.
- `statement_timeout` applies to each FETCH, not to the cursor's whole life.

**(c) Reading n+1 rows, then `rows.Close()`.** `baseRows.Close` calls `ResultReader.Close`, which "consumes any remaining result data" ([rows.go](https://raw.githubusercontent.com/jackc/pgx/master/rows.go), [pgconn.go](https://raw.githubusercontent.com/jackc/pgx/master/pgconn/pgconn.go)). Close drains the result. It does not cancel. The server runs the whole query and sends every row. This option is only acceptable if you call `PgConn().CancelRequest` before Close, and that is racy.

**Best choice for a large result on a production replica: (b).**

```
BEGIN READ ONLY;
SET LOCAL ...;
DECLARE c NO SCROLL CURSOR FOR <deparsed AST>;
FETCH FORWARD n+1 FROM c;
CLOSE c;
COMMIT;
```

With PgBouncer, all three options work in transaction pooling, as long as everything stays inside one transaction and only SET LOCAL is used.

**Column metadata.** `rows.FieldDescriptions()` gives `Name` and `DataTypeOID`. It appears to also carry `TypeModifier`, `TableOID` and `TableAttributeNumber`. `TableOID` plus the attribute number would map result columns straight onto the ERD.

**JSON-safe values.** Ask for text results and map OIDs yourself.
- `QueryExecModeExec` returns text results. With no format codes, pgconn treats every column as text.
- You can also pass `pgx.QueryResultFormats{pgx.TextFormatCode}`.
- Then read `rows.RawValues()`.

Avoid `rows.Values()` for JSON output. In the pgtype source:
- UUID decodes to `[16]byte`, which marshals as an array of numbers.
- json and jsonb are unmarshalled into `any`, so large numbers become float64 and lose precision.
- Unknown OIDs come back as `string` in text format or `[]byte` in binary format.

Fix the output formats inside the transaction with SET LOCAL: DateStyle ISO, TimeZone UTC, IntervalStyle iso_8601, bytea_output hex.

Suggested mapping:

| Type | JSON output |
|---|---|
| int2, int4, oid | number |
| float4, float8 | number; NaN and Infinity as strings |
| int8 | string, or number only if \|x\| ≤ 2^53−1 |
| numeric | string |
| bool | true / false |
| json, jsonb | raw JSON |
| bytea | the `\x` hex string, or base64 |
| date, time and timestamp types, interval, uuid, enums, geometric types, arrays, unknown types | the text value as a string |

Use `pg_type` and `format_type(oid, typmod)` for display names.

### 3. Cancellation and limits

**Timeouts.** After `BEGIN READ ONLY`, set `statement_timeout`, `lock_timeout` and `idle_in_transaction_session_timeout` with SET LOCAL. `set_config(..., true)` does the same thing. On PG 17+, also use `transaction_timeout`, which was added in 17 ([17 release notes](https://www.postgresql.org/docs/release/17.0/)). In the extended protocol, `statement_timeout` starts when Parse, Bind, Execute or Describe arrives, and stops when Execute or Sync completes ([runtime-config-client](https://www.postgresql.org/docs/18/runtime-config-client.html)).

**pgx cancellation.** The default handler is `DeadlineContextWatcherHandler` ([config.go](https://raw.githubusercontent.com/jackc/pgx/master/pgconn/config.go)). When a context is cancelled, it sets a deadline on the socket and the connection dies. The server-side query may keep running. Use `CancelRequestContextWatcherHandler{CancelRequestDelay: 0, DeadlineDelay: a few seconds}` instead. It sends a protocol cancel request in a goroutine and keeps the deadline only as a fallback. It also sleeps 100 ms so it does not cancel the next query.

**Memory and temp files.**
- `work_mem` can be set by any user. Cap it, because memory use multiplies by operations and parallel workers.
- `temp_file_limit` is `PGC_SUSET` ([guc_tables.c](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/backend/utils/misc/guc_tables.c)). The read-only role cannot set it unless a DBA runs GRANT SET ON PARAMETER (PG 15+) or ALTER ROLE ... SET.

**Errors and what to tell users.** Sources are [postgres.c](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/backend/tcop/postgres.c) and [errcodes.txt](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/backend/utils/errcodes.txt).

| Code / severity | Meaning | What to tell the user |
|---|---|---|
| 40001 ERROR, "canceling statement due to conflict with recovery" | The replica replayed cleanup that this query still needed. | Safe to retry once. Shorten the query. Ask the DBA about `hot_standby_feedback` and `max_standby_streaming_delay`. |
| 40001 FATAL, "terminating connection..." | The session was idle in a transaction, or the conflict could not be retried. | Reconnect. |
| 57P04 FATAL | The database was dropped or moved on the primary. | Do not auto-retry. |
| 57014 | Statement timeout, or a user cancel. | Report as timed out or cancelled. |
| 25P03 / 25P04 | Idle-in-transaction timeout / transaction timeout. | Reconnect. |
| 25006 | Attempted write in a read-only transaction. | Report as a blocked write. |

On a primary, 40001 is an ordinary serialization failure. Tell the two cases apart by the message text or by `in_hot_standby` (PG 14+). pgx closes the connection automatically on FATAL errors.

### 4. EXPLAIN (FORMAT JSON)

Source is explain.c for [REL_18](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/src/backend/commands/explain.c), [REL_17](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/commands/explain.c) and [REL_14](https://raw.githubusercontent.com/postgres/postgres/REL_14_STABLE/src/backend/commands/explain.c).

**"Node Type" values seen in the source:** Result, ProjectSet, ModifyTable (with "Operation" set to Insert, Update, Delete or Merge), Append, Merge Append, Recursive Union, BitmapAnd, BitmapOr, Nested Loop, Merge Join, Hash Join, Seq Scan, Sample Scan, Gather, Gather Merge, Index Scan, Index Only Scan, Bitmap Index Scan, Bitmap Heap Scan, Tid Scan, Tid Range Scan, Subquery Scan, Function Scan, Table Function Scan, Values Scan, CTE Scan, Named Tuplestore Scan, WorkTable Scan, Foreign Scan.

Two notes on node types:
- In JSON, parallel nodes are flagged with "Parallel Aware" rather than a different node type. "Async Capable" exists in 14+.
- Memoize appears in the REL_14 source. I did not fetch the rest of the node-type switch. My understanding is that it contains Custom Scan, Materialize, Memoize, Sort, Incremental Sort, Group, Aggregate, WindowAgg, Unique, SetOp, LockRows, Limit and Hash.

**Keys that identify the relation:**
- **"Relation Name"** is set on Seq Scan, Sample Scan, Index Scan, Index Only Scan, Bitmap Heap Scan, Tid Scan, Tid Range Scan, Foreign Scan, Custom Scan and ModifyTable.
- Function Scan uses "Function Name". Named Tuplestore Scan uses "Tuplestore Name". WorkTable Scan uses "CTE Name".
- **"Schema" only appears with VERBOSE.** Always use `EXPLAIN (VERBOSE, FORMAT JSON)`.
- "Alias" is always emitted in JSON.
- "Index Name" (and "Scan Direction") appear on Index Scan and Index Only Scan.
- Bitmap Index Scan has "Index Name" but no "Relation Name". Take the table from the parent Bitmap Heap Scan, or look it up in `pg_index`.
- "Parent Relationship" and "Subplan Name" come from the plan tree. Children of Append, MergeAppend, BitmapAnd and BitmapOr are labelled "Member".

**Partitions, views, CTEs and function scans:**
- Each Append or Merge Append child is a scan whose "Relation Name" is the partition. Map partitions back to the parent table with `pg_inherits` or `pg_partition_root`.
- "Subplans Removed" is always emitted in non-text formats. It counts only run-time pruning. Partitions pruned at plan time do not appear in the plan at all.
- Views are expanded away. You see their base tables, with "Alias".
- A CTE appears as a CTE Scan plus an InitPlan child whose "Subplan Name" starts with "CTE". pev2 uses this rule.
- Subquery Scan appears only when the planner did not flatten the subquery.
- With VERBOSE, Function Scan shows "Function Call".

**Join keys:** "Join Type" (Inner, Left, Full, Right, Semi, Anti), "Inner Unique" (always present in JSON), "Hash Cond", "Merge Cond", "Join Filter", "Rows Removed by Join Filter".

**ANALYZE-only keys:**
- Timing and rows: "Actual Startup Time", "Actual Total Time", "Actual Rows", "Actual Loops", "Rows Removed by Filter", "Rows Removed by Index Recheck".
- Buffers: "Shared/Local Hit/Read/Dirtied/Written Blocks", "Temp Read/Written Blocks".
- Sort: "Sort Method", "Sort Space Used", "Sort Space Type".
- Hash: "Hash Buckets", "Original Hash Buckets", "Hash Batches", "Original Hash Batches", "Peak Memory Usage".
- Hash aggregate: "HashAgg Batches", "Disk Usage".
- Bitmap heap scan: "Exact Heap Blocks", "Lossy Heap Blocks".
- Parallel: "Workers Planned" (always present), "Workers Launched" (ANALYZE only).

**Version differences:**

| Version | Change |
|---|---|
| 14 | I/O timing keys are "I/O Read Time" and "I/O Write Time". "Async Capable" exists. |
| 15 | BUFFERS adds temp-file I/O timing. |
| 16 | New GENERIC_PLAN option. |
| 17 | I/O timing is split into "Shared/Local/Temp I/O Read/Write Time". New SERIALIZE option adds a "Serialization" group with "Time", "Output Volume" and "Format". New MEMORY option. SubPlan display changed. |
| 18 | BUFFERS is on by default with ANALYZE. "Index Searches" on Index Scan, Index Only Scan and Bitmap Index Scan. "Actual Rows" is fractional with 2 decimals. "Disabled" (boolean, always present in JSON). WAL buffers-full count. Memory and disk details on Material, WindowAgg and CTE nodes. |

Sources are the [15](https://www.postgresql.org/docs/15/release-15.html), [16](https://www.postgresql.org/docs/16/release-16.html), [17](https://www.postgresql.org/docs/release/17.0/) and [18](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/doc/src/sgml/release-18.sgml) release notes.

One safety note: EXPLAIN ANALYZE executes the query. Run it only inside the guarded read-only transaction, and keep it opt-in.

### 5. Heuristics used by plan visualisers

- **pev2** ([node.ts](https://raw.githubusercontent.com/dalibo/pev2/master/src/node.ts), [plan-service.ts](https://raw.githubusercontent.com/dalibo/pev2/master/src/services/plan-service.ts)):
  - Estimate factor is max(actual/plan, plan/actual), comparing per-loop rows. It is highlighted above 10, 100 and 1000.
  - A node's share of duration or cost is highlighted above 10%, 40% and 90%.
  - "Hash Batches" above 1 is flagged.
  - Rows removed is highlighted above 50% and 90% of rows read.
- **depesz** ([help](https://explain.depesz.com/help)):
  - Exclusive and inclusive time are coloured at 10%, 50% and 90% of total time.
  - "Rows x" is coloured above 10, 100 and 1000.
  - The rows column is coloured when many rows are removed.
- **pganalyze** ([insights](https://pganalyze.com/docs/explain/insights)) has these categories: Disk Sort, Hash Batches, Inefficient Index, Large Offset, Lossy Bitmaps, Mis-Estimate, Slow Scan (a seq scan with large "Rows Removed by Filter") and Stale Stats. The pages I read give no numeric thresholds.
- **pgMustard** publishes no numeric thresholds that I could find. It says it suppresses red herrings such as a seq scan on a small table ([home](https://www.pgmustard.com/)). It notes that in PG ≤17, "Actual Rows" was rounded per loop.

**Suggested dbscope rules.** These combine the rules above; I have not checked them against any tool.
- Only flag a node if its exclusive time share is at least 10%.
- Misestimate: factor of 10 or more.
- Seq Scan: rows removed is at least 90% of rows read, and the absolute count is large.
- Disk spill: Sort Space Type "Disk", Hash Batches above 1, HashAgg Disk Usage above 0, or Temp blocks above 0.
- Lossy bitmap: "Lossy Heap Blocks" above 0.
- Nested loop: high inner "Actual Loops" multiplied by per-loop time.
- Parallel shortfall: "Workers Launched" lower than "Workers Planned".

### 6. PgBouncer detection and query mode

PgBouncer has no official in-band signal. GitHub issues [#249](https://github.com/pgbouncer/pgbouncer/issues/249) and [#653](https://github.com/pgbouncer/pgbouncer/issues/653) ask for one; I don't know if either is resolved. Possible heuristics, none verified:
- Compare the PID in BackendKeyData with `pg_backend_pid()`. They should differ when a pooler sits in between.
- Compare `inet_server_port()` with the port you dialled.
- See whether the backend PID changes between transactions.

**Recommendation: don't rely on detection.** Always use the pooler-safe pattern:
- `QueryExecModeExec`: extended protocol, one round trip, no named statements, and the server rejects multi-statement strings.
- One explicit transaction per query, SET LOCAL only, and cursors only inside that transaction.

pgx's own docs say `QueryExecModeSimpleProtocol` should only be used for proxies that lack the extended protocol ([conn.go](https://raw.githubusercontent.com/jackc/pgx/master/conn.go)). jackc says `QueryExecModeCacheStatement` should not be used with PgBouncer 1.21 ([discussion #1784](https://github.com/jackc/pgx/discussions/1784)). PgBouncer's `max_prepared_statements` tracks only protocol-level prepared statements, not SQL-level PREPARE ([config](https://www.pgbouncer.org/config.html)).

### Unverified

- The 14–17 volatility of every function except `query_to_xml` (I checked those in 18 only).
- How `pg_notify()` behaves in a read-only transaction on a primary.
- Whether SELECT FOR UPDATE errors in a read-only transaction on a primary.
- Whether `txid_current` causes a WAL commit record.
- Whether `idle_in_transaction_session_timeout` and `transaction_timeout` set with SET LOCAL apply to the current transaction.
- The SQLSTATE for exceeding `temp_file_limit` (53400 is my guess).
- That go-pgquery exposes deparse.
- That pg_query_go v6 uses the PG17 grammar, so PG18-only syntax would fail to parse.
- The FieldDescription fields beyond Name and DataTypeOID.
- That the CTE Scan key is "CTE Name".
- The rest of the node-type list after Foreign Scan.
- The PgBouncer heuristics.
- Supabase's internal mechanism, and pgAdmin and Redash (not researched).
- pgMustard's thresholds.
- The fix status of crystaldba issue #178.

```json
{"status":"partial","summary":"Researched all six questions from primary sources: PostgreSQL source (pg_proc.dat, pg_proc.h, explain.c 14/17/18, utility.c, sequence.c, xid8funcs.c, postgres.c, guc_tables.c, errcodes.txt), PG docs and release notes, the pg_query 17 protobuf, pgx master source and CHANGELOG (5.11.0), pev2 source, depesz/pganalyze/pgMustard/PostgREST docs, and security advisories (Datadog, crystaldba #178, AWS GHSA-fph8-pg5w-78fv). Main findings: all listed dangerous built-ins are VOLATILE except txid_current and pg_current_xact_id (STABLE, assign an XID); the STABLE table_to_xml/schema_to_xml/database_to_xml bypass row limits. A read-only transaction does not block dblink, advisory locks, LISTEN/NOTIFY on a primary, writes to existing temp tables, SET, or COPY TO file/PROGRAM. pgx rows.Close drains the result rather than cancelling. EXPLAIN emits Schema only with VERBOSE. Recommended design: least-privilege role, BEGIN READ ONLY, single statement via QueryExecModeExec, AST allowlist that also checks RangeFunction, server-side cursor for bounded fetching, text result format, CancelRequestContextWatcherHandler. Some sub-items remain unverified and are listed.","artifacts":[],"tests":{"command":"","passed":null,"output_tail":""},"claims_to_verify":["txid_current and pg_current_xact_id are provolatile 's' in REL_18 pg_proc.dat","query_to_xml is provolatile 'v' in REL_14 and REL_18; table_to_xml/schema_to_xml/database_to_xml are 's'","pg_proc.h declares provolatile BKI_DEFAULT(i)","utility.c classifies ListenStmt/NotifyStmt as COMMAND_OK_IN_READ_ONLY_TXN and COPY TO as COMMAND_IS_STRICTLY_READ_ONLY","nextval/setval call PreventCommandIfReadOnly unless the sequence is local temp","pgconn ResultReader.Close drains remaining rows (does not cancel)","pgconn ParseConfig default BuildContextWatcherHandler is DeadlineContextWatcherHandler","explain.c emits Schema only when es->verbose; Alias always in non-text formats; Subplans Removed always in non-text formats","PG18 Actual Rows uses 2 decimals; Index Searches and Disabled added in 18","temp_file_limit is PGC_SUSET","Recovery conflict uses ERRCODE_T_R_SERIALIZATION_FAILURE (40001), and 57P04 for the database conflict","pev2 estimate thresholds 10/100/1000; depesz rows-x thresholds 10/100/1000 and time thresholds 10/50/90 percent","pgx 5.11.0 released 2026-09-07"],"open_questions":["Does dbscope require EXPLAIN ANALYZE at all on production replicas, or only plain EXPLAIN by default?","Is dbscope allowed to require a DBA-provisioned least-privilege role, or must it work with whatever credentials the user supplies?","Should PG19 (REL_19_STABLE appears in commit metadata) be in scope for EXPLAIN key mapping?"]}
```
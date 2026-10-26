# Query execution safety

dbscope runs user-written and LLM-generated SQL against databases that are often
production read replicas. No single mechanism is enough, so execution has four
layers. The AST checks give clear errors; the transaction and the role are what
actually enforce safety.

| Layer | Stops | Does not stop |
|---|---|---|
| 1. Least-privilege role (user's responsibility; the connection test warns when the role can write) | writes, `COPY TO PROGRAM`, file reads, `dblink` if not granted | — |
| 2. `BEGIN READ ONLY` per query, always ended with `ROLLBACK` | INSERT/UPDATE/DELETE/MERGE, `nextval`, `CREATE`, `FOR UPDATE` | `dblink` writes, advisory locks, `LISTEN`, writes to existing temp tables, `SET`, `COPY TO` |
| 3. One statement over the extended protocol (`QueryExecModeExec`) | `COMMIT; DROP …` style escapes | — |
| 4. AST allowlist (fails closed) + function check against the live catalog | everything below | functions reached through operators, casts, views, RLS policies |

`ROLLBACK`, never `COMMIT`: a `pg_notify()` inside a read-only transaction is
accepted and would be delivered on commit.

## Verified facts (PostgreSQL 17, 2026-10-27, as a SELECT-only role)

- `SELECT ... FOR UPDATE` in a read-only transaction: `ERROR: cannot execute SELECT FOR UPDATE in a read-only transaction`.
- `pg_notify()` in a read-only transaction: succeeds.
- `SET LOCAL statement_timeout='200ms'` then `pg_sleep(1)`: cancelled.
- A non-superuser cannot set `temp_file_limit` (`permission denied`).
- `DECLARE c NO SCROLL CURSOR FOR <sorted 1M rows>; FETCH FORWARD 3` returns exactly 3 rows.
- Volatility (`pg_proc.provolatile`): every dangerous built-in is `v`
  (`pg_sleep*`, `set_config`, `nextval/setval/currval/lastval`, `pg_terminate_backend`,
  `pg_cancel_backend`, `pg_reload_conf`, `pg_read_file`, `pg_read_binary_file`, `pg_ls_dir`,
  `lo_*`, `pg_advisory_*`, `pg_notify`, `query_to_xml`, `cursor_to_xml`, `pg_stat_reset*`,
  `pg_logical_*`, `pg_switch_wal`, `pg_create_restore_point`, `pg_promote`, replication slot/origin functions)
  **except** these STABLE ones, which must be denylisted explicitly:
  `txid_current`, `txid_current_if_assigned`, `txid_current_snapshot`, `pg_current_xact_id`,
  `pg_current_xact_id_if_assigned` (assign a transaction id) and `table_to_xml`, `schema_to_xml`,
  `database_to_xml` plus their `*_and_xmlschema` / `*xmlschema` forms (return a whole table/schema/db in one value).
- `go-pgquery` `Deparse` re-emits a modified tree: setting `LimitCount` on the top-level
  `SelectStmt` yields `... ORDER BY created_at DESC LIMIT 101`, dropping comments and the trailing `;`.
- `SELECT * FROM pg_read_file(...)` parses as `RangeFunction`, not `FuncCall`: both must be checked.
- pgx `FieldDescription` carries `TableOID` and `TableAttributeNumber`, mapping result columns to ERD columns.
- pgx `rows.Close()` drains remaining rows rather than cancelling; use a cursor to bound work.

## Function rule

A function call is allowed when every overload with that name (resolved against the
pinned `search_path`) satisfies:

1. not in the denylist above, and
2. `provolatile` is `i` or `s`, **or** the function is in the small volatile allowlist
   (`random`, `random_normal`, `clock_timestamp`, `timeofday`, `gen_random_uuid`, `uuid_generate_v4`), and
3. it lives in `pg_catalog` or belongs to an installed extension (`pg_depend.deptype = 'e'`).
   Other user-defined functions are rejected with a message naming them: a STABLE label is
   not proof that a function does not write (e.g. via `dblink_exec`).

## Execution sequence

```
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY
SELECT set_config('statement_timeout', $1, true), set_config('lock_timeout', '2s', true),
       set_config('idle_in_transaction_session_timeout', $2, true),
       set_config('search_path', $3, true), set_config('DateStyle', 'ISO', true),
       set_config('TimeZone', 'UTC', true), set_config('IntervalStyle', 'iso_8601', true),
       set_config('bytea_output', 'hex', true), set_config('cursor_tuple_fraction', '1', true)
       -- PG17+: set_config('transaction_timeout', $1, true)
EXPLAIN (VERBOSE, FORMAT JSON) <deparsed>          -- cost gate + plan overlay
DECLARE dbscope_c NO SCROLL CURSOR FOR <deparsed>
FETCH FORWARD <maxRows+1> FROM dbscope_c          -- text format, values mapped per type
ROLLBACK
```

EXPLAIN ANALYZE executes the query; it is opt-in per request and runs inside the same
read-only transaction.

## Value mapping (text format)

| Type | JSON |
|---|---|
| int2, int4, oid, float4, float8 | number (`NaN`/`Infinity` as strings) |
| int8 | number when \|x\| ≤ 2^53−1, else string |
| numeric | string |
| bool | boolean |
| json, jsonb | raw JSON |
| NULL | `null` |
| everything else (bytea hex, dates/times ISO, interval ISO 8601, uuid, enums, arrays, geometric, unknown) | the text value |

## Errors shown to users

| SQLSTATE | Meaning | UI message |
|---|---|---|
| 57014 | statement timeout or cancel | "Stopped after the profile's statement timeout" / "Cancelled" |
| 40001 + "conflict with recovery" | replica replayed cleanup the query needed | retry once; ask the DBA about `hot_standby_feedback` |
| 57P04 | database dropped on primary | do not retry |
| 25006 | write attempted in read-only transaction | "Blocked: this would modify data" |
| 25P03 / 25P04 | idle-in-transaction / transaction timeout | reconnect |

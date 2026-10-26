# dbscope agent HTTP API

All routes are under `/api`, require the `X-Dbscope-Token` header, and use JSON.
Errors always use one envelope and an appropriate 4xx/5xx status:

```json
{ "error": { "code": "validation", "message": "port must be 1-65535", "detail": "optional" } }
```

`code` values: `validation`, `not_found`, `conflict`, `unauthorized`, `forbidden`, `secrets_locked`, `upstream`, `timeout`, `internal`.

The request guard uses the same envelope: `401 unauthorized` for a missing or
stale token (e.g. the agent restarted), `403 forbidden` for a foreign Host or Origin.

## Phase 0

### `GET /api/health`
`200 {"version":"dev","os":"darwin","arch":"arm64"}`

### Profiles

```ts
type Env = "local" | "staging" | "prod-replica";
type SummaryMode = "off" | "profile" | "rows";
type SSLMode = "disable" | "prefer" | "require" | "verify-ca" | "verify-full";

interface Profile {
  id: string;               // ULID
  name: string;             // unique, 1-80 chars
  host: string;
  port: number;             // 1-65535, default 5432
  database: string;
  user: string;
  sslMode: SSLMode;         // default "prefer"
  sslRootCert?: string;     // file path, never file contents
  env: Env;                 // required
  schemas: string[];        // empty = all non-system schemas
  summaryMode: SummaryMode; // default "profile"
  maxRowsToLLM: number;     // default 50, 1-500
  statementTimeoutMs: number; // default 30000 local/staging, 10000 prod-replica
  hasPassword: boolean;     // the password itself is never returned
  createdAt: string;        // RFC 3339
  updatedAt: string;
}
```

- `GET /api/profiles` → `200 Profile[]` (sorted by name)
- `POST /api/profiles` body: Profile fields minus `id/hasPassword/createdAt/updatedAt`, plus optional `password` → `201 Profile`
- `PUT /api/profiles/{id}` same body; omitting `password` keeps the stored one, `"password": ""` clears it → `200 Profile`
- `DELETE /api/profiles/{id}` → `204` (also deletes its secret)
- `POST /api/connection-url/parse` body `{"url":"postgres://u:p@h:5432/db?sslmode=require"}` → `200 {host,port,database,user,password?,sslMode}`; accepts `postgres://` and `postgresql://`

### Connection test

- `POST /api/profiles/test` body: same as create (unsaved profile) → `200 TestResult`
- `POST /api/profiles/{id}/test` → `200 TestResult` (uses the stored secret)

```ts
interface TestResult {
  ok: boolean;
  latencyMs?: number;
  serverVersion?: string;        // e.g. "17.2"
  serverVersionNum?: number;     // e.g. 170002
  isReplica?: boolean;           // pg_is_in_recovery()
  defaultReadOnly?: boolean;     // default_transaction_read_only
  writePrivileges?: string[];    // e.g. ["INSERT on public.orders", "CREATE on schema public"]; empty = SELECT-only
  pgStatStatements?: boolean;    // extension installed and readable
  warnings: string[];            // human sentences, e.g. env=prod-replica but server is not in recovery
  error?: { kind: "auth" | "network" | "tls" | "timeout" | "database" | "other"; message: string; sqlstate?: string };
}
```

Test runs with a 10s connect timeout and never logs the DSN or password.

### Secrets

- `GET /api/secrets/status` → `200 {"backend":"keyring"|"passphrase","locked":bool}`
- `POST /api/secrets/unlock` body `{"passphrase":"..."}` → `204` or `401 {"error":{"code":"unauthorized"}}`
  (only meaningful for the passphrase backend; the first unlock sets the passphrase)

Profile create/update/test-by-id with a locked vault → `423 {"error":{"code":"secrets_locked"}}`.

## Phase 1

### Schema introspection

- `GET /api/profiles/{id}/namespaces` → `200 [{"name":"public","relationCount":12}]`
  Non-system schemas (excludes `pg_catalog`, `information_schema`, `pg_toast`, `pg_temp_*`), sorted by name.
  Lets the UI offer a schema picker. Requires the vault to be unlocked when the profile has a password (`423` otherwise).
- `GET /api/profiles/{id}/schema` → `200 Model` (the JSON form of `internal/schema.Model`)
  Introspects `profile.schemas`, or, when that list is empty, every non-system schema the role has USAGE on
  (schemas named explicitly are read even without USAGE, since catalogs are world-readable). Runs in one
  `REPEATABLE READ READ ONLY` transaction with `statement_timeout` = the profile's `statementTimeoutMs`.
  Partitions are collapsed into their parent (`partitions`, `partitionKey`, summed `rowEstimate`).
  Relations outside the selected schemas that a foreign key references are included with `"stub": true`.
  Foreign keys inferred from naming carry `"inferred": true` and a `confidence` in (0,1]; inferred
  keys the user rejected are omitted and accepted ones are returned with `"inferred": true, "confidence": 1`.
  Errors: connection failures → `502 {"error":{"code":"upstream", ...}}` with `detail` = the TestResult
  error kind (`auth|network|tls|timeout|database|other`); statement timeout → `504 {"error":{"code":"timeout"}}`.

### Inferred foreign keys

- `PUT /api/profiles/{id}/inferred-fks` body `{"key":"public.posts(user_id)->public.users(id)","status":"accepted"|"rejected"|"unset"}` → `204`
  The key format is `<schema>.<table>(<col>[,<col>])-><schema>.<table>(<col>[,<col>])`, exactly as the
  server renders it in `ForeignKey` via `name` for inferred keys.

### Canvas layouts

- `GET /api/profiles/{id}/layout?key=<layoutKey>` → `200 {"key":"...","positions":{"public.users":{"x":0,"y":0}},"collapsed":["public.audit"],"updatedAt":"..."}` or `404`
- `PUT /api/profiles/{id}/layout` body `{"key":"...","positions":{...},"collapsed":[...]}` → `204`
  `layoutKey` is chosen by the UI (e.g. the sorted schema list); max 1 MiB body.

### Sample schema

- `GET /api/sample/schema` → `200 Model` — a built-in, realistic ~40-table e-commerce + SaaS schema
  (no database needed) used by the onboarding "Open sample schema" action. `serverVersion` is 170000.

New error codes: `upstream` (502), `timeout` (504). A schema request that arrives while the agent is
shutting down gets `503 internal`.

## Phase 2

Design and verified facts: `docs/QUERY-SAFETY.md`.

### Run a query

`POST /api/profiles/{id}/query`

```ts
interface QueryRequest {
  sql: string;              // exactly one SELECT (incl. VALUES / TABLE / WITH ... SELECT)
  maxRows?: number;         // default 500, 1..5000
  analyze?: boolean;        // EXPLAIN ANALYZE as well (executes the query a second time); default false
  confirmCost?: boolean;    // re-send true after a 409 cost_confirmation_required
}
interface QueryResult {
  columns: { name: string; type: string; typeOid: number; relation?: string; column?: string }[];
                            // relation = "schema.name" (partition children mapped to their root), column = attribute name
  rows: (string | number | boolean | null | object | unknown[])[][];  // see QUERY-SAFETY.md value mapping
  truncated: boolean;       // more than maxRows existed
  rowCount: number;         // rows returned (<= maxRows)
  durationMs: number;       // FETCH time
  executedSql: string;      // the deparsed statement actually run
  plan: PlanNode;           // EXPLAIN (VERBOSE) tree; with analyze, ANALYZE + BUFFERS figures
  overlay: Overlay;
  warnings: string[];
}
interface PlanNode {
  nodeType: string; relation?: string; alias?: string; index?: string; joinType?: string;
  condition?: string;       // Hash/Merge Cond, Join Filter, Index Cond, Filter (first present)
  startupCost: number; totalCost: number; planRows: number;
  actualRows?: number; actualLoops?: number; actualTotalMs?: number; rowsRemovedByFilter?: number;
  costShare: number;        // exclusive cost / total cost, 0..1
  timeShare?: number;       // exclusive time / total time (analyze only)
  flags: { level: "info" | "warn" | "danger"; code: string; message: string }[];
  children: PlanNode[];
}
interface Overlay {
  relations: Record<string, {           // key "schema.name"
    level: "good" | "warn" | "danger";  // worst over its scans
    scans: { nodeType: string; index?: string; planRows: number; actualRows?: number }[];
    reasons: string[];                   // human sentences, e.g. "Seq Scan reads 2.1M rows and discards 99%"
  }>;
  joins: { from: string; to: string; method: "Hash Join" | "Merge Join" | "Nested Loop"; joinType: string; condition?: string }[];
}
```

Errors (envelope `code`):
- `422 rejected` — the guard refused the statement. `detail` is a machine reason
  (`multiple_statements|not_select|writes_data|locking_clause|select_into|function_not_allowed|syntax`);
  `message` is a sentence naming the construct. For `syntax`, `position` (1-based char offset) is added to the envelope.
- `409 cost_confirmation_required` — EXPLAIN estimates over the profile's limit. The `error` object adds
  `estimatedCost` and `estimatedRows`. Re-send with `confirmCost: true`.
- `400 database` — Postgres rejected the statement; the `error` object adds `sqlstate` and `position` when
  known. This `position` refers to `executedSql` (the deparsed text), not the user's input; the 422 syntax
  `position` refers to the user's input.
- `504 timeout` (57014 from statement_timeout), `503 recovery_conflict` (40001 conflict with recovery),
  `502 upstream` (connection), `423 secrets_locked`, `404 not_found`.
- Client disconnect (fetch aborted) cancels the running statement server-side.

Cost limits: `prod-replica` 1e6, `staging` 1e7, `local` none. Profiles gain an optional `maxCost: number`:
omitted = the environment default, `0` = no limit, otherwise the limit.

### Explain only

`POST /api/profiles/{id}/explain` body `{ sql, analyze?, confirmCost? }` → `200 { executedSql, plan, overlay, warnings }`
(same guard, transaction and errors; no rows fetched). The cost gate applies only when `analyze` is true,
since plain EXPLAIN does not execute the query. Not recorded in history.

### History

`GET /api/profiles/{id}/history?limit=50` → `200 [{ id, sql, startedAt, durationMs, rowCount, status: "ok"|"error"|"rejected"|"cancelled", errorCode? }]`
Newest first, max 200. Stores SQL and metadata only, never row data. Deleted with the profile.

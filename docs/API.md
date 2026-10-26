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

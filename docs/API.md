# dbscope agent HTTP API

All routes are under `/api`, require the `X-Dbscope-Token` header, and use JSON.
Errors always use one envelope and an appropriate 4xx/5xx status:

```json
{ "error": { "code": "validation", "message": "port must be 1-65535", "detail": "optional" } }
```

`code` values: `validation`, `not_found`, `conflict`, `unauthorized`, `forbidden`, `secrets_locked`, `internal`.

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

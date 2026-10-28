/** Wire types for the Phase 0 agent API (docs/API.md). */

export type Engine =
  | "postgres"
  | "mysql"
  | "sqlite"
  | "mongodb"
  | "redis"
  | "dynamodb"
  | "cassandra"
  | "neo4j"
  | "elasticsearch"
  | "clickhouse"
  | "sqlserver"
  | "oracle";
export type Env = "local" | "staging" | "prod-replica";
export type SummaryMode = "off" | "profile" | "rows";
export type SSLMode = "disable" | "prefer" | "require" | "verify-ca" | "verify-full";

/** The modes an engine supports beyond the ERD, which every engine has (docs/ENGINES.md). */
export interface Capabilities {
  query: boolean;
  ask: boolean;
  migrations: boolean;
  workload: boolean;
  /** The key-pattern report of Redis and Valkey. */
  keyspace?: boolean;
  /** Server health checks (docs/HEALTH.md). */
  health?: boolean;
}

export interface Profile {
  id: string;
  name: string;
  engine: Engine;
  /** The database file of a file-based engine (SQLite). */
  path?: string;
  /** Connection-string options for MongoDB, as a URL query: authSource=admin&replicaSet=rs0 (srv=true for mongodb+srv). */
  options?: string;
  host: string;
  port: number;
  database: string;
  user: string;
  sslMode: SSLMode;
  sslRootCert?: string;
  env: Env;
  schemas: string[];
  summaryMode: SummaryMode;
  maxRowsToLLM: number;
  statementTimeoutMs: number;
  /**
   * EXPLAIN cost above which a query needs confirming. Omitted = the environment
   * default (prod-replica 1e6, staging 1e7, local none); 0 = no limit.
   */
  maxCost?: number;
  hasPassword: boolean;
  capabilities: Capabilities;
  createdAt: string;
  updatedAt: string;
}

/** Body for create, update and unsaved test. Omitting `password` on update keeps the stored one. */
export type ProfileInput = Omit<Profile, "id" | "hasPassword" | "capabilities" | "createdAt" | "updatedAt"> & {
  password?: string;
};

export interface ParsedConnectionUrl {
  engine: Engine;
  host: string;
  port: number;
  database: string;
  user: string;
  password?: string;
  sslMode: SSLMode;
  options?: string;
  /** What the URL held that a profile cannot keep, such as extra hosts. */
  notes?: string[];
}

export type TestErrorKind = "auth" | "network" | "tls" | "timeout" | "database" | "other";

export interface TestResult {
  ok: boolean;
  /** The server reached; omitted for PostgreSQL. */
  engine?: Engine | "mariadb" | "valkey" | "scylladb" | "opensearch";
  latencyMs?: number;
  serverVersion?: string;
  serverVersionNum?: number;
  isReplica?: boolean;
  defaultReadOnly?: boolean;
  writePrivileges?: string[];
  pgStatStatements?: boolean;
  warnings: string[];
  error?: { kind: TestErrorKind; message: string; sqlstate?: string };
}

export interface SecretsStatus {
  backend: "keyring" | "passphrase";
  locked: boolean;
  /** False until the vault file exists, so there is nothing saved to unlock yet. */
  initialized: boolean;
}

export interface Health {
  version: string;
  os: string;
  arch: string;
}

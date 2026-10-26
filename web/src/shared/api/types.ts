/** Wire types for the Phase 0 agent API (docs/API.md). */

export type Env = "local" | "staging" | "prod-replica";
export type SummaryMode = "off" | "profile" | "rows";
export type SSLMode = "disable" | "prefer" | "require" | "verify-ca" | "verify-full";

export interface Profile {
  id: string;
  name: string;
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
  hasPassword: boolean;
  createdAt: string;
  updatedAt: string;
}

/** Body for create, update and unsaved test. Omitting `password` on update keeps the stored one. */
export type ProfileInput = Omit<Profile, "id" | "hasPassword" | "createdAt" | "updatedAt"> & {
  password?: string;
};

export interface ParsedConnectionUrl {
  host: string;
  port: number;
  database: string;
  user: string;
  password?: string;
  sslMode: SSLMode;
}

export type TestErrorKind = "auth" | "network" | "tls" | "timeout" | "database" | "other";

export interface TestResult {
  ok: boolean;
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
}

export interface Health {
  version: string;
  os: string;
  arch: string;
}

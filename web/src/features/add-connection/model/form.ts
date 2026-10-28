import { defaultStatementTimeoutMs } from "@/entities/connection";
import type { Engine, Env, ParsedConnectionUrl, Profile, ProfileInput, SSLMode, SummaryMode } from "@/shared/api";

/** Raw form state. Numbers stay strings until validated so partial input is kept. */
export interface ConnectionFormValues {
  name: string;
  engine: Engine;
  /** The database file, for SQLite. */
  path: string;
  /** MongoDB connection-string options. */
  options: string;
  host: string;
  port: string;
  database: string;
  user: string;
  password: string;
  sslMode: SSLMode;
  sslRootCert: string;
  env: Env | undefined;
  schemas: string;
  summaryMode: SummaryMode;
  statementTimeoutMs: string;
  /** "" = the environment default, "0" = no limit. */
  maxCost: string;
  maxRowsToLLM: number;
}

export type ConnectionField = keyof ConnectionFormValues;
export type ConnectionFormErrors = Partial<Record<ConnectionField, string>>;

/** Display order, used to focus the first invalid field. */
export const FIELD_ORDER: readonly ConnectionField[] = [
  "name",
  "env",
  "path",
  "options",
  "host",
  "port",
  "database",
  "user",
  "password",
  "sslMode",
  "sslRootCert",
  "schemas",
  "summaryMode",
  "statementTimeoutMs",
  "maxCost",
];

export const EMPTY_CONNECTION_FORM: ConnectionFormValues = {
  name: "",
  engine: "postgres",
  path: "",
  options: "",
  host: "",
  port: "5432",
  database: "",
  user: "",
  password: "",
  sslMode: "prefer",
  sslRootCert: "",
  env: undefined,
  schemas: "",
  summaryMode: "profile",
  statementTimeoutMs: String(defaultStatementTimeoutMs(undefined)),
  maxCost: "",
  maxRowsToLLM: 50,
};

export function formFromProfile(profile: Profile): ConnectionFormValues {
  return {
    name: profile.name,
    engine: profile.engine,
    path: profile.path ?? "",
    options: profile.options ?? "",
    host: profile.host,
    port: String(profile.port),
    database: profile.database,
    user: profile.user,
    password: "",
    sslMode: profile.sslMode,
    sslRootCert: profile.sslRootCert ?? "",
    env: profile.env,
    schemas: profile.schemas.join(", "),
    summaryMode: profile.summaryMode,
    statementTimeoutMs: String(profile.statementTimeoutMs),
    maxCost: profile.maxCost === undefined ? "" : String(profile.maxCost),
    maxRowsToLLM: profile.maxRowsToLLM,
  };
}

const NAME_MAX = 80;

function isWholeNumber(value: string): boolean {
  return /^\d+$/.test(value.trim());
}

export function validateConnectionForm(values: ConnectionFormValues): ConnectionFormErrors {
  const errors: ConnectionFormErrors = {};
  const name = values.name.trim();
  if (!name) errors.name = "Enter a name for this connection.";
  else if (name.length > NAME_MAX) errors.name = `Use at most ${NAME_MAX} characters (now ${name.length}).`;

  if (!values.env) errors.env = "Choose the environment this database belongs to.";
  if (values.engine === "dynamodb") {
    if (!new URLSearchParams(values.options).get("region")) errors.options = "Enter the AWS region, such as eu-west-1.";
  } else if (values.engine === "sqlite") {
    if (!isAbsolutePath(values.path.trim())) {
      errors.path = "Enter the full path of the database file, e.g. /Users/you/app.db or C:\\data\\app.db.";
    }
  } else {
    if (!values.host.trim()) errors.host = "Enter the server host name or IP address.";
    const port = Number(values.port);
    if (!isWholeNumber(values.port) || port < 1 || port > 65_535) {
      errors.port = "Port must be a whole number from 1 to 65535.";
    }
    if (values.engine === "redis") {
      // A Redis database is a number, default 0, and the user is optional (the default user).
      if (values.database.trim() && !isWholeNumber(values.database))
        errors.database = "A Redis database is a number, such as 0.";
    } else if (values.engine === "neo4j" || values.engine === "elasticsearch" || values.engine === "clickhouse") {
      // The database defaults (neo4j, or every index), and a server may run without
      // authentication; an Elasticsearch API key goes in the password with no user.
    } else if (values.engine === "cassandra") {
      // The user is optional: a cluster without authentication has none.
      if (!values.database.trim()) errors.database = "Enter the keyspace.";
    } else {
      if (!values.database.trim()) errors.database = "Enter the database name.";
      if (!values.user.trim()) errors.user = "Enter the user name.";
    }
  }

  if (!isWholeNumber(values.statementTimeoutMs) || Number(values.statementTimeoutMs) < 1) {
    errors.statementTimeoutMs = "Timeout must be a whole number of milliseconds, 1 or more.";
  }
  const cost = values.maxCost.trim();
  if (cost && !(/^\d+(\.\d+)?$/.test(cost) && Number.isFinite(Number(cost)))) {
    errors.maxCost = "Cost limit must be 0 (no limit) or a positive number, or empty for the default.";
  }
  return errors;
}

/** An absolute POSIX path or a Windows drive or UNC path; the agent checks it again. */
function isAbsolutePath(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith("\\\\");
}

export function firstInvalidField(errors: ConnectionFormErrors): ConnectionField | undefined {
  return FIELD_ORDER.find((field) => errors[field]);
}

export function parseSchemas(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

/**
 * Converts validated values into the API body. An empty password is omitted,
 * which keeps the stored secret on update and stores none on create.
 */
export function toProfileInput(values: ConnectionFormValues): ProfileInput {
  if (!values.env) throw new Error("toProfileInput requires a validated form with env set");
  if (values.engine === "dynamodb") {
    return {
      name: values.name.trim(),
      engine: "dynamodb",
      options: values.options,
      host: "",
      port: 0,
      database: "",
      user: "",
      sslMode: "prefer",
      env: values.env,
      schemas: [],
      summaryMode: values.summaryMode,
      maxRowsToLLM: values.maxRowsToLLM,
      statementTimeoutMs: Number(values.statementTimeoutMs),
    };
  }
  if (values.engine === "sqlite") {
    return {
      name: values.name.trim(),
      engine: "sqlite",
      path: values.path.trim(),
      host: "",
      port: 0,
      database: "",
      user: "",
      sslMode: "prefer",
      env: values.env,
      schemas: [],
      summaryMode: values.summaryMode,
      maxRowsToLLM: values.maxRowsToLLM,
      statementTimeoutMs: Number(values.statementTimeoutMs),
    };
  }
  const input: ProfileInput = {
    engine: values.engine,
    ...((values.engine === "mongodb" || values.engine === "cassandra" || values.engine === "neo4j") &&
    values.options.trim()
      ? { options: values.options.trim() }
      : {}),
    name: values.name.trim(),
    host: values.host.trim(),
    port: Number(values.port),
    database: values.database.trim(),
    user: values.user.trim(),
    sslMode: values.sslMode,
    env: values.env,
    schemas: parseSchemas(values.schemas),
    summaryMode: values.summaryMode,
    maxRowsToLLM: values.maxRowsToLLM,
    statementTimeoutMs: Number(values.statementTimeoutMs),
  };
  // Omitted means the environment default; the agent treats 0 as no limit.
  if (values.maxCost.trim()) input.maxCost = Number(values.maxCost.trim());
  const rootCert = values.sslRootCert.trim();
  if (rootCert && (values.sslMode === "verify-ca" || values.sslMode === "verify-full")) {
    input.sslRootCert = rootCert;
  }
  if (values.password) input.password = values.password;
  return input;
}

/** Fields the parse endpoint filled, for the confirmation message. */
export function applyParsedUrl(
  values: ConnectionFormValues,
  parsed: ParsedConnectionUrl,
): { values: ConnectionFormValues; filled: string[] } {
  const next: ConnectionFormValues = {
    ...values,
    host: parsed.host,
    port: String(parsed.port),
    database: parsed.database,
    user: parsed.user,
    sslMode: parsed.sslMode,
  };
  const filled = ["host", "port", "database", "user", "SSL mode"];
  if (parsed.password !== undefined) {
    next.password = parsed.password;
    filled.push("password");
  }
  if (!values.name.trim() && parsed.database && parsed.host) {
    next.name = `${parsed.database}@${parsed.host}`.slice(0, NAME_MAX);
    filled.unshift("name");
  }
  return { values: next, filled };
}

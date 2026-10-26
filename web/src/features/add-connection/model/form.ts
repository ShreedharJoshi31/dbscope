import { defaultStatementTimeoutMs } from "@/entities/connection";
import type { Env, ParsedConnectionUrl, Profile, ProfileInput, SSLMode, SummaryMode } from "@/shared/api";

/** Raw form state. Numbers stay strings until validated so partial input is kept. */
export interface ConnectionFormValues {
  name: string;
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
  maxRowsToLLM: number;
}

export type ConnectionField = keyof ConnectionFormValues;
export type ConnectionFormErrors = Partial<Record<ConnectionField, string>>;

/** Display order, used to focus the first invalid field. */
export const FIELD_ORDER: readonly ConnectionField[] = [
  "name",
  "env",
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
];

export const EMPTY_CONNECTION_FORM: ConnectionFormValues = {
  name: "",
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
  maxRowsToLLM: 50,
};

export function formFromProfile(profile: Profile): ConnectionFormValues {
  return {
    name: profile.name,
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
  if (!values.host.trim()) errors.host = "Enter the server host name or IP address.";

  const port = Number(values.port);
  if (!isWholeNumber(values.port) || port < 1 || port > 65_535) {
    errors.port = "Port must be a whole number from 1 to 65535.";
  }
  if (!values.database.trim()) errors.database = "Enter the database name.";
  if (!values.user.trim()) errors.user = "Enter the user name.";

  if (!isWholeNumber(values.statementTimeoutMs) || Number(values.statementTimeoutMs) < 1) {
    errors.statementTimeoutMs = "Timeout must be a whole number of milliseconds, 1 or more.";
  }
  return errors;
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
  const input: ProfileInput = {
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

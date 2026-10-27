/** Server error codes from docs/API.md, plus two the client raises itself. */
export type ServerErrorCode =
  | "validation"
  | "not_found"
  | "conflict"
  | "unauthorized"
  | "forbidden"
  | "secrets_locked"
  | "upstream"
  | "timeout"
  | "internal"
  // Phase 2: query execution
  | "rejected"
  | "cost_confirmation_required"
  | "database"
  | "recovery_conflict"
  // Phase 3: assistant
  | "llm_not_configured"
  | "llm";
export type ApiErrorCode = ServerErrorCode | "network" | "malformed_response";

const SERVER_CODES: readonly ServerErrorCode[] = [
  "validation",
  "not_found",
  "conflict",
  "unauthorized",
  "forbidden",
  "secrets_locked",
  "upstream",
  "timeout",
  "internal",
  "rejected",
  "cost_confirmation_required",
  "database",
  "recovery_conflict",
  "llm_not_configured",
  "llm",
];

/** Fields some Phase 2 errors add to the envelope (docs/API.md "Run a query"). */
export interface ApiErrorExtras {
  /** PostgreSQL SQLSTATE, e.g. "42P01". */
  sqlstate?: string | undefined;
  /**
   * 1-based character offset. For `rejected` (syntax) it points into the SQL the
   * user sent; for `database` it points into the result's `executedSql`.
   */
  position?: number | undefined;
  estimatedCost?: number | undefined;
  estimatedRows?: number | undefined;
  /**
   * The deparsed statement a `database` position refers to. The agent does not
   * send it today (docs/API.md lists only sqlstate and position); read it when present.
   */
  executedSql?: string | undefined;
}

export class ApiError extends Error {
  override readonly name = "ApiError";
  readonly code: ApiErrorCode;
  readonly status: number;
  readonly detail: string | undefined;
  readonly sqlstate: string | undefined;
  readonly position: number | undefined;
  readonly estimatedCost: number | undefined;
  readonly estimatedRows: number | undefined;
  readonly executedSql: string | undefined;

  constructor(code: ApiErrorCode, message: string, status: number, detail?: string, extras: ApiErrorExtras = {}) {
    super(message);
    this.code = code;
    this.status = status;
    this.detail = detail;
    this.sqlstate = extras.sqlstate;
    this.position = extras.position;
    this.estimatedCost = extras.estimatedCost;
    this.estimatedRows = extras.estimatedRows;
    this.executedSql = extras.executedSql;
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

const FALLBACK_MESSAGES: Record<ServerErrorCode, string> = {
  validation: "The request was rejected as invalid.",
  not_found: "That item no longer exists.",
  conflict: "That conflicts with an existing item.",
  unauthorized: "This session is not authorized.",
  forbidden: "The dbscope agent refused this request.",
  secrets_locked: "The secrets vault is locked.",
  upstream: "The database could not be reached or refused the request.",
  timeout: "The database did not answer within the statement timeout.",
  internal: "The dbscope agent hit an internal error.",
  rejected: "dbscope's safety check refused this statement.",
  cost_confirmation_required: "This query is estimated to be expensive and needs confirming.",
  database: "PostgreSQL rejected the statement.",
  recovery_conflict: "The replica removed rows this query still needed.",
  llm_not_configured: "Choose an LLM provider in the assistant settings first.",
  llm: "The LLM provider failed to answer.",
};

const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);

/** Builds an ApiError from an envelope's inner `error` object (also the data of an SSE `error` event). */
export function errorFromBody(error: unknown, status: number): ApiError {
  if (!isRecord(error) || typeof error.code !== "string") {
    return new ApiError("malformed_response", `Unexpected response from the dbscope agent (HTTP ${status}).`, status);
  }
  const code = SERVER_CODES.find((c) => c === error.code) ?? "internal";
  const message = str(error.message) ?? FALLBACK_MESSAGES[code];
  return new ApiError(code, message, status, str(error.detail), {
    sqlstate: str(error.sqlstate),
    position: num(error.position),
    estimatedCost: num(error.estimatedCost),
    estimatedRows: num(error.estimatedRows),
    executedSql: str(error.executedSql),
  });
}

/** Builds an ApiError from a parsed `{ error: { code, message, detail, ... } }` body. */
export function errorFromEnvelope(body: unknown, status: number): ApiError {
  return errorFromBody(isRecord(body) ? body.error : undefined, status);
}

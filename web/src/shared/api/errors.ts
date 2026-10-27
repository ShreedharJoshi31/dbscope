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
  | "internal";
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
];

export class ApiError extends Error {
  override readonly name = "ApiError";
  readonly code: ApiErrorCode;
  readonly status: number;
  readonly detail: string | undefined;

  constructor(code: ApiErrorCode, message: string, status: number, detail?: string) {
    super(message);
    this.code = code;
    this.status = status;
    this.detail = detail;
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
};

/** Builds an ApiError from a parsed `{ error: { code, message, detail } }` body. */
export function errorFromEnvelope(body: unknown, status: number): ApiError {
  const error = isRecord(body) ? body.error : undefined;
  if (!isRecord(error) || typeof error.code !== "string") {
    return new ApiError("malformed_response", `Unexpected response from the dbscope agent (HTTP ${status}).`, status);
  }
  const code = SERVER_CODES.find((c) => c === error.code) ?? "internal";
  const message = typeof error.message === "string" && error.message ? error.message : FALLBACK_MESSAGES[code];
  const detail = typeof error.detail === "string" ? error.detail : undefined;
  return new ApiError(code, message, status, detail);
}

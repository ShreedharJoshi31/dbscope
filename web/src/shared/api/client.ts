import { ApiError, errorFromEnvelope } from "@/shared/api/errors";
import { getToken } from "@/shared/api/token";

export const TOKEN_HEADER = "X-Dbscope-Token";

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "DELETE";
  body?: unknown;
  signal?: AbortSignal | undefined;
  headers?: Record<string, string>;
}

/** Sends an authenticated request to the local agent. Errors throw ApiError. */
export async function request(path: string, options: RequestOptions = {}): Promise<Response> {
  const headers = new Headers(options.headers);
  const token = getToken();
  if (token) headers.set(TOKEN_HEADER, token);
  if (options.body !== undefined) headers.set("Content-Type", "application/json");
  if (!headers.has("Accept")) headers.set("Accept", "application/json");

  let response: Response;
  try {
    response = await fetch(path, {
      method: options.method ?? "GET",
      headers,
      body: options.body === undefined ? null : JSON.stringify(options.body),
      signal: options.signal ?? null,
      credentials: "omit",
    });
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
    throw new ApiError("network", "Could not reach the dbscope agent. Is `dbscope ui` still running?", 0);
  }

  if (!response.ok) {
    const body: unknown = await response.json().catch(() => undefined);
    throw errorFromEnvelope(body, response.status);
  }
  return response;
}

/** JSON request; resolves to undefined for 204 No Content. */
export async function apiFetch<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const response = await request(path, options);
  if (response.status === 204) return undefined as T;
  try {
    return (await response.json()) as T;
  } catch {
    throw new ApiError(
      "malformed_response",
      "The dbscope agent returned a response that is not JSON.",
      response.status,
    );
  }
}

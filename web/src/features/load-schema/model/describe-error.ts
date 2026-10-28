import { errorCopy } from "@/entities/connection";
import type { ApiError, Engine, TestErrorKind } from "@/shared/api";

const KINDS: readonly TestErrorKind[] = ["auth", "network", "tls", "timeout", "database", "other"];

export interface SchemaErrorCopy {
  title: string;
  /** What to do next; the same hints the connection test shows for upstream failures. */
  hint: string;
  /** The server's own words, when they add something. */
  message: string | undefined;
}

/** Turns an introspection failure into a named cause and a hint (UX rule 10). */
export function describeSchemaError(error: ApiError, statementTimeoutMs?: number, engine?: Engine): SchemaErrorCopy {
  if (error.code === "upstream") {
    const kind = KINDS.find((k) => k === error.detail) ?? "other";
    const copy = errorCopy(kind, engine ?? "unknown");
    return { title: copy.title, hint: copy.hint, message: error.message };
  }
  if (error.code === "timeout") {
    const limit = statementTimeoutMs ? ` (${Math.round(statementTimeoutMs / 1000)} s)` : "";
    return {
      title: "Reading the schema timed out",
      hint: `The catalog queries ran past this connection's statement timeout${limit}. Select fewer schemas, or raise the timeout under the connection's advanced settings.`,
      message: error.message,
    };
  }
  if (error.code === "secrets_locked") {
    return {
      title: "The secrets vault is locked",
      hint: "Unlock it to let dbscope use this connection's stored password, then try again.",
      message: undefined,
    };
  }
  if (error.code === "network") {
    return { title: "The dbscope agent is not answering", hint: error.message, message: undefined };
  }
  return {
    title: "Could not read the schema",
    hint: "Try again. If it keeps failing, check the agent's log.",
    message: error.message,
  };
}

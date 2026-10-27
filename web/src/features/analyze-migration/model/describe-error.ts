import { ERROR_KIND_COPY } from "@/entities/connection";
import { formatBytes, type ReviewError } from "@/entities/migration-finding";
import type { TestErrorKind } from "@/shared/api";

const KINDS: readonly TestErrorKind[] = ["auth", "network", "tls", "timeout", "database", "other"];

export type ReviewErrorAction = "retry" | "unlock" | "pick" | "connect";

export interface ReviewErrorCopy {
  title: string;
  hint: string;
  /** The server's own words, when they add something. */
  message: string | undefined;
  /** File names or sizes the problem is about. */
  details: string[];
  /** What the error view offers, primary first. */
  actions: ReviewErrorAction[];
}

const MAX_DETAILS = 8;

function capped(list: readonly string[]): string[] {
  if (list.length <= MAX_DETAILS) return [...list];
  return [...list.slice(0, MAX_DETAILS), `and ${list.length - MAX_DETAILS} more`];
}

/** Turns a failed analysis into a named cause and what to do next (UX rule 10). */
export function describeReviewError(error: ReviewError, statementTimeoutMs?: number): ReviewErrorCopy {
  if (error.kind === "client") {
    const p = error.problem;
    switch (p.code) {
      case "no_files":
        return {
          title: "No SQL migration files",
          hint: "Drop .sql files or a folder of migrations (Flyway, golang-migrate, Prisma, Atlas or plain SQL). Other files are not sent.",
          message: undefined,
          details: p.ignored.length > 0 ? capped(p.ignored.map((n) => `Ignored: ${n}`)) : [],
          actions: ["pick"],
        };
      case "too_many":
        return {
          title: `Too many files: ${p.count}`,
          hint: `dbscope analyzes up to ${p.limit} files at a time. Drop a smaller folder, or use \`dbscope migrate check\` in CI for the full history.`,
          message: undefined,
          details: [],
          actions: ["pick"],
        };
      case "too_large":
        return {
          title: p.files.length === 1 ? "A file is larger than 1 MiB" : `${p.files.length} files are larger than 1 MiB`,
          hint: `Each migration file can be at most ${formatBytes(p.limit)}. Large data loads are better reviewed with \`dbscope migrate check\`.`,
          message: undefined,
          details: capped(p.files.map((f) => `${f.name}: ${formatBytes(f.bytes)}`)),
          actions: ["pick"],
        };
      case "body_too_large":
        return {
          title: "The files are too large together",
          hint: `All files together can be at most ${formatBytes(p.limit)}; these are ${formatBytes(p.bytes)}.`,
          message: undefined,
          details: [],
          actions: ["pick"],
        };
      case "bad_name":
        return {
          title: "A file name cannot be sent",
          hint: "File names must be 1 to 512 characters.",
          message: undefined,
          details: capped(p.names),
          actions: ["pick"],
        };
      case "read_failed":
        return {
          title: "Could not read the files",
          hint: "The browser could not read them. Check that they still exist and are readable, then drop them again.",
          message: undefined,
          details: capped(p.names),
          actions: ["pick"],
        };
      case "no_target":
        return {
          title: "No schema to analyze against",
          hint: "Migrations are checked against a schema: add a connection, or open the sample schema.",
          message: undefined,
          details: [],
          actions: ["connect"],
        };
      case "cancelled":
        return {
          title: "Analysis cancelled",
          hint: "Nothing was analyzed.",
          message: undefined,
          details: [],
          actions: ["retry"],
        };
    }
  }

  const e = error.error;
  switch (e.code) {
    case "validation":
      return {
        title: e.status === 413 ? "The files are too large together" : "The agent rejected the files",
        hint: "dbscope analyzes 1 to 200 files of at most 1 MiB each.",
        message: e.message,
        details: [],
        actions: ["pick"],
      };
    case "secrets_locked":
      return {
        title: "The secrets vault is locked",
        hint: "Analysis reads the live schema with this connection's stored password. Unlock the vault, then try again.",
        message: undefined,
        details: [],
        actions: ["unlock", "retry"],
      };
    case "upstream": {
      const kind = KINDS.find((k) => k === e.detail) ?? "other";
      const copy = ERROR_KIND_COPY[kind];
      return {
        title: `Could not read the base schema: ${copy.title.charAt(0).toLowerCase()}${copy.title.slice(1)}`,
        hint: copy.hint,
        message: e.message,
        details: [],
        actions: ["retry"],
      };
    }
    case "timeout": {
      const limit = statementTimeoutMs ? ` (${Math.round(statementTimeoutMs / 1000)} s)` : "";
      return {
        title: "Reading the base schema timed out",
        hint: `The catalog, size and dependency queries ran past this connection's statement timeout${limit}. Select fewer schemas, or raise the timeout under the connection's advanced settings.`,
        message: e.message,
        details: [],
        actions: ["retry"],
      };
    }
    case "not_found":
      return {
        title: "The connection no longer exists",
        hint: "It may have been deleted in another window. Choose another connection and drop the files again.",
        message: undefined,
        details: [],
        actions: ["connect"],
      };
    case "network":
      return {
        title: "The dbscope agent is not answering",
        hint: e.message,
        message: undefined,
        details: [],
        actions: ["retry"],
      };
    default:
      return {
        title: "The analysis failed",
        hint: "Try again. If it keeps failing, check the agent's log.",
        message: e.message,
        details: [],
        actions: ["retry"],
      };
  }
}

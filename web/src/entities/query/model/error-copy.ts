import { positionToRange } from "@/entities/query/model/position";
import type { ApiError, GuardReason } from "@/shared/api";

/** Where an error's `position` points: the SQL the user sent, or the statement dbscope actually ran. */
export type PositionTarget = "input" | "executed";

export interface QueryErrorCopy {
  title: string;
  /** The server's sentence, shown as is. */
  message: string;
  /** What to do about it, from docs/QUERY-SAFETY.md "Errors shown to users". */
  guidance?: string;
  sqlstate?: string | undefined;
  position?: { value: number; target: PositionTarget } | undefined;
  /** Safe to run again as is (recovery conflict, transaction timeout). */
  retryable: boolean;
}

const GUARD_TITLES: Record<GuardReason, string> = {
  multiple_statements: "Only one statement can run at a time",
  not_select: "Only read-only queries can run here",
  writes_data: "Blocked: this statement would change data",
  locking_clause: "Row locks are not allowed",
  select_into: "SELECT INTO would create a table",
  function_not_allowed: "This calls a function dbscope does not allow",
  syntax: "The SQL has a syntax error",
};

const GUARD_GUIDANCE: Partial<Record<GuardReason, string>> = {
  multiple_statements: "Run each statement on its own.",
  not_select: "dbscope runs SELECT, VALUES, TABLE and WITH … SELECT statements, always read-only.",
  locking_clause: "Remove FOR UPDATE / FOR SHARE; reads in dbscope never lock rows.",
  function_not_allowed:
    "Functions that can change state or read files are blocked, as are user-defined functions that are not part of an extension.",
  syntax: "The highlighted position in the editor shows where PostgreSQL's parser stopped.",
};

const UPSTREAM_TITLES: Record<string, string> = {
  auth: "The database rejected the credentials",
  network: "Could not reach the database",
  tls: "The TLS connection failed",
  timeout: "The connection timed out",
  database: "The database ended the session",
};

function isGuardReason(value: string | undefined): value is GuardReason {
  return value !== undefined && value in GUARD_TITLES;
}

/** Plain-language copy for a failed /query, /explain or assistant run. */
export function describeQueryError(error: ApiError, statementTimeoutMs?: number): QueryErrorCopy {
  const base = { message: error.message, sqlstate: error.sqlstate, retryable: false };
  const at = (target: PositionTarget) => (error.position ? { value: error.position, target } : undefined);
  switch (error.code) {
    case "rejected": {
      const reason = isGuardReason(error.detail) ? error.detail : undefined;
      return {
        ...base,
        title: reason ? GUARD_TITLES[reason] : "dbscope's safety check refused this statement",
        guidance: reason ? GUARD_GUIDANCE[reason] : undefined,
        position: at("input"),
      };
    }
    case "database":
      if (error.sqlstate === "25006") {
        return { ...base, title: "Blocked: this would modify data", position: at("executed") };
      }
      return { ...base, title: "PostgreSQL rejected the statement", position: at("executed") };
    case "timeout": {
      if (error.sqlstate === "25P03" || error.sqlstate === "25P04") {
        return {
          ...base,
          title: "The session timed out",
          guidance: "Run the query again; dbscope reconnects.",
          retryable: true,
        };
      }
      const limit = statementTimeoutMs ? ` (${(statementTimeoutMs / 1000).toLocaleString("en")} s)` : "";
      return {
        ...base,
        title: "Stopped after the profile's statement timeout",
        guidance: `The query ran longer than this connection's statement timeout${limit}. Narrow it with a WHERE clause or LIMIT, or raise the timeout in the connection's advanced settings.`,
      };
    }
    case "recovery_conflict":
      return {
        ...base,
        title: "The replica removed rows this query needed",
        guidance:
          "This is a conflict with recovery on a hot standby. It is safe to run the query once more; if it keeps happening, ask your DBA about hot_standby_feedback.",
        retryable: true,
      };
    case "upstream":
      if (error.sqlstate === "57P04") {
        return { ...base, title: "The database was dropped or moved", guidance: "Do not retry; check the connection." };
      }
      return {
        ...base,
        title: UPSTREAM_TITLES[error.detail ?? ""] ?? "The database connection failed",
        guidance: "Check the connection in the connection settings, then run the query again.",
      };
    case "secrets_locked":
      return {
        ...base,
        title: "The secrets vault is locked",
        guidance: "Unlock it to use this connection's password.",
      };
    case "network":
      return { ...base, title: "Could not reach the dbscope agent" };
    case "not_found":
      return { ...base, title: "This connection no longer exists" };
    case "validation":
      return { ...base, title: "The request was not valid" };
    default:
      return { ...base, title: "The query failed" };
  }
}

/** Short status line for the editor and history ("Cancelled", "Rejected", ...). */
export function errorHeadline(error: ApiError): string {
  return describeQueryError(error).title;
}

export interface EditorDiagnostic {
  from: number;
  to: number;
  message: string;
}

/**
 * Where a failed run's error belongs in the editor text, or null. Guard
 * (422 syntax) positions point into the submitted SQL. Database positions
 * point into executedSql and only line up when it equals the editor text.
 */
export function editorDiagnostic(
  error: ApiError,
  submittedSql: string,
  editorSql: string,
  executedSql?: string,
): EditorDiagnostic | null {
  if (editorSql !== submittedSql) return null; // edited since: the offset no longer applies
  const copy = describeQueryError(error);
  if (!copy.position) return null;
  if (copy.position.target === "executed" && executedSql !== submittedSql) return null;
  const range = positionToRange(submittedSql, copy.position.value);
  if (!range) return null;
  return { from: range.from, to: Math.max(range.to, range.from + 1), message: `${copy.title}: ${error.message}` };
}

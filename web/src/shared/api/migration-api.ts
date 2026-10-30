import { apiFetch } from "@/shared/api/client";
import type { AnalyzeFile, AnalyzeReport, AnalyzeRequest } from "@/shared/api/migration-types";

/** Limits the agent enforces (internal/agent/migrate_handlers.go); checked client-side first. */
export const MIGRATION_LIMITS = {
  files: 200,
  fileBytes: 1 << 20,
  nameLength: 512,
  /** Request body limit; JSON escaping makes the body larger than the files. */
  bodyBytes: 32 << 20,
} as const;

/** Where to analyze: against a connection's live schema, or the built-in sample. */
export type MigrationTarget = { kind: "profile"; profileId: string } | { kind: "sample" };

export function analyzePath(target: MigrationTarget): string {
  return target.kind === "sample"
    ? "/api/sample/migrations/analyze"
    : `/api/profiles/${encodeURIComponent(target.profileId)}/migrations/analyze`;
}

/** POST …/migrations/analyze. Deterministic, no LLM; aborting stops reading the base schema. */
export function analyzeMigrations(
  target: MigrationTarget,
  body: AnalyzeRequest,
  signal?: AbortSignal,
): Promise<AnalyzeReport> {
  return apiFetch<AnalyzeReport>(analyzePath(target), { method: "POST", body, signal });
}

export interface FolderAnalyzeRequest {
  /** Leave out the files the migration tool's history shows applied. */
  pendingOnly: boolean;
  txMode?: AnalyzeRequest["txMode"];
  assumeLockTimeout?: boolean;
}

/** The report plus the files read from the folder (only those analyzed), so the review can show them. */
export interface FolderAnalyzeResponse {
  report: AnalyzeReport;
  sources: AnalyzeFile[];
}

/** POST …/migrations/analyze-folder: reads the profile's migrations folder on the agent's machine. */
export function analyzeMigrationFolder(
  profileId: string,
  body: FolderAnalyzeRequest,
  signal?: AbortSignal,
): Promise<FolderAnalyzeResponse> {
  return apiFetch<FolderAnalyzeResponse>(`/api/profiles/${encodeURIComponent(profileId)}/migrations/analyze-folder`, {
    method: "POST",
    body,
    signal,
  });
}

/** GET …/migrations/folder-state: a fingerprint of the folder's migration files, for watch mode. */
export function migrationFolderState(
  profileId: string,
  signal?: AbortSignal,
): Promise<{ fingerprint: string; files: number }> {
  return apiFetch(`/api/profiles/${encodeURIComponent(profileId)}/migrations/folder-state`, { signal });
}

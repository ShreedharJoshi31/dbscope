import { apiFetch } from "@/shared/api/client";
import type { AnalyzeReport, AnalyzeRequest } from "@/shared/api/migration-types";

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

import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/shared/api/client";
import type { ApiError } from "@/shared/api/errors";

/** Per-table activity (internal/workload); counters are cumulative since their reset, not rates. */
export interface WorkloadTable {
  seqScans: number;
  idxScans: number;
  rowsWritten: number;
  liveRows: number;
  /** Statements that use the table; one using three tables counts in each. */
  queryCalls: number;
  queryTimeMs: number;
  columns?: Record<string, { queries: number; calls: number }>;
  wildcardCalls?: number;
  topQueries?: { queryId: string; text: string; calls: number; totalMs: number; meanMs: number }[];
}

export interface WorkloadSnapshot {
  generatedAt: string;
  /** A replica: counters cover only what ran on it, and writes are never counted. */
  inRecovery: boolean;
  tableStatsReset?: string;
  statements: {
    status: "ok" | "not_installed" | "not_loaded" | "unavailable";
    message?: string;
    since?: string;
    dealloc: number;
    totalCalls: number;
    totalTimeMs: number;
    hiddenCalls: number;
    unattributedCalls: number;
    /** Calls of statements beyond the ones read (most time first). */
    unreadCalls: number;
    read: number;
  };
  tables: Record<string, WorkloadTable>;
}

export const workloadQueryKey = (profileId: string) => ["profiles", profileId, "workload"] as const;

/** GET /workload; read on demand and kept a minute, since it scans pg_stat_statements. */
export function useWorkload(profileId: string | undefined, enabled: boolean) {
  return useQuery<WorkloadSnapshot, ApiError>({
    queryKey: workloadQueryKey(profileId ?? ""),
    queryFn: ({ signal }) =>
      apiFetch<WorkloadSnapshot>(`/api/profiles/${encodeURIComponent(profileId ?? "")}/workload`, { signal }),
    enabled: enabled && Boolean(profileId),
    staleTime: 60_000,
    retry: false,
  });
}

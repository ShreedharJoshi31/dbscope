import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/shared/api/client";
import type { ApiError } from "@/shared/api/errors";

/** A group of Redis keys whose names differ only in id-like segments (internal/redis). */
export interface KeyPattern {
  pattern: string;
  keys: number;
  types: Record<string, number>;
  bytes: number;
  noTtl: number;
  largest?: KeyInfo;
}

export interface KeyInfo {
  name: string;
  type: string;
  bytes: number;
  elements: number;
  /** -1 without expiry. */
  ttlMs: number;
}

export interface KeyspaceFinding {
  ruleId: string;
  rating: "low" | "medium" | "high" | "critical";
  message: string;
  safeAlternative?: string;
}

export interface KeyspaceReport {
  server: { flavor: "redis" | "valkey"; version: string; versionNum: number; mode: string; role: string };
  memory: { used: number; rss: number; max: number; policy: string; fragmentation: number; fragBytes: number };
  dbKeys: number;
  dbExpire: number;
  scanned: number;
  /** False when the scan stopped at its key or time budget. */
  complete: boolean;
  patterns: KeyPattern[];
  bigKeys: KeyInfo[];
  findings: KeyspaceFinding[];
  warnings: string[];
  tookMs: number;
}

export function useKeyspace(profileId: string | undefined, enabled: boolean) {
  return useQuery<KeyspaceReport, ApiError>({
    queryKey: ["keyspace", profileId ?? ""],
    queryFn: ({ signal }) =>
      apiFetch<KeyspaceReport>(`/api/profiles/${encodeURIComponent(profileId ?? "")}/keyspace`, { signal }),
    enabled: enabled && Boolean(profileId),
    staleTime: 30_000,
    retry: false,
  });
}

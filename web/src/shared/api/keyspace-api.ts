import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/shared/api/client";
import type { ApiError } from "@/shared/api/errors";

/** A group of Redis keys whose names differ only in id-like segments (internal/redis). */
export interface KeyPattern {
  pattern: string;
  keys: number;
  types: Record<string, number>;
  /** MEMORY USAGE of the scanned keys, summed. */
  bytes: number;
  /** bytes scaled up to the whole pattern when the scan was partial. */
  estimatedBytes: number;
  noTtl: number;
  largest?: KeyInfo;
  /** Some key's bytes are MEMORY USAGE's sampled estimate. */
  bytesApprox?: boolean;
}

export interface KeyInfo {
  name: string;
  type: string;
  bytes: number;
  elements: number;
  /** MEMORY USAGE sampled a large collection rather than counting every element. */
  bytesApprox?: boolean;
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
  /** Share of the keyspace scanned, 0..1. */
  sampleFraction: number;
  /** MEMORY USAGE could be run (an ACL may refuse it). */
  memorySampled: boolean;
  bigKeys: KeyInfo[];
  largestByMemory: KeyInfo[];
  largestByElements: KeyInfo[];
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

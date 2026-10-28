import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/shared/api/client";
import type { ApiError } from "@/shared/api/errors";

export type ServerHealthStatus = "ok" | "warn" | "danger" | "unknown";

/** One health signal of a server (internal/health). */
export interface ServerHealthCheck {
  id: string;
  title: string;
  status: ServerHealthStatus;
  value: string;
  detail?: string;
  fix?: string;
}

export interface ServerHealthReport {
  engine: string;
  server: string;
  checks: ServerHealthCheck[];
  tookMs: number;
}

export function useServerHealth(profileId: string | undefined, enabled: boolean) {
  return useQuery<ServerHealthReport, ApiError>({
    queryKey: ["server-health", profileId ?? ""],
    queryFn: ({ signal }) =>
      apiFetch<ServerHealthReport>(`/api/profiles/${encodeURIComponent(profileId ?? "")}/health`, { signal }),
    enabled: enabled && Boolean(profileId),
    staleTime: 15_000,
    retry: false,
  });
}

import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/shared/api/client";
import type { ApiError } from "@/shared/api/errors";
import type {
  ExplainRequest,
  ExplainResult,
  GraphExpansion,
  HistoryEntry,
  QueryRequest,
  QueryResult,
  TimeTravelBounds,
} from "@/shared/api/query-types";

const profilePath = (id: string) => `/api/profiles/${encodeURIComponent(id)}`;

export const queryApiKeys = {
  history: (profileId: string) => ["profiles", profileId, "history"] as const,
  timeTravel: (profileId: string, tables: readonly string[]) =>
    ["profiles", profileId, "time-travel", tables.join(",")] as const,
};

/** POST /query. Aborting the signal makes the agent cancel the statement server-side. */
export function runQuery(profileId: string, body: QueryRequest, signal?: AbortSignal): Promise<QueryResult> {
  return apiFetch<QueryResult>(`${profilePath(profileId)}/query`, { method: "POST", body, signal });
}

/** POST /explain: plan and overlay only, no rows. The cost gate applies only with `analyze`. */
export function explainQuery(profileId: string, body: ExplainRequest, signal?: AbortSignal): Promise<ExplainResult> {
  return apiFetch<ExplainResult>(`${profilePath(profileId)}/explain`, { method: "POST", body, signal });
}

/** POST /graph/expand: up to `limit` (1..200) neighbours of a node, read with fixed Cypher; `graph` names an AGE graph. */
export function expandGraphNode(
  profileId: string,
  nodeId: string,
  limit: number,
  graph?: string,
): Promise<GraphExpansion> {
  return apiFetch<GraphExpansion>(`${profilePath(profileId)}/graph/expand`, {
    method: "POST",
    body: graph ? { nodeId, limit, graph } : { nodeId, limit },
  });
}

export const HISTORY_LIMIT = 50;

/** GET /time-travel: each table's oldest readable time against the database server's clock. */
export function fetchTimeTravelBounds(
  profileId: string,
  tables: readonly string[],
  signal?: AbortSignal,
): Promise<TimeTravelBounds> {
  const query = new URLSearchParams({ tables: tables.join(",") });
  return apiFetch<TimeTravelBounds>(`${profilePath(profileId)}/time-travel?${query}`, { signal });
}

/** Bounds move with the server's clock, so they are read again each time `enabled` turns on (the picker opens). */
export function useTimeTravelBounds(profileId: string | undefined, tables: readonly string[], enabled: boolean) {
  return useQuery<TimeTravelBounds, ApiError>({
    queryKey: queryApiKeys.timeTravel(profileId ?? "", tables),
    queryFn: ({ signal }) => fetchTimeTravelBounds(profileId ?? "", tables, signal),
    enabled: enabled && Boolean(profileId) && tables.length > 0,
    staleTime: 0,
    gcTime: 0,
    retry: false,
  });
}

export function useQueryHistory(profileId: string | undefined) {
  return useQuery<HistoryEntry[], ApiError>({
    queryKey: queryApiKeys.history(profileId ?? ""),
    queryFn: ({ signal }) =>
      apiFetch<HistoryEntry[]>(`${profilePath(profileId ?? "")}/history?limit=${HISTORY_LIMIT}`, { signal }),
    enabled: Boolean(profileId),
    retry: false,
  });
}

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
} from "@/shared/api/query-types";

const profilePath = (id: string) => `/api/profiles/${encodeURIComponent(id)}`;

export const queryApiKeys = {
  history: (profileId: string) => ["profiles", profileId, "history"] as const,
};

/** POST /query. Aborting the signal makes the agent cancel the statement server-side. */
export function runQuery(profileId: string, body: QueryRequest, signal?: AbortSignal): Promise<QueryResult> {
  return apiFetch<QueryResult>(`${profilePath(profileId)}/query`, { method: "POST", body, signal });
}

/** POST /explain: plan and overlay only, no rows. The cost gate applies only with `analyze`. */
export function explainQuery(profileId: string, body: ExplainRequest, signal?: AbortSignal): Promise<ExplainResult> {
  return apiFetch<ExplainResult>(`${profilePath(profileId)}/explain`, { method: "POST", body, signal });
}

/** POST /graph/expand: up to `limit` (1..200) neighbours of a node, read with fixed Cypher. */
export function expandGraphNode(profileId: string, nodeId: string, limit: number): Promise<GraphExpansion> {
  return apiFetch<GraphExpansion>(`${profilePath(profileId)}/graph/expand`, {
    method: "POST",
    body: { nodeId, limit },
  });
}

export const HISTORY_LIMIT = 50;

export function useQueryHistory(profileId: string | undefined) {
  return useQuery<HistoryEntry[], ApiError>({
    queryKey: queryApiKeys.history(profileId ?? ""),
    queryFn: ({ signal }) =>
      apiFetch<HistoryEntry[]>(`${profilePath(profileId ?? "")}/history?limit=${HISTORY_LIMIT}`, { signal }),
    enabled: Boolean(profileId),
    retry: false,
  });
}

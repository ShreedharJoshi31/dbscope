import { useMutation, useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/shared/api/client";
import { type ApiError, isApiError } from "@/shared/api/errors";
import type { CanvasLayout, CanvasLayoutInput, InferredFkDecision, Model, Namespace } from "@/shared/api/schema-types";

const profilePath = (id: string) => `/api/profiles/${encodeURIComponent(id)}`;

export const schemaQueryKeys = {
  namespaces: (profileId: string) => ["profiles", profileId, "namespaces"] as const,
  schema: (profileId: string) => ["profiles", profileId, "schema"] as const,
  layout: (profileId: string, key: string) => ["profiles", profileId, "layout", key] as const,
  sample: ["sample", "schema"] as const,
};

export function useNamespaces(profileId: string | undefined, enabled = true) {
  return useQuery<Namespace[], ApiError>({
    queryKey: schemaQueryKeys.namespaces(profileId ?? ""),
    queryFn: ({ signal }) => apiFetch<Namespace[]>(`${profilePath(profileId ?? "")}/namespaces`, { signal }),
    enabled: Boolean(profileId) && enabled,
    retry: false,
  });
}

/**
 * Introspects the profile's schemas. Introspection reads the catalog and can
 * take seconds, so the result is kept until the user refreshes; errors are
 * answers (upstream/timeout) and are not retried.
 */
export function useProfileSchema(profileId: string | undefined) {
  return useQuery<Model, ApiError>({
    queryKey: schemaQueryKeys.schema(profileId ?? ""),
    queryFn: ({ signal }) => apiFetch<Model>(`${profilePath(profileId ?? "")}/schema`, { signal }),
    enabled: Boolean(profileId),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
}

export function useSampleSchema(enabled: boolean) {
  return useQuery<Model, ApiError>({
    queryKey: schemaQueryKeys.sample,
    queryFn: ({ signal }) => apiFetch<Model>("/api/sample/schema", { signal }),
    enabled,
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
}

export function putInferredFkDecision(profileId: string, decision: InferredFkDecision): Promise<void> {
  return apiFetch<void>(`${profilePath(profileId)}/inferred-fks`, { method: "PUT", body: decision });
}

export function useSetInferredFk() {
  return useMutation<void, ApiError, { profileId: string; decision: InferredFkDecision }>({
    mutationFn: ({ profileId, decision }) => putInferredFkDecision(profileId, decision),
  });
}

/** The saved canvas layout, or null when none is saved under this key (404). */
export async function fetchLayout(profileId: string, key: string, signal?: AbortSignal): Promise<CanvasLayout | null> {
  try {
    return await apiFetch<CanvasLayout>(`${profilePath(profileId)}/layout?key=${encodeURIComponent(key)}`, { signal });
  } catch (error) {
    if (isApiError(error) && error.code === "not_found") return null;
    throw error;
  }
}

export function useCanvasLayout(profileId: string | undefined, key: string | undefined) {
  return useQuery<CanvasLayout | null, ApiError>({
    queryKey: schemaQueryKeys.layout(profileId ?? "", key ?? ""),
    queryFn: ({ signal }) => fetchLayout(profileId ?? "", key ?? "", signal),
    enabled: Boolean(profileId && key),
    // The canvas owns positions after the first read; refetching would fight the user's drags.
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 0,
    retry: false,
  });
}

export function putLayout(profileId: string, layout: CanvasLayoutInput): Promise<void> {
  return apiFetch<void>(`${profilePath(profileId)}/layout`, { method: "PUT", body: layout });
}

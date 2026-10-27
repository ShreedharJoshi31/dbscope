import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/shared/api/client";
import type { ApiError } from "@/shared/api/errors";
import type { Health, ParsedConnectionUrl, Profile, ProfileInput, SecretsStatus, TestResult } from "@/shared/api/types";

export const queryKeys = {
  health: ["health"] as const,
  profiles: ["profiles"] as const,
  profileTest: (id: string) => ["profiles", id, "test"] as const,
  secretsStatus: ["secrets", "status"] as const,
};

export function useHealth() {
  return useQuery<Health, ApiError>({
    queryKey: queryKeys.health,
    queryFn: ({ signal }) => apiFetch<Health>("/api/health", { signal }),
    staleTime: Number.POSITIVE_INFINITY,
  });
}

export function useProfiles() {
  return useQuery<Profile[], ApiError>({
    queryKey: queryKeys.profiles,
    queryFn: ({ signal }) => apiFetch<Profile[]>("/api/profiles", { signal }),
  });
}

export function useCreateProfile() {
  const client = useQueryClient();
  return useMutation<Profile, ApiError, ProfileInput>({
    mutationFn: (input) => apiFetch<Profile>("/api/profiles", { method: "POST", body: input }),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.profiles }),
  });
}

export function useUpdateProfile() {
  const client = useQueryClient();
  return useMutation<Profile, ApiError, { id: string; input: ProfileInput }>({
    mutationFn: ({ id, input }) =>
      apiFetch<Profile>(`/api/profiles/${encodeURIComponent(id)}`, { method: "PUT", body: input }),
    onSuccess: (profile) => {
      client.removeQueries({ queryKey: queryKeys.profileTest(profile.id) });
      return client.invalidateQueries({ queryKey: queryKeys.profiles });
    },
  });
}

export function useDeleteProfile() {
  const client = useQueryClient();
  return useMutation<void, ApiError, string>({
    mutationFn: (id) => apiFetch<void>(`/api/profiles/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: (_, id) => {
      client.removeQueries({ queryKey: queryKeys.profileTest(id) });
      return client.invalidateQueries({ queryKey: queryKeys.profiles });
    },
  });
}

export function useParseConnectionUrl() {
  return useMutation<ParsedConnectionUrl, ApiError, string>({
    mutationFn: (url) => apiFetch<ParsedConnectionUrl>("/api/connection-url/parse", { method: "POST", body: { url } }),
  });
}

export function useTestUnsavedProfile() {
  return useMutation<TestResult, ApiError, ProfileInput>({
    mutationFn: (input) => apiFetch<TestResult>("/api/profiles/test", { method: "POST", body: input }),
  });
}

/**
 * Tests a saved profile with its stored secret. Modelled as a query so the
 * status bar and explorer share one result per profile; `refetch` re-tests.
 */
export function useSavedProfileTest(id: string | undefined) {
  return useQuery<TestResult, ApiError>({
    queryKey: queryKeys.profileTest(id ?? ""),
    queryFn: ({ signal }) =>
      apiFetch<TestResult>(`/api/profiles/${encodeURIComponent(id ?? "")}/test`, { method: "POST", signal }),
    enabled: Boolean(id),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
}

/** Explicit re-test of a saved profile; refreshes the shared result used by useSavedProfileTest. */
export function useRetestSavedProfile() {
  const client = useQueryClient();
  return useMutation<TestResult, ApiError, string>({
    mutationFn: (id) => apiFetch<TestResult>(`/api/profiles/${encodeURIComponent(id)}/test`, { method: "POST" }),
    onSuccess: (result, id) => client.setQueryData(queryKeys.profileTest(id), result),
  });
}

export function useSecretsStatus() {
  return useQuery<SecretsStatus, ApiError>({
    queryKey: queryKeys.secretsStatus,
    queryFn: ({ signal }) => apiFetch<SecretsStatus>("/api/secrets/status", { signal }),
  });
}

export function useUnlockSecrets() {
  const client = useQueryClient();
  return useMutation<void, ApiError, string>({
    mutationFn: (passphrase) => apiFetch<void>("/api/secrets/unlock", { method: "POST", body: { passphrase } }),
    // Anything fetched while locked may have failed with 423 (schema, namespaces, tests), so refetch it all.
    onSuccess: () => client.invalidateQueries(),
  });
}

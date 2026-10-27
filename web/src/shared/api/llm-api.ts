import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch, request } from "@/shared/api/client";
import { ApiError } from "@/shared/api/errors";
import type {
  AskAnswer,
  AskConfirmation,
  AskContext,
  AskDone,
  AskEvent,
  AskRequest,
  AskStage,
  AskSummary,
  GlossaryEntry,
  LLMSettings,
  LLMSettingsInput,
  LLMTestResult,
} from "@/shared/api/llm-types";
import type { QueryResult } from "@/shared/api/query-types";
import { readSseStream, type SseEvent } from "@/shared/api/sse";

const profilePath = (id: string) => `/api/profiles/${encodeURIComponent(id)}`;

export const llmQueryKeys = {
  settings: ["llm", "settings"] as const,
  models: ["llm", "models"] as const,
  glossary: (profileId: string) => ["profiles", profileId, "glossary"] as const,
};

export function useLlmSettings() {
  return useQuery<LLMSettings, ApiError>({
    queryKey: llmQueryKeys.settings,
    queryFn: ({ signal }) => apiFetch<LLMSettings>("/api/llm/settings", { signal }),
    retry: false,
  });
}

export function useSaveLlmSettings() {
  const client = useQueryClient();
  return useMutation<LLMSettings, ApiError, LLMSettingsInput>({
    mutationFn: (input) => apiFetch<LLMSettings>("/api/llm/settings", { method: "PUT", body: input }),
    onSuccess: (saved) => {
      client.setQueryData(llmQueryKeys.settings, saved);
      client.removeQueries({ queryKey: llmQueryKeys.models });
    },
  });
}

/** One tiny structured-output call with the saved settings; never sends schema or data. */
export function useTestLlm() {
  return useMutation<LLMTestResult, ApiError, void>({
    mutationFn: () => apiFetch<LLMTestResult>("/api/llm/test", { method: "POST" }),
  });
}

/** Models of the saved provider: Anthropic's supported list, or an OpenAI-compatible `/v1/models`. */
export function fetchLlmModels(signal?: AbortSignal): Promise<string[]> {
  return apiFetch<string[]>("/api/llm/models", { signal });
}

export function useLlmModels(enabled: boolean) {
  return useQuery<string[], ApiError>({
    queryKey: llmQueryKeys.models,
    queryFn: ({ signal }) => fetchLlmModels(signal),
    enabled,
    retry: false,
    staleTime: 5 * 60_000,
  });
}

export function useGlossary(profileId: string | undefined) {
  return useQuery<GlossaryEntry[], ApiError>({
    queryKey: llmQueryKeys.glossary(profileId ?? ""),
    queryFn: async ({ signal }) =>
      (await apiFetch<GlossaryEntry[] | null>(`${profilePath(profileId ?? "")}/glossary`, { signal })) ?? [],
    enabled: Boolean(profileId),
    retry: false,
  });
}

export function useSaveGlossary() {
  const client = useQueryClient();
  return useMutation<void, ApiError, { profileId: string; entries: GlossaryEntry[] }>({
    mutationFn: ({ profileId, entries }) =>
      apiFetch<void>(`${profilePath(profileId)}/glossary`, { method: "PUT", body: entries }),
    onSuccess: (_, { profileId, entries }) => client.setQueryData(llmQueryKeys.glossary(profileId), entries),
  });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

/** Go marshals nil slices as null; the UI wants arrays. */
function normalizeAnswer(a: AskAnswer): AskAnswer {
  return {
    ...a,
    explanationSteps: a.explanationSteps ?? [],
    assumptions: a.assumptions ?? [],
    tables: a.tables ?? [],
  };
}

/** Turns one SSE frame into a typed event; unknown event names and unparsable data are skipped. */
export function parseAskEvent(frame: SseEvent): AskEvent | null {
  let data: unknown;
  try {
    data = JSON.parse(frame.data);
  } catch {
    return null;
  }
  if (!isRecord(data)) return null;
  switch (frame.event) {
    case "status":
      return typeof data.stage === "string" ? { type: "status", stage: data.stage as AskStage } : null;
    // The agent is the trusted local backend; payloads are typed as the contract says.
    case "context":
      return { type: "context", context: data as unknown as AskContext };
    case "answer":
      return { type: "answer", answer: normalizeAnswer(data as unknown as AskAnswer) };
    case "needs_confirmation":
      return { type: "needs_confirmation", confirmation: data as unknown as AskConfirmation };
    case "result":
      return { type: "result", result: data as unknown as QueryResult };
    case "summary":
      return { type: "summary", summary: data as unknown as AskSummary };
    case "done":
      return { type: "done", done: data as unknown as AskDone };
    case "error":
      return { type: "error", error: data };
    default:
      return null;
  }
}

/**
 * POST /ask as an event stream. Errors before the stream (400, 404, 409
 * llm_not_configured, 423) throw ApiError; aborting the signal cancels the
 * model call and any running statement on the agent and rejects with AbortError.
 */
export async function* askStream(
  profileId: string,
  body: AskRequest,
  signal?: AbortSignal,
): AsyncGenerator<AskEvent, void, undefined> {
  const response = await request(`${profilePath(profileId)}/ask`, {
    method: "POST",
    body,
    signal,
    headers: { Accept: "text/event-stream" },
  });
  if (!response.body) {
    throw new ApiError("malformed_response", "The dbscope agent sent an empty answer stream.", response.status);
  }
  for await (const frame of readSseStream(response.body, signal)) {
    const event = parseAskEvent(frame);
    if (event) yield event;
  }
}

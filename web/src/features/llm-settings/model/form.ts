import type { LLMEffort, LLMProvider, LLMSettings, LLMSettingsInput, LLMTestErrorKind } from "@/shared/api";

/** Mirrors llm.AnthropicModels; used until the saved provider is Anthropic and GET /api/llm/models can answer. */
export const ANTHROPIC_MODELS = ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"] as const;
export const ANTHROPIC_DEFAULT_MODEL = "claude-opus-5-5";
export const ANTHROPIC_DEFAULT_SUMMARY = "claude-haiku-5-5";
export const OLLAMA_BASE_URL = "http://localhost:11434/v1/";

export interface LlmFormValues {
  provider: LLMProvider;
  model: string;
  summaryModel: string;
  baseUrl: string;
  effort: LLMEffort;
  /** A new key the user typed. The saved key is never sent to the browser, so this starts empty. */
  apiKey: string;
}

export type LlmFormErrors = Partial<Record<keyof LlmFormValues, string>>;

export function formFromSettings(s: LLMSettings | undefined): LlmFormValues {
  return {
    provider: s?.provider ?? "none",
    model: s?.model ?? "",
    summaryModel: s?.summaryModel ?? "",
    baseUrl: s?.baseUrl ?? "",
    effort: s?.effort ?? "medium",
    apiKey: "",
  };
}

/** Switching provider resets the fields that belong to the other one. */
export function withProvider(values: LlmFormValues, provider: LLMProvider): LlmFormValues {
  if (provider === values.provider) return values;
  if (provider === "anthropic") {
    return {
      ...values,
      provider,
      model: ANTHROPIC_DEFAULT_MODEL,
      summaryModel: ANTHROPIC_DEFAULT_SUMMARY,
      baseUrl: "",
    };
  }
  if (provider === "openai_compat") {
    return { ...values, provider, model: "", summaryModel: "", baseUrl: values.baseUrl || OLLAMA_BASE_URL };
  }
  return { ...values, provider, apiKey: "" };
}

export function validBaseUrl(raw: string): boolean {
  try {
    const url = new URL(raw.trim());
    return (
      (url.protocol === "http:" || url.protocol === "https:") &&
      Boolean(url.host) &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash
    );
  } catch {
    return false;
  }
}

/** The agent validates too (internal/agent/nl_handlers.go); these catch the common cases before a round trip. */
export function validateLlmForm(v: LlmFormValues): LlmFormErrors {
  const errors: LlmFormErrors = {};
  if (v.provider === "openai_compat") {
    if (!v.model.trim()) errors.model = "Enter the model name, for example llama3.1:8b.";
    if (!validBaseUrl(v.baseUrl)) {
      errors.baseUrl = "Enter an http(s) URL without a user name or query, for example http://localhost:11434/v1/.";
    }
  }
  if (v.apiKey && /[\r\n]/.test(v.apiKey)) errors.apiKey = "The key must be a single line.";
  if (v.apiKey.length > 1000) errors.apiKey = "The key is longer than 1000 characters.";
  return errors;
}

/** The PUT body. `apiKey` is sent only when the user typed a new one, so the stored key is kept otherwise. */
export function toLlmInput(v: LlmFormValues): LLMSettingsInput {
  if (v.provider === "none") return { provider: "none", model: "", summaryModel: "" };
  const input: LLMSettingsInput = { provider: v.provider, model: v.model.trim(), summaryModel: v.summaryModel.trim() };
  if (v.provider === "anthropic") input.effort = v.effort;
  if (v.provider === "openai_compat") input.baseUrl = v.baseUrl.trim();
  if (v.apiKey) input.apiKey = v.apiKey;
  return input;
}

/** The saved settings as a PUT body, e.g. to remove the key without touching anything else. */
export function inputFromSettings(s: LLMSettings): LLMSettingsInput {
  return toLlmInput({ ...formFromSettings(s), apiKey: "" });
}

/** True when the form differs from what is saved (a typed key counts). */
export function isDirty(v: LlmFormValues, saved: LLMSettings | undefined): boolean {
  const base = formFromSettings(saved);
  const fields: (keyof LlmFormValues)[] =
    v.provider === "anthropic"
      ? ["provider", "model", "summaryModel", "effort"]
      : v.provider === "openai_compat"
        ? ["provider", "model", "summaryModel", "baseUrl"]
        : ["provider"];
  return Boolean(v.apiKey) || fields.some((f) => v[f].trim() !== base[f].trim());
}

const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);

export function isLocalEndpoint(baseUrl: string): boolean {
  try {
    return LOCAL_HOSTS.has(new URL(baseUrl).hostname);
  } catch {
    return false;
  }
}

/** Where questions go, in words: "Anthropic (api.anthropic.com)" or "http://localhost:11434/v1/ on this machine". */
export function destination(v: Pick<LlmFormValues, "provider" | "baseUrl">): string {
  if (v.provider === "anthropic") return "Anthropic (api.anthropic.com)";
  if (v.provider === "openai_compat") {
    const url = v.baseUrl.trim() || "the endpoint you enter";
    return isLocalEndpoint(url) ? `${url}, a server on this machine` : url;
  }
  return "nowhere: the assistant is off";
}

export const TEST_ERROR_COPY: Record<LLMTestErrorKind, { title: string; hint: string }> = {
  auth: { title: "The provider rejected the API key", hint: "Check the key, or paste a new one and save." },
  network: {
    title: "Could not reach the endpoint",
    hint: "Check the base URL and that the server is running (for Ollama: ollama serve).",
  },
  model: { title: "The model was not found", hint: "Check the model name, or load the list of models." },
  rate_limit: { title: "The provider is rate limiting requests", hint: "Wait a moment and test again." },
  other: { title: "The test call failed", hint: "The message below is the provider's answer." },
};

import type { ApiError, LLMFailureKind } from "@/shared/api";

export interface AskErrorCopy {
  /** "query" errors render like the Messages tab (SQLSTATE, position, guidance). */
  kind: "llm" | "setup" | "query" | "other";
  title: string;
  hint: string;
  /** Offer a link to the assistant settings. */
  settings: boolean;
}

const LLM_COPY: Record<LLMFailureKind, { title: string; hint: string }> = {
  auth: { title: "The provider rejected the API key", hint: "Check the key in the assistant settings." },
  network: {
    title: "Could not reach the model",
    hint: "Check the endpoint in the assistant settings and that the server is running.",
  },
  refusal: {
    title: "The model declined to answer",
    hint: "Rephrase the question. dbscope does not retry refusals.",
  },
  max_tokens: {
    title: "The answer was cut off",
    hint: "The model ran out of output space. Ask a narrower question, or lower the effort in the settings.",
  },
  invalid_output: {
    title: "The model's answer was not usable",
    hint: "It did not follow the answer format, even after one correction. Ask again, or try a stronger model.",
  },
  rate_limit: { title: "The provider is rate limiting requests", hint: "Wait a moment, then ask again." },
  model: { title: "The model was not found", hint: "Pick another model in the assistant settings." },
};

const QUERY_CODES = new Set([
  "rejected",
  "database",
  "timeout",
  "recovery_conflict",
  "upstream",
  "cost_confirmation_required",
]);

export function describeAskError(error: ApiError): AskErrorCopy {
  if (error.code === "llm") {
    const copy = LLM_COPY[error.detail as LLMFailureKind] ?? {
      title: "The model failed to answer",
      hint: "Ask again, or check the assistant settings.",
    };
    const settings = ["auth", "network", "model", "max_tokens"].includes(error.detail ?? "");
    return { kind: "llm", ...copy, settings };
  }
  if (error.code === "llm_not_configured") {
    return { kind: "setup", title: "The assistant is not set up", hint: error.message, settings: true };
  }
  if (error.code === "secrets_locked") {
    return {
      kind: "setup",
      title: "The secrets vault is locked",
      hint: "Unlock it to use the API key and the connection's password.",
      settings: false,
    };
  }
  if (QUERY_CODES.has(error.code)) return { kind: "query", title: "", hint: "", settings: false };
  return { kind: "other", title: "The answer failed", hint: error.message, settings: false };
}

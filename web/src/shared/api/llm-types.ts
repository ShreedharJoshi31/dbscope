/** Wire types for the Phase 3 assistant API (docs/API.md "Phase 3", internal/nl/pipeline.go). */

import type { QueryResult } from "@/shared/api/query-types";

export type LLMProvider = "anthropic" | "openai_compat" | "none";
export type LLMEffort = "low" | "medium" | "high";

export interface LLMSettings {
  provider: LLMProvider;
  model: string;
  summaryModel: string;
  /** openai_compat only, e.g. "http://localhost:11434/v1/". */
  baseUrl?: string;
  /** anthropic only. */
  effort?: LLMEffort;
  /** Never the key itself. */
  hasApiKey: boolean;
}

/** PUT body: `apiKey` omitted keeps the stored key, "" clears it. */
export interface LLMSettingsInput {
  provider: LLMProvider;
  model: string;
  summaryModel: string;
  baseUrl?: string;
  effort?: LLMEffort;
  apiKey?: string;
}

export type LLMTestErrorKind = "auth" | "network" | "model" | "rate_limit" | "other";

export interface LLMTestResult {
  ok: boolean;
  provider: string;
  model: string;
  latencyMs: number;
  error?: { kind: LLMTestErrorKind; message: string };
}

export interface GlossaryEntry {
  term: string;
  definition: string;
  sql: string;
}

export interface Assumption {
  label: string;
  sqlFragment: string;
}

export interface Clarification {
  question: string;
  answer: string;
  saveToGlossary?: boolean;
}

export interface AskRequest {
  question: string;
  conversationId?: string;
  assumptionOverrides?: Assumption[];
  clarification?: Clarification;
  maxRows?: number;
  confirmCost?: boolean;
  confirmRows?: boolean;
}

export type AskStage = "retrieving" | "generating" | "checking" | "running" | "repairing" | "summarizing";

export interface AskContext {
  provider: string;
  model: string;
  schemaMode: "full" | "retrieved";
  tables: string[];
  omittedColumns: string[];
  sends: "schema" | "schema+rows";
}

export interface AskAnswer {
  kind: "sql" | "clarify";
  sql: string;
  explanationSteps: string[];
  assumptions: Assumption[];
  tables: string[];
  clarifyingQuestion?: { question: string; options: string[] };
  repaired: boolean;
}

export type AskConfirmation =
  | { reason: "cost"; estimatedCost: number; estimatedRows: number; conversationId: string }
  | { reason: "rows"; conversationId: string };

export interface AskSummary {
  text: string;
  mode: "profile" | "rows";
}

export interface AskDone {
  conversationId: string;
  durationMs: number;
  usage: { inputTokens: number; outputTokens: number; cacheReadTokens: number };
}

/** The `llm` error detail values for provider failures. */
export type LLMFailureKind = "auth" | "network" | "refusal" | "max_tokens" | "invalid_output" | "rate_limit" | "model";

/** One parsed SSE frame of POST /ask. `error` carries the envelope's inner object. */
export type AskEvent =
  | { type: "status"; stage: AskStage }
  | { type: "context"; context: AskContext }
  | { type: "answer"; answer: AskAnswer }
  | { type: "needs_confirmation"; confirmation: AskConfirmation }
  | { type: "result"; result: QueryResult }
  | { type: "summary"; summary: AskSummary }
  | { type: "done"; done: AskDone }
  | { type: "error"; error: unknown };

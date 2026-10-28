import { create } from "zustand";
import type { ApiError, Overlay, PlanNode, QueryResult } from "@/shared/api";

export type DockTab = "results" | "messages" | "plan" | "risk" | "schema" | "drift" | "keyspace";
export type RunKind = "query" | "explain";
export type RunSource = "editor" | "assistant";

export interface RunMeta {
  id: number;
  kind: RunKind;
  analyze: boolean;
  /** The SQL sent (the user's text, not the deparsed statement). */
  sql: string;
  /** The SQL tab that ran it, when it came from an editor. */
  tabId: string | null;
  source: RunSource;
  startedAt: number;
}

/** What the dock shows: rows (null for an explain) plus plan, overlay and the executed statement. */
export interface RunOutput {
  meta: RunMeta;
  result: QueryResult | null;
  executedSql: string;
  plan: PlanNode;
  overlay: Overlay;
  warnings: string[];
}

export type MessageLevel = "error" | "warning" | "info";

export interface RunMessage {
  id: number;
  at: number;
  level: MessageLevel;
  meta: RunMeta;
  text: string;
  error?: ApiError;
  /** The deparsed statement a database error position refers to, when known. */
  executedSql?: string;
}

const MAX_MESSAGES = 100;

interface RunState {
  running: RunMeta | null;
  output: RunOutput | null;
  /** The last failed run; cleared when another starts. Editors squiggle guard positions from it. */
  lastError: { meta: RunMeta; error: ApiError } | null;
  messages: RunMessage[];
  dockTab: DockTab;
  /** Bumped whenever a run ends or output is shown, so the dock can open itself. */
  revision: number;
  /** For the status bar. */
  last: { durationMs: number; rowCount: number | null; kind: RunKind } | null;

  start: (meta: RunMeta) => void;
  succeed: (output: RunOutput) => void;
  fail: (meta: RunMeta, error: ApiError, executedSql?: string) => void;
  cancelled: (meta: RunMeta) => void;
  /** Ends a run that stopped for a confirmation without recording an error. */
  settle: (meta: RunMeta) => void;
  /** Shows a result produced elsewhere (an assistant answer) in the dock. */
  show: (output: RunOutput) => void;
  setDockTab: (tab: DockTab) => void;
  clearMessages: () => void;
  /** Forgets every run, as when the workspace switches to another connection; a run still in flight is ignored when it ends. */
  reset: () => void;
}

let messageId = 0;
let runId = 0;

export function nextRunId(): number {
  runId += 1;
  return runId;
}

function message(meta: RunMeta, level: MessageLevel, text: string, extra: Partial<RunMessage> = {}): RunMessage {
  messageId += 1;
  return { id: messageId, at: Date.now(), level, meta, text, ...extra };
}

function withMessages(current: RunMessage[], added: RunMessage[]): RunMessage[] {
  return [...added.reverse(), ...current].slice(0, MAX_MESSAGES);
}

export const useRunStore = create<RunState>((set, get) => ({
  running: null,
  output: null,
  lastError: null,
  messages: [],
  dockTab: "results",
  revision: 0,
  last: null,

  start: (meta) => set({ running: meta, lastError: null }),

  succeed: (output) => {
    const { meta, result, warnings } = output;
    if (get().running?.id !== meta.id) return;
    const added = warnings.map((w) => message(meta, "warning", w));
    set((s) => ({
      running: null,
      output,
      messages: withMessages(s.messages, added),
      dockTab: meta.kind === "explain" ? "plan" : "results",
      revision: s.revision + 1,
      last: { durationMs: result?.durationMs ?? 0, rowCount: result?.rowCount ?? null, kind: meta.kind },
    }));
  },

  fail: (meta, error, executedSql) => {
    if (get().running?.id !== meta.id) return;
    const entry = message(meta, "error", error.message, executedSql ? { error, executedSql } : { error });
    set((s) => ({
      running: null,
      lastError: { meta, error },
      messages: withMessages(s.messages, [entry]),
      dockTab: "messages",
      revision: s.revision + 1,
    }));
  },

  cancelled: (meta) => {
    if (get().running?.id !== meta.id) return;
    set((s) => ({ running: null, messages: withMessages(s.messages, [message(meta, "info", "Cancelled")]) }));
  },

  settle: (meta) => {
    if (get().running?.id === meta.id) set({ running: null });
  },

  show: (output) =>
    set((s) => ({
      output,
      dockTab: output.result ? "results" : "plan",
      revision: s.revision + 1,
      messages: withMessages(
        s.messages,
        output.warnings.map((w) => message(output.meta, "warning", w)),
      ),
    })),

  setDockTab: (dockTab) => set({ dockTab }),
  clearMessages: () => set({ messages: [] }),
  reset: () => set({ running: null, output: null, lastError: null, messages: [], dockTab: "results", last: null }),
}));

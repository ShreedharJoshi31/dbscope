import { create } from "zustand";
import type { RunKind } from "@/entities/query";
import type { Profile } from "@/shared/api";

export interface RunOptions {
  profile: Profile;
  sql: string;
  kind: RunKind;
  analyze: boolean;
  tabId: string | null;
  /** Rows to fetch (API default 500, max 5000). */
  maxRows?: number;
  confirmCost?: boolean;
  /** The user already confirmed that ANALYZE may execute the statement on a prod-replica. */
  analyzeConfirmed?: boolean;
}

export type RunPrompt =
  | { type: "cost"; options: RunOptions; estimatedCost: number; estimatedRows: number; limit: number }
  | { type: "analyze"; options: RunOptions };

interface RunPromptState {
  prompt: RunPrompt | null;
  open: (prompt: RunPrompt) => void;
  close: () => void;
}

/** The confirmation the run is waiting for: an over-limit cost or ANALYZE on a prod-replica. */
export const useRunPrompt = create<RunPromptState>((set) => ({
  prompt: null,
  open: (prompt) => set({ prompt }),
  close: () => set({ prompt: null }),
}));

export const MAX_ROWS_CHOICES = [100, 500, 1000, 2000, 5000] as const;
export const DEFAULT_MAX_ROWS = 500;

export interface TabRunOptions {
  /** Also measure with EXPLAIN ANALYZE. Off by default. */
  analyze: boolean;
  maxRows: number;
}

const DEFAULT_TAB_OPTIONS: TabRunOptions = { analyze: false, maxRows: DEFAULT_MAX_ROWS };

interface RunOptionsState {
  byTab: Record<string, TabRunOptions>;
  set: (tabId: string, patch: Partial<TabRunOptions>) => void;
}

/** Per SQL tab run options, kept for this page load. */
export const useTabRunOptions = create<RunOptionsState>((set) => ({
  byTab: {},
  set: (tabId, patch) =>
    set((s) => ({ byTab: { ...s.byTab, [tabId]: { ...(s.byTab[tabId] ?? DEFAULT_TAB_OPTIONS), ...patch } } })),
}));

export function tabRunOptions(tabId: string): TabRunOptions {
  return useTabRunOptions.getState().byTab[tabId] ?? DEFAULT_TAB_OPTIONS;
}

export function useTabRunOption(tabId: string): TabRunOptions {
  return useTabRunOptions((s) => s.byTab[tabId] ?? DEFAULT_TAB_OPTIONS);
}

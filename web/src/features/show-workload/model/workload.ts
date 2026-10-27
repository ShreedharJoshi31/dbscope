import { create } from "zustand";
import type { OverlayEntry } from "@/entities/schema";
import type { WorkloadSnapshot, WorkloadTable } from "@/shared/api";
import { formatCompact } from "@/shared/lib/format-number";

export type WorkloadMetric = "time" | "calls" | "reads" | "writes";

export const METRIC_LABELS: Record<WorkloadMetric, string> = {
  time: "Query time",
  calls: "Calls",
  reads: "Scans",
  writes: "Writes",
};

/** Metrics that need pg_stat_statements; the others come from table counters. */
export const STATEMENT_METRICS: ReadonlySet<WorkloadMetric> = new Set(["time", "calls"]);

interface WorkloadState {
  on: boolean;
  metric: WorkloadMetric;
  setOn: (on: boolean) => void;
  setMetric: (metric: WorkloadMetric) => void;
}

export const useWorkloadView = create<WorkloadState>((set) => ({
  on: false,
  metric: "time",
  setOn: (on) => set({ on }),
  setMetric: (metric) => set({ metric }),
}));

/** Palette: "Show workload on the canvas". */
export function toggleWorkload(): void {
  const s = useWorkloadView.getState();
  s.setOn(!s.on);
}

export function formatMs(ms: number): string {
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  if (ms < 3_600_000) return `${(ms / 60_000).toFixed(1)} min`;
  return `${(ms / 3_600_000).toFixed(1)} h`;
}

function value(t: WorkloadTable, metric: WorkloadMetric): number {
  switch (metric) {
    case "time":
      return t.queryTimeMs;
    case "calls":
      return t.queryCalls;
    case "reads":
      return t.seqScans + t.idxScans;
    case "writes":
      return t.rowsWritten;
  }
}

function percent(share: number): string {
  return share < 0.005 ? "<1%" : `${Math.round(100 * share)}%`;
}

function count(n: number, one: string, many: string): string {
  return `${formatCompact(n)} ${n === 1 ? one : many}`;
}

function label(t: WorkloadTable, metric: WorkloadMetric, totalMs: number): string {
  switch (metric) {
    case "time":
      return totalMs > 0 ? `${percent(t.queryTimeMs / totalMs)} of query time` : formatMs(t.queryTimeMs);
    case "calls":
      return count(t.queryCalls, "call", "calls");
    case "reads":
      return count(t.seqScans + t.idxScans, "scan", "scans");
    case "writes":
      return count(t.rowsWritten, "row written", "rows written");
  }
}

function details(t: WorkloadTable): string[] {
  const lines: string[] = [];
  if (t.queryCalls > 0) {
    lines.push(`${count(t.queryCalls, "call", "calls")} of statements using it, ${formatMs(t.queryTimeMs)} in total`);
  }
  lines.push(
    `${count(t.seqScans, "sequential scan", "sequential scans")} and ${count(t.idxScans, "index scan", "index scans")}`,
  );
  lines.push(`${count(t.rowsWritten, "row written", "rows written")}, ${formatCompact(t.liveRows)} live`);
  const cols = Object.entries(t.columns ?? {})
    .sort((a, b) => b[1].calls - a[1].calls)
    .slice(0, 5)
    .map(([name]) => name);
  if (cols.length > 0) lines.push(`Most used columns: ${cols.join(", ")}`);
  if ((t.wildcardCalls ?? 0) > 0)
    lines.push(`${count(t.wildcardCalls ?? 0, "call selects", "calls select")} * from it`);
  const top = t.topQueries?.[0];
  if (top)
    lines.push(`Slowest in total: ${top.text.replace(/\s+/g, " ").slice(0, 160)} (mean ${formatMs(top.meanMs)})`);
  return lines;
}

/**
 * Badges for tables with activity under metric. Intensity is on a log scale,
 * so one very hot table does not flatten the rest.
 */
export function workloadEntries(s: WorkloadSnapshot, metric: WorkloadMetric): Record<string, OverlayEntry> {
  const values = Object.entries(s.tables).map(([key, t]) => [key, t, value(t, metric)] as const);
  const max = Math.max(0, ...values.map(([, , v]) => v));
  const entries: Record<string, OverlayEntry> = {};
  if (max <= 0) return entries;
  for (const [key, t, v] of values) {
    if (v <= 0) continue;
    entries[key] = {
      tone: "info",
      label: label(t, metric, s.statements.totalTimeMs),
      intensity: Math.log1p(v) / Math.log1p(max),
      details: details(t),
    };
  }
  return entries;
}

/** One line on what the numbers under metric cover and what is missing from them. */
export function workloadSummary(s: WorkloadSnapshot, metric: WorkloadMetric, now: number = Date.now()): string {
  const parts: string[] = [];
  const st = s.statements;
  // Query time and calls count since pg_stat_statements was reset; scans and writes since the table counters were.
  const since = STATEMENT_METRICS.has(metric) ? st.since : s.tableStatsReset;
  parts.push(
    since
      ? `Totals since ${new Date(since).toLocaleString()}`
      : "Totals since the statistics were last reset (no time recorded)",
  );
  if (since && now - new Date(since).getTime() < 24 * 3_600_000) parts.push("less than a day of data");
  if (s.inRecovery) parts.push("a replica: only what ran here, no writes");
  switch (st.status) {
    case "not_installed":
      parts.push("pg_stat_statements is not installed in this database, so only scans and writes are shown");
      break;
    case "not_loaded":
      parts.push(
        "pg_stat_statements is installed but not in shared_preload_libraries, so only scans and writes are shown",
      );
      break;
    case "unavailable":
      parts.push(`pg_stat_statements could not be read (${st.message ?? "unknown error"})`);
      break;
    case "ok": {
      if (!STATEMENT_METRICS.has(metric)) break;
      const missing = st.hiddenCalls + st.unattributedCalls + st.unreadCalls;
      if (st.totalCalls > 0 && missing > 0) {
        parts.push(`${percent(missing / st.totalCalls)} of calls not tied to a table`);
      }
      if (st.hiddenCalls > 0) parts.push("some statements are hidden without pg_read_all_stats");
      if (st.unreadCalls > 0) parts.push(`only the ${st.read} statements with the most time were read`);
    }
  }
  return `${parts.join(" · ")}.`;
}

import { useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { costLimit } from "@/entities/connection";
import { activeSqlTab, nextRunId, type RunKind, type RunMeta, type RunOutput, useRunStore } from "@/entities/query";
import { showQueryOverlay } from "@/entities/schema";
import { type RunOptions, tabRunOptions, useRunPrompt } from "@/features/run-query/model/prompt-store";
import { ApiError, explainQuery, isApiError, type Profile, queryApiKeys, runQuery } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { formatGrouped } from "@/shared/lib/format-number";

let controller: AbortController | null = null;

function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`;
}

/** The deparsed statement a database error's position refers to, when the agent sent it or an earlier run of the same SQL showed it. */
function executedSqlFor(meta: RunMeta, error: ApiError): string | undefined {
  if (error.executedSql) return error.executedSql;
  const output = useRunStore.getState().output;
  return output && output.meta.sql === meta.sql ? output.executedSql : undefined;
}

async function fetchOutput(meta: RunMeta, options: RunOptions, signal: AbortSignal): Promise<RunOutput> {
  const { profile, sql, analyze, confirmCost, maxRows } = options;
  const body = { sql, ...(analyze ? { analyze } : {}), ...(confirmCost ? { confirmCost } : {}) };
  if (meta.kind === "query") {
    const result = await runQuery(profile.id, maxRows ? { ...body, maxRows } : body, signal);
    return { meta, result, ...result };
  }
  const explain = await explainQuery(profile.id, body, signal);
  return { meta, result: null, ...explain };
}

/**
 * Runs SQL through the guarded executor (or EXPLAIN only) and records the
 * outcome in the run store. A running query is cancelled first: one query at
 * a time. A 409 opens the cost confirmation instead of failing.
 */
export async function executeRun(options: RunOptions, onSettled?: (profileId: string) => void): Promise<void> {
  controller?.abort();
  const ac = new AbortController();
  controller = ac;
  const meta: RunMeta = {
    id: nextRunId(),
    kind: options.kind,
    analyze: options.analyze,
    sql: options.sql,
    tabId: options.tabId,
    source: "editor",
    startedAt: Date.now(),
  };
  const store = useRunStore.getState();
  store.start(meta);
  try {
    const output = await fetchOutput(meta, options, ac.signal);
    store.succeed(output);
    showQueryOverlay(output.overlay);
    announce(
      output.result
        ? `Query finished, ${formatGrouped(output.result.rowCount)} rows, ${formatDuration(output.result.durationMs)}.`
        : "Plan ready.",
    );
  } catch (error) {
    if (isAbort(error)) {
      store.cancelled(meta);
      announce("Query cancelled.");
    } else if (isApiError(error) && error.code === "cost_confirmation_required") {
      store.settle(meta);
      useRunPrompt.getState().open({
        type: "cost",
        options,
        estimatedCost: error.estimatedCost ?? 0,
        estimatedRows: error.estimatedRows ?? 0,
        limit: costLimit(options.profile),
      });
    } else {
      const apiError = isApiError(error) ? error : new ApiError("internal", String(error), 0);
      store.fail(meta, apiError, executedSqlFor(meta, apiError));
      announce(`Query failed: ${apiError.message}`);
    }
  } finally {
    if (controller === ac) controller = null;
    if (options.kind === "query") onSettled?.(options.profile.id);
  }
}

/** Aborts the running query; the agent cancels the statement on the server. */
export function cancelRun(): boolean {
  if (!controller) return false;
  controller.abort();
  return true;
}

/** Asks first when ANALYZE would execute the statement on a prod-replica. */
export function requestRun(options: RunOptions, onSettled?: (profileId: string) => void): void {
  if (options.analyze && options.profile.env === "prod-replica" && !options.analyzeConfirmed) {
    useRunPrompt.getState().open({ type: "analyze", options });
    return;
  }
  void executeRun(options, onSettled);
}

/** Run, explain and cancel bound to the query client (history refresh after each run). */
export function useQueryRunner() {
  const client = useQueryClient();
  return useMemo(() => {
    const onSettled = (profileId: string) =>
      void client.invalidateQueries({ queryKey: queryApiKeys.history(profileId) });
    return {
      onSettled,
      request: (options: RunOptions) => requestRun(options, onSettled),
      execute: (options: RunOptions) => executeRun(options, onSettled),
      cancel: cancelRun,
      /** Runs the active SQL tab; announces why when there is nothing to run. */
      runActive: (profile: Profile | undefined, kind: RunKind) => {
        const tab = activeSqlTab();
        if (!tab) {
          announce("Open a SQL tab to run a query.");
          return;
        }
        if (!profile) {
          announce("The sample schema has no database. Add a connection to run queries.");
          return;
        }
        if (!tab.sql.trim()) {
          announce("The SQL tab is empty.");
          return;
        }
        const { analyze, maxRows } = tabRunOptions(tab.id);
        requestRun({ profile, sql: tab.sql, kind, analyze, maxRows, tabId: tab.id }, onSettled);
      },
    };
  }, [client]);
}

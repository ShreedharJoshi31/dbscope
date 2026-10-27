import { Copy, Download, Info, LoaderCircle, Rows3, Table2 } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { useActiveConnection } from "@/entities/connection";
import {
  csvFileName,
  ExecutedSql,
  formatCell,
  formatRowTsv,
  type RunOutput,
  toCsv,
  useRunStore,
} from "@/entities/query";
import { formatDuration } from "@/features/run-query";
import type { QueryResult } from "@/shared/api";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { copyText, downloadText } from "@/shared/lib/clipboard";
import { formatGrouped } from "@/shared/lib/format-number";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import type { GridPosition } from "@/widgets/results-dock/model/grid";

// TanStack Table and Virtual load with the first result, like CodeMirror and React Flow.
const ResultGrid = lazy(() => import("@/widgets/results-dock/ui/result-grid"));

function ResultToolbar({
  output,
  result,
  active,
}: {
  output: RunOutput;
  result: QueryResult;
  active: GridPosition | null;
}) {
  const { profile } = useActiveConnection();
  const copy = (text: string, what: string) =>
    void copyText(text).then((ok) =>
      announce(ok ? `Copied the ${what}.` : "Copy failed; the browser blocked the clipboard."),
    );
  const exportCsv = () => {
    downloadText(csvFileName(profile?.name ?? "result"), toCsv(result.columns, result.rows));
    announce(`Exported ${formatGrouped(result.rowCount)} rows as CSV.`);
  };
  const row = active ? result.rows[active.row] : undefined;

  return (
    <div className="flex shrink-0 flex-col gap-1 border-b border-border-default px-2 py-1">
      <div className="flex flex-wrap items-center gap-2">
        <span data-testid="result-summary" className="text-xs text-text-muted">
          <span className="font-mono text-text-primary tabular-nums">{formatGrouped(result.rowCount)}</span> rows in{" "}
          <span className="font-mono text-text-primary tabular-nums">{formatDuration(result.durationMs)}</span>
          {output.meta.source === "assistant" && " · from the assistant"}
        </span>
        <div className="ml-auto flex items-center gap-1">
          <Button
            variant="ghost"
            size="xs"
            disabled={!row || !active}
            onClick={() => active && copy(formatCell(row?.[active.col] ?? null), "cell")}
            title={`Copy the focused cell (${formatCombo({ key: "c", mod: true })} in the grid)`}
          >
            <Copy aria-hidden />
            Copy cell
          </Button>
          <Button variant="ghost" size="xs" disabled={!row} onClick={() => row && copy(formatRowTsv(row), "row")}>
            <Rows3 aria-hidden />
            Copy row
          </Button>
          <Button variant="outline" size="xs" onClick={exportCsv} disabled={result.columns.length === 0}>
            <Download aria-hidden />
            Export CSV
          </Button>
        </div>
      </div>
      {result.truncated && (
        <p data-testid="truncated-banner" className="flex items-center gap-1 text-xs text-status-warn-fg">
          <Info aria-hidden className="size-3.5" />
          Showing the first {formatGrouped(result.rowCount)} rows; the query returned more. Add a LIMIT or a narrower
          WHERE clause to see the rest.
        </p>
      )}
      <ExecutedSql executedSql={output.executedSql} submittedSql={output.meta.sql} />
    </div>
  );
}

export function ResultsTab() {
  const output = useRunStore((s) => s.output);
  const [active, setActive] = useState<GridPosition | null>(null);
  const result = output?.result;

  if (!output || !result) {
    return (
      <EmptyState
        icon={Table2}
        title={output ? "Explain shows no rows" : "No results yet"}
        description={
          output
            ? "The last run was EXPLAIN only, so nothing was fetched. Its plan is on the Plan tab."
            : `Run a query (${formatCombo(SHORTCUTS.runQuery.combo)}) to see rows here with the row count and timing.`
        }
      />
    );
  }
  if (result.columns.length === 0) {
    return (
      <EmptyState icon={Table2} title="The query returned no columns" description="It ran and returned no columns." />
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      <ResultToolbar output={output} result={result} active={active} />
      <div className="min-h-0 flex-1">
        {result.rows.length === 0 ? (
          <p className="p-3 text-xs text-text-muted">The query returned no rows.</p>
        ) : (
          <Suspense
            fallback={
              <p role="status" className="flex items-center gap-2 p-3 text-xs text-text-muted">
                <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
                Loading the grid…
              </p>
            }
          >
            <ResultGrid key={output.meta.id} result={result} onActiveChange={setActive} />
          </Suspense>
        )}
      </div>
    </div>
  );
}

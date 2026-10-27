import { PanelBottomOpen } from "lucide-react";
import type { Turn } from "@/entities/assistant-turn";
import { ExecutedSql, ResultMiniTable } from "@/entities/query";
import { showTurnResult } from "@/features/ask-assistant";
import { formatDuration } from "@/features/run-query";
import { formatGrouped } from "@/shared/lib/format-number";
import { Button } from "@/shared/ui/button";

const MINI_ROWS = 20;

/** First rows of the answer's result, the exact statement run, and a way to see all of it in the dock. */
export function TurnResult({ turn }: { turn: Turn }) {
  const result = turn.result;
  if (!result) return null;
  const shown = Math.min(MINI_ROWS, result.rows.length);
  return (
    <section data-section="result" aria-label="Result" className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2 text-xs text-text-muted">
        <span>
          <span className="font-mono text-text-primary tabular-nums">{formatGrouped(result.rowCount)}</span>
          {result.truncated ? "+" : ""} rows in{" "}
          <span className="font-mono tabular-nums">{formatDuration(result.durationMs)}</span>
          {result.rowCount > shown && ` · first ${shown} shown`}
        </span>
        <Button variant="ghost" size="xs" className="ml-auto" onClick={() => showTurnResult(turn)}>
          <PanelBottomOpen aria-hidden />
          Open in results
        </Button>
      </div>
      {result.columns.length > 0 && result.rows.length > 0 ? (
        <ResultMiniTable result={result} limit={MINI_ROWS} caption={`First ${shown} rows of the answer`} />
      ) : (
        <p className="text-xs text-text-muted">No rows.</p>
      )}
      <ExecutedSql executedSql={result.executedSql} submittedSql={turn.answer?.sql} />
    </section>
  );
}

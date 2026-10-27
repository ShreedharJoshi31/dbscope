import { RotateCcw } from "lucide-react";
import type { ReactNode } from "react";
import { describeQueryError } from "@/entities/query/model/error-copy";
import { positionToRange } from "@/entities/query/model/position";
import { SqlWithMark } from "@/entities/query/ui/executed-sql";
import type { ApiError } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { StatusIcon } from "@/shared/ui/status-icon";

interface QueryErrorViewProps {
  error: ApiError;
  /** The SQL that was sent; guard positions point into it. */
  submittedSql: string;
  /** The deparsed statement database positions point into, when known. */
  executedSql?: string | undefined;
  statementTimeoutMs?: number | undefined;
  onRetry?: (() => void) | undefined;
  /** Extra actions, e.g. "Show in editor". */
  children?: ReactNode;
}

function PositionLine({ error, submittedSql, executedSql }: Omit<QueryErrorViewProps, "statementTimeoutMs">) {
  const copy = describeQueryError(error);
  if (!copy.position) return null;
  const { value, target } = copy.position;
  if (target === "input") {
    const range = positionToRange(submittedSql, value);
    return (
      <>
        <p className="text-xs text-text-muted">
          {range ? `Line ${range.line}, column ${range.column} of your SQL.` : `Character ${value} of your SQL.`}
        </p>
        <SqlWithMark sql={submittedSql} position={value} label="Your SQL" />
      </>
    );
  }
  if (executedSql) {
    return (
      <>
        <p className="text-xs text-text-muted">
          Character {value} of the executed SQL (dbscope reformats your statement before running it).
        </p>
        <SqlWithMark sql={executedSql} position={value} label="Executed SQL" />
      </>
    );
  }
  return (
    <>
      <p className="text-xs text-text-muted">
        Character {value} of the statement as dbscope ran it. dbscope reformats SQL before running it and the error does
        not include that text, so the position may not line up with what you typed.
      </p>
      <SqlWithMark sql={submittedSql} label="Your SQL" />
    </>
  );
}

/** SQLSTATE, message, position and guidance for a failed run (UX rule 10). Never just a toast. */
export function QueryErrorView({ error, statementTimeoutMs, onRetry, children, ...sql }: QueryErrorViewProps) {
  const copy = describeQueryError(error, statementTimeoutMs);
  return (
    <div data-testid="query-error" data-code={error.code} className="flex min-w-0 flex-col gap-2">
      <p className="flex items-start gap-1.5 text-sm font-medium text-status-danger-fg">
        <StatusIcon tone="danger" className="mt-0.5" />
        {copy.title}
      </p>
      <p className="text-sm break-words text-text-primary">
        {copy.message}
        {copy.sqlstate && (
          <span className="ml-2 font-mono text-xs text-text-muted">
            SQLSTATE <span className="text-text-primary">{copy.sqlstate}</span>
          </span>
        )}
      </p>
      <PositionLine error={error} {...sql} />
      {copy.guidance && <p className="text-xs text-text-muted">{copy.guidance}</p>}
      {(children || (copy.retryable && onRetry)) && (
        <div className="flex flex-wrap items-center gap-2">
          {copy.retryable && onRetry && (
            <Button variant="outline" size="sm" onClick={onRetry}>
              <RotateCcw aria-hidden />
              Run again
            </Button>
          )}
          {children}
        </div>
      )}
    </div>
  );
}

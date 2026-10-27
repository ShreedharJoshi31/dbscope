import { cn } from "cn";
import { lazy, Suspense } from "react";

const HighlightedSql = lazy(() => import("@/shared/ui/sql/highlighted-sql"));

interface SqlCodeProps {
  sql: string;
  label: string;
  className?: string;
}

/** Read-only, syntax-highlighted SQL. Plain text until the highlighter chunk loads. */
export function SqlCode({ sql, label, className }: SqlCodeProps) {
  return (
    <figure aria-label={label} className="m-0 min-w-0">
      <pre
        data-testid="sql-code"
        className={cn(
          "overflow-x-auto rounded-md border border-border-default bg-surface-canvas p-2 font-mono text-xs whitespace-pre-wrap text-text-primary",
          className,
        )}
      >
        <code>
          <Suspense fallback={sql}>
            <HighlightedSql sql={sql} />
          </Suspense>
        </code>
      </pre>
    </figure>
  );
}

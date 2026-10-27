import { ChevronRight } from "lucide-react";
import { positionToRange } from "@/entities/query/model/position";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/shared/ui/collapsible";

interface SqlWithMarkProps {
  sql: string;
  /** 1-based character offset to highlight. */
  position?: number | undefined;
  label?: string;
}

/** SQL as plain text, with the token at `position` marked. */
export function SqlWithMark({ sql, position, label = "SQL" }: SqlWithMarkProps) {
  const range = position ? positionToRange(sql, position) : null;
  return (
    <figure aria-label={label} className="m-0 min-w-0">
      <pre className="max-h-48 overflow-auto rounded-md border border-border-default bg-surface-subtle p-2 font-mono text-xs whitespace-pre-wrap text-text-primary">
        {range ? (
          <>
            {sql.slice(0, range.from)}
            <mark
              data-testid="sql-error-mark"
              className="rounded-xs bg-status-danger-subtle text-status-danger-fg underline decoration-status-danger-fg decoration-wavy underline-offset-2"
            >
              {sql.slice(range.from, range.to) || " "}
            </mark>
            {sql.slice(range.to)}
          </>
        ) : (
          sql
        )}
      </pre>
    </figure>
  );
}

interface ExecutedSqlProps {
  executedSql: string;
  /** The SQL the user sent; when it differs, the disclosure says dbscope rewrote it. */
  submittedSql?: string | undefined;
  position?: number | undefined;
  defaultOpen?: boolean;
}

/** "Executed SQL" disclosure: the exact statement dbscope ran (UX rule 4). */
export function ExecutedSql({ executedSql, submittedSql, position, defaultOpen = false }: ExecutedSqlProps) {
  const rewritten = submittedSql !== undefined && submittedSql.trim() !== executedSql.trim();
  return (
    <Collapsible defaultOpen={defaultOpen}>
      <CollapsibleTrigger className="group/exec flex items-center gap-1 rounded-sm text-xs font-medium text-text-muted hover:text-text-primary">
        <ChevronRight aria-hidden className="size-3.5 transition-transform group-data-panel-open/exec:rotate-90" />
        Executed SQL
        {rewritten && <span className="font-normal">(rewritten by dbscope)</span>}
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1">
        <SqlWithMark sql={executedSql} position={position} label="Executed SQL" />
      </CollapsibleContent>
    </Collapsible>
  );
}

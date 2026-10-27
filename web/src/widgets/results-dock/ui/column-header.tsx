import { Network } from "lucide-react";
import type { ResultColumn } from "@/shared/api";
import { mappedLabel } from "@/widgets/results-dock/model/grid";
import { showColumnOnCanvas } from "@/widgets/results-dock/model/show-on-canvas";

/** Name, type and, for columns read straight from a table, a link to that table on the canvas. */
export function ColumnHeader({ column, index }: { column: ResultColumn; index: number }) {
  const mapped = mappedLabel(column);
  return (
    <div
      role="columnheader"
      aria-colindex={index + 2}
      data-testid="result-column"
      className="flex min-w-0 flex-col border-l border-border-default px-2 py-0.5"
    >
      <span className="truncate font-mono font-semibold text-text-primary" title={column.name}>
        {column.name}
      </span>
      <span className="flex min-w-0 items-center gap-1 text-2xs text-text-muted">
        <span className="truncate font-mono" title={column.type}>
          {column.type}
        </span>
        {mapped && (
          <button
            type="button"
            onClick={() => showColumnOnCanvas(column)}
            title={`Show ${column.relation}.${column.column} on the canvas`}
            className="flex min-w-0 items-center gap-0.5 rounded-xs font-mono text-text-link hover:underline"
          >
            <Network aria-hidden className="size-3 shrink-0" />
            <span className="truncate">{mapped}</span>
          </button>
        )}
      </span>
    </div>
  );
}

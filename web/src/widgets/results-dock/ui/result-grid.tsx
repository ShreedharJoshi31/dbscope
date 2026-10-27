import { createColumnHelper, tableFeatures, useTable } from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import { cn } from "cn";
import { type KeyboardEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CellValueView, cellKind, formatCell, isNumericType } from "@/entities/query";
import type { CellValue, QueryResult } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { copyText } from "@/shared/lib/clipboard";
import { columnWidths, type GridPosition, moveCell, ROW_NUMBER_WIDTH } from "@/widgets/results-dock/model/grid";
import { ColumnHeader } from "@/widgets/results-dock/ui/column-header";

const ROW_HEIGHT = 24;
type Row = CellValue[];
const features = tableFeatures({});
const helper = createColumnHelper<typeof features, Row>();

export interface ResultGridProps {
  result: QueryResult;
  /** Reports the focused cell, for the toolbar's copy buttons. */
  onActiveChange: (pos: GridPosition | null) => void;
}

function useGridTable(result: QueryResult) {
  const columns = useMemo(
    () =>
      helper.columns(
        result.columns.map((c, i) => helper.accessor((row: Row) => row[i] ?? null, { id: `c${i}`, header: c.name })),
      ),
    [result.columns],
  );
  return useTable({ features, columns, data: result.rows });
}

/**
 * ARIA grid on divs (the APG pattern for virtualized grids): rows are absolutely
 * positioned, so <table> layout cannot be used. Biome's semantic-element rules
 * are turned off for this file in biome.json.
 *
 * The results grid: TanStack Table for the row/cell model, TanStack Virtual
 * for rows, so thousands of rows render only what is on screen. A single
 * roving tab stop (UX rule 12): arrows, Page Up/Down, Home/End move;
 * Ctrl/Cmd+C copies the focused cell.
 */
export default function ResultGrid({ result, onActiveChange }: ResultGridProps) {
  const table = useGridTable(result);
  const rows = table.getRowModel().rows;
  const scrollRef = useRef<HTMLDivElement>(null);
  const [active, setActive] = useState<GridPosition>({ row: 0, col: 0 });
  const focusPending = useRef(false);
  const widths = useMemo(() => columnWidths(result.columns, result.rows), [result]);
  const template = `${ROW_NUMBER_WIDTH}px ${widths.map((w) => `${w}px`).join(" ")}`;
  const totalWidth = ROW_NUMBER_WIDTH + widths.reduce((a, b) => a + b, 0);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
    scrollPaddingStart: ROW_HEIGHT,
  });

  // The target row may render only after the virtualizer scrolls; keep trying on each render until it exists.
  useEffect(() => {
    if (!focusPending.current) return;
    const cell = scrollRef.current?.querySelector<HTMLElement>(`[data-cell="${active.row}:${active.col}"]`);
    if (!cell) return;
    focusPending.current = false;
    cell.focus({ preventScroll: true });
  });

  const go = useCallback(
    (next: GridPosition) => {
      setActive(next);
      onActiveChange(next);
      focusPending.current = true;
      virtualizer.scrollToIndex(next.row, { align: "auto" });
    },
    [onActiveChange, virtualizer],
  );

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const mod = event.metaKey || event.ctrlKey;
    if (mod && event.key.toLowerCase() === "c" && !event.shiftKey) {
      event.preventDefault();
      const value = result.rows[active.row]?.[active.col] ?? null;
      void copyText(formatCell(value)).then((ok) => announce(ok ? "Copied the cell." : "Copy failed."));
      return;
    }
    const page = Math.max(1, Math.floor((scrollRef.current?.clientHeight ?? 240) / ROW_HEIGHT) - 1);
    const next = moveCell(active, event.key, mod, { rows: rows.length, cols: result.columns.length, page });
    if (!next) return;
    event.preventDefault();
    go(next);
  };

  return (
    <div
      ref={scrollRef}
      role="grid"
      aria-label={`Query results, ${result.rowCount} rows`}
      aria-rowcount={rows.length + 1}
      aria-colcount={result.columns.length + 1}
      data-testid="result-grid"
      onKeyDown={onKeyDown}
      className="relative h-full min-h-0 overflow-auto text-xs"
    >
      <div style={{ width: totalWidth, minWidth: "100%" }}>
        <div
          role="row"
          aria-rowindex={1}
          className="sticky top-0 z-10 grid border-b border-border-default bg-surface-subtle"
          style={{ gridTemplateColumns: template }}
        >
          <span role="columnheader" className="px-2 py-1 text-right text-2xs text-text-muted">
            #
          </span>
          {table.getHeaderGroups()[0]?.headers.map((header) => {
            const column = result.columns[header.index];
            return column ? <ColumnHeader key={header.id} column={column} index={header.index} /> : null;
          })}
        </div>
        <div role="rowgroup" className="relative" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((item) => {
            const row = rows[item.index];
            if (!row) return null;
            return (
              <div
                key={row.id}
                role="row"
                aria-rowindex={item.index + 2}
                data-row={item.index}
                className="absolute top-0 left-0 grid w-full border-b border-border-default hover:bg-surface-hover"
                style={{ gridTemplateColumns: template, height: ROW_HEIGHT, transform: `translateY(${item.start}px)` }}
              >
                <span
                  role="rowheader"
                  className="truncate px-2 text-right font-mono leading-6 text-text-muted tabular-nums"
                >
                  {item.index + 1}
                </span>
                {row.getAllCells().map((cell, col) => {
                  const value = cell.getValue() as CellValue;
                  const type = result.columns[col]?.type;
                  const isActive = active.row === item.index && active.col === col;
                  return (
                    <div
                      key={cell.id}
                      role="gridcell"
                      aria-colindex={col + 2}
                      data-cell={`${item.index}:${col}`}
                      tabIndex={isActive ? 0 : -1}
                      onFocus={() => {
                        if (!isActive) setActive({ row: item.index, col });
                        onActiveChange({ row: item.index, col });
                      }}
                      onClick={() => go({ row: item.index, col })}
                      className={cn(
                        "min-w-0 border-l border-border-default px-2 leading-6 outline-none",
                        "focus-visible:ring-2 focus-visible:ring-border-focus focus-visible:ring-inset",
                        isActive && "bg-surface-selected",
                        (cellKind(value, type) === "number" || isNumericType(type)) && "text-right",
                      )}
                    >
                      <CellValueView value={value} type={type} />
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

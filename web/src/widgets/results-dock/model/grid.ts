import { formatCell } from "@/entities/query";
import type { CellValue, ResultColumn } from "@/shared/api";

/** Approximate width of one character of the 12px mono font. */
const CHAR = 7.4;
const PADDING = 20;
export const MIN_COLUMN = 72;
export const MAX_COLUMN = 420;
export const ROW_NUMBER_WIDTH = 52;
const SAMPLE_ROWS = 100;

/** "orders.customer_id" for a column that maps to a table column. */
export function mappedLabel(c: ResultColumn): string | null {
  if (!c.relation || !c.column) return null;
  return `${c.relation.split(".").slice(1).join(".") || c.relation}.${c.column}`;
}

/** Column widths from the header text and a sample of values, clamped. */
export function columnWidths(columns: readonly ResultColumn[], rows: readonly (readonly CellValue[])[]): number[] {
  const sample = rows.slice(0, SAMPLE_ROWS);
  return columns.map((c, i) => {
    // Type and table.column share the second header line.
    const mapped = mappedLabel(c);
    let chars = Math.max(c.name.length, c.type.length + (mapped ? mapped.length + 3 : 0));
    for (const row of sample) {
      const len = formatCell(row[i] ?? null).length;
      if (len > chars) chars = len;
      if (chars * CHAR > MAX_COLUMN) break;
    }
    return Math.round(Math.min(MAX_COLUMN, Math.max(MIN_COLUMN, chars * CHAR + PADDING)));
  });
}

export interface GridPosition {
  row: number;
  col: number;
}

/** Roving-tabindex movement inside the grid; returns null for keys the grid does not handle. */
export function moveCell(
  pos: GridPosition,
  key: string,
  ctrl: boolean,
  size: { rows: number; cols: number; page: number },
): GridPosition | null {
  const clampRow = (r: number) => Math.max(0, Math.min(size.rows - 1, r));
  const clampCol = (c: number) => Math.max(0, Math.min(size.cols - 1, c));
  switch (key) {
    case "ArrowDown":
      return { ...pos, row: clampRow(pos.row + 1) };
    case "ArrowUp":
      return { ...pos, row: clampRow(pos.row - 1) };
    case "ArrowRight":
      return { ...pos, col: clampCol(pos.col + 1) };
    case "ArrowLeft":
      return { ...pos, col: clampCol(pos.col - 1) };
    case "PageDown":
      return { ...pos, row: clampRow(pos.row + size.page) };
    case "PageUp":
      return { ...pos, row: clampRow(pos.row - size.page) };
    case "Home":
      return ctrl ? { row: 0, col: 0 } : { ...pos, col: 0 };
    case "End":
      return ctrl ? { row: size.rows - 1, col: size.cols - 1 } : { ...pos, col: size.cols - 1 };
    default:
      return null;
  }
}

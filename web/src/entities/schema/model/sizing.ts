import type { Position } from "@xyflow/react";
import { type ColumnFlags, columnFlags, fkColumnSets } from "@/entities/schema/model/graph";
import { HEADER_HANDLE, handleId } from "@/entities/schema/model/keys";
import type { Column, Relation } from "@/shared/api";

/**
 * Table node geometry. Sizes are computed from the column count, never
 * measured from the DOM, so layout can run in a worker before anything renders
 * and edges anchor to rows that are exactly where the node draws them.
 */
export const NODE_WIDTH = 280;
export const HEADER_HEIGHT = 44;
export const ROW_HEIGHT = 24;
export const FOOTER_ROW_HEIGHT = 24;
export const BODY_PADDING = 4;
export const STUB_WIDTH = 220;
export const STUB_HEIGHT = 44;

export interface ColumnRow {
  column: Column;
  flags: ColumnFlags;
}

export type FooterRow = { kind: "partitions"; count: number; key: string } | { kind: "hidden"; count: number };

export interface HandleSpec {
  id: string;
  type: "source" | "target";
  position: Position.Left | Position.Right;
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface NodeLayout {
  width: number;
  height: number;
  rows: ColumnRow[];
  footers: FooterRow[];
  /** Columns that have a row (and so a handle) in this mode. */
  visible: ReadonlySet<string>;
  /** React Flow `node.handles`: right side = edge source (child), left side = edge target (parent). */
  handles: HandleSpec[];
}

/** Collapsed nodes keep PK and FK columns, the ones relationships hang off (ChartDB "visibleFields"). */
export function visibleColumns(relation: Relation, collapsed: boolean): ColumnRow[] {
  const sets = fkColumnSets(relation);
  const rows = (relation.columns ?? []).map((column) => ({ column, flags: columnFlags(relation, column, sets) }));
  return collapsed ? rows.filter((r) => r.flags.pk || r.flags.fk || r.flags.inferredFk) : rows;
}

function rowHandles(name: string, centreY: number, width: number): HandleSpec[] {
  const y = centreY - 0.5;
  return [
    // Type-only import: the string values match React Flow's Position enum without pulling it into the shell bundle.
    { id: handleId(name, "L"), type: "target", position: "left" as Position.Left, x: 0, y, width: 1, height: 1 },
    {
      id: handleId(name, "R"),
      type: "source",
      position: "right" as Position.Right,
      x: width - 1,
      y,
      width: 1,
      height: 1,
    },
  ];
}

function computeLayout(relation: Relation, collapsed: boolean): NodeLayout {
  if (relation.stub) {
    return {
      width: STUB_WIDTH,
      height: STUB_HEIGHT,
      rows: [],
      footers: [],
      visible: new Set(),
      handles: rowHandles(HEADER_HANDLE, STUB_HEIGHT / 2, STUB_WIDTH),
    };
  }
  const rows = visibleColumns(relation, collapsed);
  const footers: FooterRow[] = [];
  const total = relation.columns?.length ?? 0;
  if (rows.length < total) footers.push({ kind: "hidden", count: total - rows.length });
  if (relation.kind === "partitioned_table") {
    footers.push({ kind: "partitions", count: relation.partitions?.length ?? 0, key: relation.partitionKey ?? "" });
  }
  const height = HEADER_HEIGHT + rows.length * ROW_HEIGHT + footers.length * FOOTER_ROW_HEIGHT + BODY_PADDING;
  const handles = rowHandles(HEADER_HANDLE, HEADER_HEIGHT / 2, NODE_WIDTH);
  rows.forEach((row, i) => {
    handles.push(...rowHandles(row.column.name, HEADER_HEIGHT + i * ROW_HEIGHT + ROW_HEIGHT / 2, NODE_WIDTH));
  });
  return {
    width: NODE_WIDTH,
    height,
    rows,
    footers,
    visible: new Set(rows.map((r) => r.column.name)),
    handles,
  };
}

const layoutCache = new WeakMap<Relation, { open?: NodeLayout; collapsed?: NodeLayout }>();

/** Cached per Relation object, so node data stays referentially stable across rebuilds. */
export function nodeLayout(relation: Relation, collapsed: boolean): NodeLayout {
  let entry = layoutCache.get(relation);
  if (!entry) {
    entry = {};
    layoutCache.set(relation, entry);
  }
  const slot = collapsed ? "collapsed" : "open";
  let layout = entry[slot];
  if (!layout) {
    layout = computeLayout(relation, collapsed);
    entry[slot] = layout;
  }
  return layout;
}

export function nodeSize(relation: Relation, collapsed: boolean): { width: number; height: number } {
  const { width, height } = nodeLayout(relation, collapsed);
  return { width, height };
}

/** Handle id an edge end uses: the column row when visible, else the header. */
export function anchorHandle(layout: NodeLayout, column: string, side: "L" | "R"): string {
  return handleId(layout.visible.has(column) ? column : HEADER_HANDLE, side);
}

/** Vertical centre of a column row within its node, for panning to a column. */
export function columnOffsetY(layout: NodeLayout, column: string): number {
  const index = layout.rows.findIndex((r) => r.column.name === column);
  return index === -1 ? HEADER_HEIGHT / 2 : HEADER_HEIGHT + index * ROW_HEIGHT + ROW_HEIGHT / 2;
}

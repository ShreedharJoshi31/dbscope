import type { Node, NodeProps } from "@xyflow/react";
import { cn } from "cn";
import { ChevronDown, ChevronRight, Layers } from "lucide-react";
import { memo } from "react";
import { type ColumnMark, nodeVisual, type OverlayEntry, useCanvasStore } from "@/entities/schema/model/canvas-store";
import { relationKey } from "@/entities/schema/model/keys";
import {
  type ColumnRow,
  FOOTER_ROW_HEIGHT,
  type FooterRow,
  HEADER_HEIGHT,
  type NodeLayout,
} from "@/entities/schema/model/sizing";
import { BadgeList, columnBadges, KeyIcon } from "@/entities/schema/ui/column-badges";
import { KindIcon } from "@/entities/schema/ui/kind-icon";
import { useZoomDetail } from "@/entities/schema/ui/lod";
import { rowEstimateLabel } from "@/entities/schema/ui/row-estimate";
import { useTableNodeActions } from "@/entities/schema/ui/table-node-actions";
import type { Relation } from "@/shared/api";

/**
 * Edges anchor to `node.handles`, precomputed in sizing.ts together with
 * `measured`, so React Flow does not need to measure the DOM.
 */
export interface TableNodeData extends Record<string, unknown> {
  relation: Relation;
  layout: NodeLayout;
  collapsed: boolean;
  /** Handle ids some edge uses; only these get DOM anchors. */
  anchors: ReadonlySet<string>;
}

export type TableNodeType = Node<TableNodeData, "table" | "stub">;

const OVERLAY_TONE: Record<OverlayEntry["tone"], string> = {
  info: "text-status-info-fg border-status-info-fg",
  success: "text-status-success-fg border-status-success-fg",
  warn: "text-status-warn-fg border-status-warn-fg",
  danger: "text-status-danger-fg border-status-danger-fg",
};

/**
 * Invisible elements at the precomputed handle positions. React Flow still
 * re-measures a node now and then (when its wrapper mounts under
 * onlyRenderVisibleElements); without these it would find no handles and drop
 * the node's edges. Plain divs, not <Handle>, so they add no store
 * subscriptions. They are siblings of the card so the card border does not
 * shift them.
 */
function HandleAnchors({ layout, anchors }: { layout: NodeLayout; anchors: ReadonlySet<string> }) {
  // Each mount is measured with getBoundingClientRect per anchor, so only used handles get one.
  return layout.handles
    .filter((h) => anchors.has(h.id))
    .map((h) => (
      <div
        key={h.id}
        aria-hidden
        className={cn("pointer-events-none absolute size-px opacity-0", h.type)}
        data-handleid={h.id}
        data-handlepos={h.position}
        style={{ left: h.x, top: h.y }}
      />
    ));
}

const COLUMN_MARK_CLASS: Record<ColumnMark, string> = {
  endpoint: "bg-surface-selected font-semibold",
  match: "bg-status-info-subtle",
  "current-match":
    "bg-status-info-subtle font-semibold underline decoration-status-info-fg decoration-2 underline-offset-2",
};

function ColumnRowView({ row, mark }: { row: ColumnRow; mark: ColumnMark | undefined }) {
  const { column, flags } = row;
  return (
    <div
      data-column={column.name}
      data-mark={mark}
      title={column.comment || undefined}
      className={cn("flex h-6 items-center gap-1 px-2 text-xs", mark && COLUMN_MARK_CLASS[mark])}
    >
      <KeyIcon flags={flags} />
      <span className="min-w-0 truncate font-mono text-text-primary">{column.name}</span>
      <span className="ml-auto max-w-[40%] min-w-0 truncate pl-1 font-mono text-2xs text-text-muted">
        {column.type}
      </span>
      <BadgeList badges={columnBadges(flags, column.generatedKind)} />
    </div>
  );
}

function FooterView({ footer }: { footer: FooterRow }) {
  const text =
    footer.kind === "hidden"
      ? `+${footer.count} more column${footer.count === 1 ? "" : "s"}`
      : `${footer.count} partition${footer.count === 1 ? "" : "s"}${footer.key ? ` · ${footer.key}` : ""}`;
  return (
    <div
      className="flex items-center gap-1 border-t border-border-default px-2 text-2xs text-text-muted"
      style={{ height: FOOTER_ROW_HEIGHT }}
    >
      {footer.kind === "partitions" && <Layers aria-hidden className="size-3" />}
      <span className="truncate font-mono">{text}</span>
    </div>
  );
}

function Header({ id, data }: { id: string; data: TableNodeData }) {
  const { relation, collapsed } = data;
  const overlay = useCanvasStore((s) => s.overlay[id]);
  const { toggleCollapsed } = useTableNodeActions();
  const Chevron = collapsed ? ChevronRight : ChevronDown;
  return (
    <div
      className="flex items-center gap-2 border-b border-border-default bg-surface-subtle px-2"
      style={{ height: HEADER_HEIGHT }}
    >
      <KindIcon kind={relation.kind} />
      <div className="flex min-w-0 flex-1 flex-col">
        <span
          className="truncate font-mono text-xs font-semibold text-text-primary"
          title={relation.comment || relationKey(relation)}
        >
          {relation.name}
        </span>
        <span className="truncate text-2xs text-text-muted">
          {relation.schema} · <span className="font-mono">{rowEstimateLabel(relation.rowEstimate)}</span>
        </span>
      </div>
      {overlay && (
        <span
          data-overlay-tone={overlay.tone}
          className={cn("shrink-0 rounded-xs border px-1 text-2xs font-semibold", OVERLAY_TONE[overlay.tone])}
        >
          {overlay.label}
        </span>
      )}
      <button
        type="button"
        tabIndex={-1}
        aria-label={collapsed ? `Expand ${relation.name}` : `Collapse ${relation.name}`}
        title={collapsed ? "Show all columns" : "Show key columns only"}
        className="nodrag nopan flex size-5 shrink-0 items-center justify-center rounded-sm text-text-muted hover:bg-surface-hover hover:text-text-primary"
        onClick={(event) => {
          event.stopPropagation();
          toggleCollapsed(id);
        }}
      >
        <Chevron aria-hidden className="size-3.5" />
      </button>
    </div>
  );
}

const FRAME = "relative flex h-full w-full flex-col overflow-hidden rounded-md border bg-surface-raised";
const MARK_CLASS: Record<string, string> = {
  selected: "border-accent-fg ring-2 ring-accent-fg",
  "focus-root": "border-accent-fg ring-2 ring-accent-fg",
  "current-match": "border-status-info-fg ring-2 ring-status-info-fg",
  match: "border-status-info-fg outline-dashed outline-1 outline-offset-2 outline-status-info-fg",
  chain: "border-accent-fg",
};

function useVisual(id: string): { mark: string; dim: boolean } {
  const visual = useCanvasStore((s) => nodeVisual(s, id));
  const [mark = "", dim] = visual.split("|");
  return { mark, dim: dim === "dim" };
}

/** Far zoom: one text node per table, so 1,500 tables stay cheap to paint. */
interface FarNodeProps {
  id: string;
  relation: Relation;
  mark: string;
  dim: boolean;
  collapsed: boolean;
}

function FarNode({ id, relation, mark, dim, collapsed }: FarNodeProps) {
  return (
    <div
      data-testid="table-node"
      data-node-id={id}
      data-kind={relation.stub ? "stub" : relation.kind}
      data-mark={mark || undefined}
      data-dim={dim || undefined}
      data-detail="far"
      data-collapsed={collapsed || undefined}
      className={cn(
        "h-full w-full overflow-hidden rounded-md border border-border-control bg-surface-subtle px-2 pt-2 font-mono text-lg font-semibold text-text-primary",
        relation.stub && "border-dashed text-text-muted",
        MARK_CLASS[mark],
        dim && "opacity-25",
      )}
    >
      {relation.name}
    </div>
  );
}

function TableNodeView({ id, data }: NodeProps<TableNodeType>) {
  const zoom = useZoomDetail();
  const detailed = zoom === "full";
  const { mark, dim } = useVisual(id);
  const columnMarks = useCanvasStore((s) => s.derived.columnMarks.get(id));
  const { layout, relation } = data;
  if (zoom === "far") {
    return (
      <>
        <FarNode id={id} relation={relation} mark={mark} dim={dim} collapsed={data.collapsed} />
        <HandleAnchors layout={layout} anchors={data.anchors} />
      </>
    );
  }

  return (
    <>
      <div
        data-testid="table-node"
        data-node-id={id}
        data-kind={relation.kind}
        data-collapsed={data.collapsed || undefined}
        data-mark={mark || undefined}
        data-dim={dim || undefined}
        data-detail={detailed ? "full" : "header"}
        className={cn(FRAME, "border-border-control", MARK_CLASS[mark], dim && "opacity-25")}
      >
        <Header id={id} data={data} />
        {detailed ? (
          <>
            <div className="flex flex-col">
              {layout.rows.map((row) => (
                <ColumnRowView key={row.column.name} row={row} mark={columnMarks?.get(row.column.name)} />
              ))}
            </div>
            {layout.footers.map((f) => (
              <FooterView key={f.kind} footer={f} />
            ))}
          </>
        ) : (
          <div className="flex flex-1 items-center justify-center bg-surface-subtle text-2xs text-text-muted">
            {relation.columns?.length ?? 0} columns
          </div>
        )}
      </div>
      <HandleAnchors layout={layout} anchors={data.anchors} />
    </>
  );
}

function StubNodeView({ id, data }: NodeProps<TableNodeType>) {
  const { mark, dim } = useVisual(id);
  const far = useZoomDetail() === "far";
  const { relation } = data;
  if (far) {
    return (
      <>
        <FarNode id={id} relation={relation} mark={mark} dim={dim} collapsed={data.collapsed} />
        <HandleAnchors layout={data.layout} anchors={data.anchors} />
      </>
    );
  }
  return (
    <>
      <div
        data-testid="table-node"
        data-node-id={id}
        data-kind="stub"
        data-mark={mark || undefined}
        data-dim={dim || undefined}
        title={`${relationKey(relation)} is outside the selected schemas; only its name and primary key are known.`}
        className={cn(
          FRAME,
          "justify-center gap-0.5 border-dashed border-border-control bg-surface-subtle px-2",
          MARK_CLASS[mark],
          dim && "opacity-25",
        )}
      >
        <span className="flex items-center gap-1.5">
          <KindIcon kind={relation.kind} className="size-3 text-text-muted" />
          <span className="truncate font-mono text-xs text-text-muted">{relationKey(relation)}</span>
        </span>
        <span className="text-2xs text-text-muted">Outside selected schemas</span>
      </div>
      <HandleAnchors layout={data.layout} anchors={data.anchors} />
    </>
  );
}

/** Declared at module level and memoized, per React Flow's performance guidance. */
export const TableNode = memo(TableNodeView);
export const StubNode = memo(StubNodeView);

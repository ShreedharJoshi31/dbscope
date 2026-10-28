import type { Node, NodeProps } from "@xyflow/react";
import { cn } from "cn";
import {
  ChevronDown,
  ChevronRight,
  CircleCheck,
  Info,
  Layers,
  Link2,
  OctagonX,
  SquareMinus,
  SquarePen,
  SquarePlus,
  TriangleAlert,
} from "lucide-react";
import { memo } from "react";
import {
  type ColumnMark,
  type DiffChange,
  type DiffOverlay,
  nodeVisual,
  type OverlayEntry,
  useCanvasStore,
} from "@/entities/schema/model/canvas-store";
import { DIFF_CHANGE_META } from "@/entities/schema/model/diff-overlay";
import { worstLevel } from "@/entities/schema/model/findings";
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
import type { ColumnChangeKind, Relation } from "@/shared/api";

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

const OVERLAY_ICON = { info: Info, success: CircleCheck, warn: TriangleAlert, danger: OctagonX } as const;

/** Icon + text + token colour, never colour alone (UX rule 7); details in the tooltip and accessible name. */
function OverlayBadge({ entry }: { entry: OverlayEntry }) {
  const Icon = OVERLAY_ICON[entry.tone];
  const details = entry.details?.join("\n");
  return (
    <span
      data-testid="overlay-badge"
      data-overlay-tone={entry.tone}
      title={details || undefined}
      className={cn(
        "flex shrink-0 items-center gap-0.5 rounded-xs border px-1 text-2xs font-semibold",
        OVERLAY_TONE[entry.tone],
      )}
    >
      <Icon aria-hidden className="size-3" />
      {entry.label}
      {entry.intensity !== undefined && (
        // A heat bar; the label already states the number, so it is decorative.
        <span aria-hidden className="ml-0.5 h-1.5 w-6 overflow-hidden rounded-full bg-current/20">
          <span
            data-testid="overlay-intensity"
            className="block h-full rounded-full bg-current"
            style={{ width: `${Math.round(Math.max(0.08, Math.min(1, entry.intensity)) * 100)}%` }}
          />
        </span>
      )}
      {details && <span className="sr-only">. {entry.details?.join(". ")}</span>}
    </span>
  );
}

/** How many design risks the table has, toned by the worst; the messages are in the tooltip and accessible name. */
function RiskBadge({ relation }: { relation: TableNodeData["relation"] }) {
  const findings = relation.findings ?? [];
  const worst = worstLevel(findings);
  if (!worst) return null;
  const Icon = OVERLAY_ICON[worst];
  const lines = findings.map((f) => f.message);
  return (
    <span
      data-testid="risk-badge"
      data-level={worst}
      title={lines.join("\n")}
      className={cn(
        "flex shrink-0 items-center gap-0.5 rounded-xs border px-1 text-2xs font-semibold",
        OVERLAY_TONE[worst],
      )}
    >
      <Icon aria-hidden className="size-3" />
      {findings.length} risk{findings.length === 1 ? "" : "s"}
      <span className="sr-only">: {lines.join(". ")}</span>
    </span>
  );
}

const DIFF_ICON = { added: SquarePlus, altered: SquarePen, dropped: SquareMinus } as const;

type ColumnDiff = Readonly<Record<string, { change: ColumnChangeKind; to?: string | undefined }>>;

function columnDiffLines(columns: ColumnDiff | undefined): string[] {
  return Object.entries(columns ?? {}).map(([name, c]) =>
    c.change === "renamed" && c.to ? `Column ${name} renamed to ${c.to}` : `Column ${name} ${c.change}`,
  );
}

/** "added" / "altered" / "dropped" with its icon (UX rule 7); column changes in the tooltip and accessible name. */
function DiffBadge({ change, columns }: { change: DiffChange; columns: ColumnDiff | undefined }) {
  const meta = DIFF_CHANGE_META[change];
  const Icon = DIFF_ICON[change];
  const lines = [meta.description, ...columnDiffLines(columns)];
  return (
    <span
      data-testid="diff-badge"
      data-diff={change}
      title={lines.join("\n")}
      className={cn(
        "flex shrink-0 items-center gap-0.5 rounded-xs border bg-surface-canvas px-1 text-2xs font-semibold",
        OVERLAY_TONE[meta.tone],
      )}
    >
      <Icon aria-hidden className="size-3" />
      {meta.label}
      <span className="sr-only">. {lines.join(". ")}</span>
    </span>
  );
}

/** Marks a node as depending on the selected migration change, with its certainty. */
function ImpactChip({ heuristic }: { heuristic: boolean }) {
  const certainty = heuristic ? "heuristic" : "catalog";
  return (
    <span
      data-testid="impact-chip"
      data-certainty={certainty}
      title={`Depends on the selected change (${certainty}: ${heuristic ? "matched by name, may be wrong" : "recorded in the catalog"})`}
      className={cn(
        "flex shrink-0 items-center gap-0.5 rounded-xs border border-accent-fg bg-surface-canvas px-1 text-2xs font-semibold text-accent-fg",
        heuristic && "border-dashed",
      )}
    >
      <Link2 aria-hidden className="size-3" />
      {certainty}
      <span className="sr-only">. Depends on the selected change</span>
    </span>
  );
}

const DIFF_FRAME: Record<DiffChange, string> = {
  added: "border-status-success-fg",
  altered: "border-status-warn-fg",
  dropped: "border-dashed border-status-danger-fg",
};

const COLUMN_DIFF: Record<ColumnChangeKind, { className: string; tone: string; icon: typeof SquarePlus }> = {
  added: { className: "bg-status-success-subtle", tone: "text-status-success-fg", icon: SquarePlus },
  altered: { className: "bg-status-warn-subtle", tone: "text-status-warn-fg", icon: SquarePen },
  renamed: { className: "bg-status-warn-subtle", tone: "text-status-warn-fg", icon: SquarePen },
  dropped: { className: "bg-status-danger-subtle", tone: "text-status-danger-fg", icon: SquareMinus },
};

function ColumnDiffLabel({ change, to }: { change: ColumnChangeKind; to?: string | undefined }) {
  const meta = COLUMN_DIFF[change];
  const Icon = meta.icon;
  return (
    <span
      data-testid="column-diff"
      data-diff={change}
      className={cn(
        "ml-auto flex max-w-[55%] min-w-0 shrink-0 items-center gap-0.5 pl-1 text-2xs font-semibold",
        meta.tone,
      )}
    >
      <Icon aria-hidden className="size-3 shrink-0" />
      {change === "renamed" && to ? (
        <span className="truncate">
          renamed <span aria-hidden>→</span>
          <span className="sr-only">to</span> <span className="font-mono">{to}</span>
        </span>
      ) : (
        change
      )}
    </span>
  );
}

const MAX_ADDED_ROWS = 3;

/**
 * Columns the migration adds are not in the model, so they have no row. They
 * are drawn as a strip under the card (absolutely positioned: the node keeps
 * its size and the layout does not move).
 */
function AddedColumns({ columns }: { columns: ColumnDiff | undefined }) {
  const added = Object.entries(columns ?? {})
    .filter(([, c]) => c.change === "added")
    .map(([name]) => name);
  if (added.length === 0) return null;
  const shown = added.slice(0, MAX_ADDED_ROWS);
  return (
    <div
      data-testid="added-columns"
      className="pointer-events-none absolute inset-x-2 top-full flex flex-col rounded-b-md border border-t-0 border-dashed border-status-success-fg bg-surface-raised"
    >
      {shown.map((name) => (
        <div key={name} data-column={name} data-diff="added" className="flex h-6 items-center gap-1 px-2 text-xs">
          <span className="min-w-0 truncate font-mono text-text-primary">{name}</span>
          <ColumnDiffLabel change="added" />
        </div>
      ))}
      {added.length > shown.length && (
        <div className="px-2 pb-1 text-2xs text-text-muted">+{added.length - shown.length} more added</div>
      )}
    </div>
  );
}

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

interface ColumnRowProps {
  row: ColumnRow;
  mark: ColumnMark | undefined;
  diff: { change: ColumnChangeKind; to?: string | undefined } | undefined;
}

function ColumnRowView({ row, mark, diff }: ColumnRowProps) {
  const { column, flags } = row;
  return (
    <div
      data-column={column.name}
      data-mark={mark}
      data-diff={diff?.change}
      title={column.comment || undefined}
      className={cn(
        "flex h-6 items-center gap-1 px-2 text-xs",
        diff && COLUMN_DIFF[diff.change].className,
        mark && COLUMN_MARK_CLASS[mark],
      )}
    >
      <KeyIcon flags={flags} />
      <span
        className={cn(
          "min-w-0 truncate font-mono text-text-primary",
          diff?.change === "dropped" && "line-through decoration-status-danger-fg",
        )}
      >
        {column.name}
      </span>
      {diff ? (
        <ColumnDiffLabel change={diff.change} to={diff.to} />
      ) : (
        <>
          <span className="ml-auto max-w-[40%] min-w-0 truncate pl-1 font-mono text-2xs text-text-muted">
            {column.type}
          </span>
          <BadgeList badges={columnBadges(flags, column.generatedKind)} />
        </>
      )}
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

const diffOf = (s: { diff: DiffOverlay | null }, id: string) => s.diff?.relations[id];
const columnDiffOf = (s: { diff: DiffOverlay | null }, id: string) => s.diff?.columns[id];

function Header({ id, data, mark }: { id: string; data: TableNodeData; mark: string }) {
  const { relation, collapsed } = data;
  const overlay = useCanvasStore((s) => s.overlay[id]);
  const change = useCanvasStore((s) => diffOf(s, id));
  const columns = useCanvasStore((s) => columnDiffOf(s, id));
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
      {(mark === "impact" || mark === "impact-heuristic") && <ImpactChip heuristic={mark === "impact-heuristic"} />}
      {change && <DiffBadge change={change} columns={columns} />}
      {overlay && <OverlayBadge entry={overlay} />}
      {!overlay && <RiskBadge relation={relation} />}
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
  highlight: "border-accent-fg ring-1 ring-accent-fg",
  impact: "outline-2 outline-offset-2 outline-accent-fg",
  "impact-heuristic": "outline-2 outline-offset-2 outline-dashed outline-accent-fg",
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
  const change = useCanvasStore((s) => diffOf(s, id));
  return (
    <div
      data-testid="table-node"
      data-node-id={id}
      data-kind={relation.stub ? "stub" : relation.kind}
      data-mark={mark || undefined}
      data-dim={dim || undefined}
      data-diff={change}
      data-detail="far"
      data-collapsed={collapsed || undefined}
      className={cn(
        "h-full w-full overflow-hidden rounded-md border border-border-control bg-surface-subtle px-2 pt-2 font-mono text-lg font-semibold text-text-primary",
        relation.stub && "border-dashed text-text-muted",
        change && cn("border-4", DIFF_FRAME[change]),
        MARK_CLASS[mark],
        dim && "opacity-25",
      )}
    >
      {relation.name}
      {change && <span className="sr-only"> ({DIFF_CHANGE_META[change].label})</span>}
    </div>
  );
}

function TableNodeView({ id, data }: NodeProps<TableNodeType>) {
  const zoom = useZoomDetail();
  const detailed = zoom === "full";
  const { mark, dim } = useVisual(id);
  const columnMarks = useCanvasStore((s) => s.derived.columnMarks.get(id));
  const change = useCanvasStore((s) => diffOf(s, id));
  const columnDiff = useCanvasStore((s) => columnDiffOf(s, id));
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
        data-diff={change}
        data-detail={detailed ? "full" : "header"}
        className={cn(
          FRAME,
          "border-border-control",
          change && cn("border-2", DIFF_FRAME[change]),
          MARK_CLASS[mark],
          dim && "opacity-25",
        )}
      >
        <Header id={id} data={data} mark={mark} />
        {detailed ? (
          <>
            <div className="flex flex-col">
              {layout.rows.map((row) => (
                <ColumnRowView
                  key={row.column.name}
                  row={row}
                  mark={columnMarks?.get(row.column.name)}
                  diff={columnDiff?.[row.column.name]}
                />
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
      {detailed && !dim && <AddedColumns columns={columnDiff} />}
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

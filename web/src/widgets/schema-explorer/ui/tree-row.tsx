import { cn } from "cn";
import { ChevronDown, ChevronRight, Folder } from "lucide-react";
import { memo } from "react";
import { BadgeList, columnBadges, KeyIcon, KindIcon, rowEstimateLabel } from "@/entities/schema";
import { formatCompact } from "@/shared/lib/format-number";
import type { TreeRow, TreeRowPosition } from "@/widgets/schema-explorer/model/tree";

interface TreeRowViewProps {
  row: TreeRow;
  position: TreeRowPosition | undefined;
  active: boolean;
  selected: boolean;
  onClick: (row: TreeRow) => void;
  onToggle: (row: TreeRow) => void;
}

const INDENT = { 1: "pl-1", 2: "pl-5", 3: "pl-10" } as const;

function Twisty({ row, onToggle }: { row: TreeRow; onToggle: (row: TreeRow) => void }) {
  if (row.type === "column" || (row.type === "relation" && !row.hasChildren))
    return <span aria-hidden className="w-4 shrink-0" />;
  const Icon = row.expanded ? ChevronDown : ChevronRight;
  return (
    <span
      aria-hidden
      className="flex w-4 shrink-0 items-center justify-center text-text-muted"
      onClick={(e) => {
        e.stopPropagation();
        onToggle(row);
      }}
    >
      <Icon className="size-3.5" />
    </span>
  );
}

function Label({ row }: { row: TreeRow }) {
  if (row.type === "schema") {
    return (
      <>
        <Folder aria-hidden className="size-3.5 shrink-0 text-text-muted" strokeWidth={1.75} />
        <span className="truncate font-mono font-medium">{row.schema}</span>
        <span className="ml-auto pr-2 font-mono text-2xs text-text-muted tabular-nums">{row.count}</span>
      </>
    );
  }
  if (row.type === "relation") {
    const r = row.relation;
    return (
      <>
        <KindIcon kind={r.kind} />
        <span className={cn("truncate font-mono", r.stub && "text-text-muted")}>{r.name}</span>
        <span className="ml-auto shrink-0 pr-2 font-mono text-2xs text-text-muted">
          {r.stub ? "stub" : r.rowEstimate === null ? "unknown" : formatCompact(r.rowEstimate)}
        </span>
      </>
    );
  }
  const { column, flags } = row.row;
  return (
    <>
      <KeyIcon flags={flags} />
      <span className="truncate font-mono">{column.name}</span>
      <span className="ml-auto max-w-[45%] shrink truncate pl-1 font-mono text-2xs text-text-muted">{column.type}</span>
      <BadgeList badges={columnBadges(flags, column.generatedKind)} className="pr-2" />
    </>
  );
}

function ariaLabel(row: TreeRow): string {
  if (row.type === "schema") return `Schema ${row.schema}, ${row.count} relations`;
  if (row.type === "relation") {
    const r = row.relation;
    return `${r.name}, ${r.stub ? "outside the selected schemas" : `${r.kind.replace("_", " ")}, ${rowEstimateLabel(r.rowEstimate)}`}`;
  }
  const { column, flags } = row.row;
  const parts = [column.name, column.type];
  if (flags.pk) parts.push("primary key");
  if (flags.fk) parts.push("foreign key");
  if (flags.inferredFk) parts.push("inferred foreign key");
  if (flags.unique) parts.push("unique");
  if (flags.nullable) parts.push("nullable");
  return parts.join(", ");
}

function TreeRowViewImpl({ row, position, active, selected, onClick, onToggle }: TreeRowViewProps) {
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: keys are handled once on the tree (roving tabindex, use-tree-keyboard).
    <div
      role="treeitem"
      id={`explorer-${row.id}`}
      data-row-id={row.id}
      aria-level={row.level}
      aria-setsize={position?.setSize}
      aria-posinset={position?.posInSet}
      aria-expanded={row.type === "column" || (row.type === "relation" && !row.hasChildren) ? undefined : row.expanded}
      aria-selected={selected}
      aria-label={ariaLabel(row)}
      tabIndex={active ? 0 : -1}
      title={
        row.type === "relation" ? row.relation.comment : row.type === "column" ? row.row.column.comment : undefined
      }
      onClick={() => onClick(row)}
      className={cn(
        "flex h-6 cursor-default items-center gap-1 text-xs outline-none select-none",
        "hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus focus-visible:ring-inset",
        INDENT[row.level],
        selected && "bg-surface-selected",
      )}
    >
      <Twisty row={row} onToggle={onToggle} />
      <Label row={row} />
    </div>
  );
}

export const TreeRowView = memo(TreeRowViewImpl);

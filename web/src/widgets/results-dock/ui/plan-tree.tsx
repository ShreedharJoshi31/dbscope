import { cn } from "cn";
import { ChevronDown, ChevronRight } from "lucide-react";
import { actualTotalRows, nodeShare, nodeTitle } from "@/entities/query";
import type { PlanNode } from "@/shared/api";
import { formatCompact, formatGrouped } from "@/shared/lib/format-number";
import { StatusIcon } from "@/shared/ui/status-icon";

const FLAG_TONE = { info: "info", warn: "warn", danger: "danger" } as const;

function pct(share: number): string {
  return `${Math.round(share * 100)}%`;
}

function cost(n: number): string {
  return n >= 1000 ? formatGrouped(Math.round(n)) : n.toFixed(2);
}

/** A share bar with its number beside it, so the bar is never the only signal. */
function ShareBar({ share, label }: { share: number; label: string }) {
  return (
    <span className="flex items-center gap-1.5" title={`${pct(share)} of the plan's ${label}`}>
      <span aria-hidden className="h-1.5 w-20 overflow-hidden rounded-full bg-surface-hover">
        <span
          className={cn("block h-full rounded-full", share >= 0.5 ? "bg-status-danger-fg" : "bg-accent-fg")}
          style={{ width: pct(share) }}
        />
      </span>
      <span className="font-mono tabular-nums">
        {pct(share)} <span className="sr-only">of the plan's </span>
        {label}
      </span>
    </span>
  );
}

function Metrics({ node, analyzed }: { node: PlanNode; analyzed: boolean }) {
  const actual = actualTotalRows(node);
  return (
    <p className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-2xs text-text-muted">
      <span>
        est. <span className="font-mono text-text-primary tabular-nums">{formatCompact(node.planRows)}</span> rows
      </span>
      <span>
        cost{" "}
        <span className="font-mono tabular-nums">
          {cost(node.startupCost)}..{cost(node.totalCost)}
        </span>
      </span>
      {analyzed && actual !== undefined && (
        <span>
          actual <span className="font-mono text-text-primary tabular-nums">{formatCompact(actual)}</span> rows
          {(node.actualLoops ?? 1) > 1 && (
            <>
              {" "}
              over <span className="font-mono tabular-nums">{formatGrouped(node.actualLoops ?? 1)}</span> loops
            </>
          )}
          {node.actualTotalMs !== undefined && (
            <>
              , <span className="font-mono tabular-nums">{node.actualTotalMs.toFixed(2)} ms</span>
              {(node.actualLoops ?? 1) > 1 ? " per loop" : ""}
            </>
          )}
        </span>
      )}
      {analyzed && node.actualLoops === 0 && <span>never executed</span>}
      {node.rowsRemovedByFilter !== undefined && node.rowsRemovedByFilter > 0 && (
        <span>
          <span className="font-mono tabular-nums">{formatCompact(node.rowsRemovedByFilter)}</span> removed by filter
        </span>
      )}
      <ShareBar share={nodeShare({ ...node, timeShare: undefined }, false)} label="cost" />
      {analyzed && node.timeShare !== undefined && <ShareBar share={nodeShare(node, true)} label="time" />}
    </p>
  );
}

interface PlanTreeNodeProps {
  node: PlanNode;
  id: string;
  depth: number;
  analyzed: boolean;
  collapsed: ReadonlySet<string>;
  onToggle: (id: string) => void;
}

export function PlanTreeNode({ node, id, depth, analyzed, collapsed, onToggle }: PlanTreeNodeProps) {
  const children = node.children ?? [];
  const open = !collapsed.has(id);
  const title = nodeTitle(node);
  const Chevron = open ? ChevronDown : ChevronRight;
  return (
    <li data-testid="plan-node" data-node-type={node.nodeType} className="flex flex-col">
      <div
        className="flex items-start gap-1 rounded-sm py-1 pr-2 hover:bg-surface-hover"
        style={{ paddingLeft: depth * 16 }}
      >
        {children.length > 0 ? (
          <button
            type="button"
            aria-expanded={open}
            aria-label={`${open ? "Collapse" : "Expand"} ${title}`}
            onClick={() => onToggle(id)}
            className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-xs text-text-muted hover:text-text-primary"
          >
            <Chevron aria-hidden className="size-3.5" />
          </button>
        ) : (
          <span aria-hidden className="size-4 shrink-0" />
        )}
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="font-mono text-xs font-semibold text-text-primary">{title}</span>
          {node.condition && (
            <span className="truncate font-mono text-2xs text-text-muted" title={node.condition}>
              {node.condition}
            </span>
          )}
          <Metrics node={node} analyzed={analyzed} />
          {(node.flags ?? []).map((f) => (
            <span key={f.code + f.message} data-flag={f.code} className="flex items-start gap-1 text-xs">
              <StatusIcon tone={FLAG_TONE[f.level]} className="mt-0.5 size-3.5" />
              {f.message}
            </span>
          ))}
        </div>
      </div>
      {open && children.length > 0 && (
        <ul className="flex flex-col">
          {children.map((child, i) => {
            // Plan nodes have no id; the path from the root is their identity.
            const childId = `${id}.${i}`;
            return (
              <PlanTreeNode
                key={childId}
                node={child}
                id={childId}
                depth={depth + 1}
                analyzed={analyzed}
                collapsed={collapsed}
                onToggle={onToggle}
              />
            );
          })}
        </ul>
      )}
    </li>
  );
}

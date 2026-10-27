import { ViewportPortal } from "@xyflow/react";
import { memo, useMemo } from "react";
import type { TableNodeType } from "@/entities/schema";
import type { RelationshipEdgeType } from "@/widgets/erd-canvas/model/elements";

interface FarEdgesProps {
  nodes: readonly TableNodeType[];
  edges: readonly RelationshipEdgeType[];
}

/** Straight segments between facing sides, one `M..L..` per foreign key. */
export function farEdgesPath(nodes: readonly TableNodeType[], edges: readonly RelationshipEdgeType[]): string {
  const byId = new Map(nodes.map((n): [string, TableNodeType] => [n.id, n]));
  const parts: string[] = [];
  for (const e of edges) {
    if (e.data && e.data.pairIndex > 0) continue;
    const child = byId.get(e.source);
    const parent = byId.get(e.target);
    if (!child || !parent) continue;
    const cw = child.width ?? 0;
    const pw = parent.width ?? 0;
    const cy = child.position.y + 22;
    const py = parent.position.y + 22;
    const childLeftOfParent = child.position.x + cw / 2 < parent.position.x + pw / 2;
    const cx = childLeftOfParent ? child.position.x + cw : child.position.x;
    const px = childLeftOfParent ? parent.position.x : parent.position.x + pw;
    parts.push(`M${Math.round(cx)} ${Math.round(cy)}L${Math.round(px)} ${Math.round(py)}`);
  }
  return parts.join("");
}

/**
 * Overview-zoom relationships: every foreign key in a single SVG path inside
 * the viewport. React Flow edges each subscribe to its store and recompute on
 * every pan frame; at 3,000 edges that alone costs ~30 ms a frame, while one
 * path costs one paint. The per-edge components come back above the threshold.
 */
function FarEdgesView({ nodes, edges }: FarEdgesProps) {
  const d = useMemo(() => farEdgesPath(nodes, edges), [nodes, edges]);
  return (
    <ViewportPortal>
      <svg aria-hidden className="pointer-events-none absolute top-0 left-0 size-px overflow-visible" focusable="false">
        <path
          d={d}
          fill="none"
          className="stroke-(--erd-edge-stroke)"
          strokeWidth={2}
          vectorEffect="non-scaling-stroke"
        />
      </svg>
    </ViewportPortal>
  );
}

export const FarEdges = memo(FarEdgesView);

import type { XYPosition } from "@xyflow/react";
import type { LayoutNode } from "@/widgets/erd-canvas/model/elements";

/**
 * Places nodes that have no saved position in a column to the right of the
 * saved ones, so a table added since the layout was saved shows up without
 * moving anything the user arranged.
 */
export function placeMissing(
  nodes: readonly LayoutNode[],
  known: ReadonlyMap<string, XYPosition>,
): Map<string, XYPosition> {
  const out = new Map<string, XYPosition>();
  let maxX = Number.NEGATIVE_INFINITY;
  let minY = Number.POSITIVE_INFINITY;
  const widths = new Map(nodes.map((n): [string, number] => [n.id, n.width]));
  for (const [id, p] of known) {
    if (!widths.has(id)) continue;
    out.set(id, p);
    maxX = Math.max(maxX, p.x + (widths.get(id) ?? 0));
    minY = Math.min(minY, p.y);
  }
  if (out.size === 0) {
    maxX = -96;
    minY = 0;
  }
  let y = minY;
  for (const n of nodes) {
    if (out.has(n.id)) continue;
    out.set(n.id, { x: maxX + 96, y });
    y += n.height + 32;
  }
  return out;
}

/** True when every node has a saved position. */
export function coversAll(nodes: readonly LayoutNode[], known: ReadonlyMap<string, XYPosition>): boolean {
  return nodes.every((n) => known.has(n.id));
}

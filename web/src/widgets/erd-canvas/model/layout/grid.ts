import type { XYPosition } from "@xyflow/react";
import type { LayoutNode } from "@/widgets/erd-canvas/model/elements";
import type { LayoutLink } from "@/widgets/erd-canvas/model/layout/types";

const GAP_X = 64;
const GAP_Y = 32;

/** Orders nodes so tables joined by foreign keys sit near each other: schema, then a BFS over links. */
function orderNodes(nodes: LayoutNode[], links: LayoutLink[], groups: Record<string, string>): LayoutNode[] {
  const byId = new Map(nodes.map((n): [string, LayoutNode] => [n.id, n]));
  const adjacent = new Map<string, string[]>();
  for (const { source, target } of links) {
    if (source === target) continue;
    adjacent.set(source, [...(adjacent.get(source) ?? []), target]);
    adjacent.set(target, [...(adjacent.get(target) ?? []), source]);
  }
  const schemas = [...new Set(nodes.map((n) => groups[n.id] ?? ""))];
  const out: LayoutNode[] = [];
  const seen = new Set<string>();
  for (const schema of schemas) {
    for (const start of nodes) {
      if (seen.has(start.id) || (groups[start.id] ?? "") !== schema) continue;
      const queue = [start.id];
      seen.add(start.id);
      while (queue.length > 0) {
        const id = queue.shift() as string;
        const node = byId.get(id);
        if (node) out.push(node);
        for (const next of adjacent.get(id) ?? []) {
          if (seen.has(next) || (groups[next] ?? "") !== schema) continue;
          seen.add(next);
          queue.push(next);
        }
      }
    }
  }
  return out;
}

/**
 * Column-major masonry: nodes fill columns top to bottom up to a height that
 * makes the whole grid roughly 16:10. Deterministic and O(n), so it is the
 * default for very large schemas.
 */
export function gridLayout(
  nodes: LayoutNode[],
  links: LayoutLink[] = [],
  groups: Record<string, string> = {},
): Record<string, XYPosition> {
  const ordered = orderNodes(nodes, links, groups);
  const area = ordered.reduce((sum, n) => sum + (n.width + GAP_X) * (n.height + GAP_Y), 0);
  const maxHeight = Math.max(400, Math.sqrt(area / 1.6));
  const positions: Record<string, XYPosition> = {};
  let x = 0;
  let y = 0;
  let columnWidth = 0;
  for (const node of ordered) {
    if (y > 0 && y + node.height > maxHeight) {
      x += columnWidth + GAP_X;
      y = 0;
      columnWidth = 0;
    }
    positions[node.id] = { x, y };
    y += node.height + GAP_Y;
    columnWidth = Math.max(columnWidth, node.width);
  }
  return positions;
}

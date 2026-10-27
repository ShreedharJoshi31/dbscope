import type { ElkExtendedEdge, ElkNode, ElkPort } from "elkjs/lib/elk-api.js";
import { HEADER_HANDLE, type NodeLayout, nodeLayout, relationKey, type SchemaGraph } from "@/entities/schema";

/**
 * ELK layered options for ERDs (docs: eclipse.dev/elk/reference). Parents
 * end up left of their children (edges run parent -> child), ports keep
 * column order, and model order keeps the result stable across runs.
 */
export const ELK_LAYOUT_OPTIONS: Record<string, string> = {
  "elk.algorithm": "layered",
  "elk.direction": "RIGHT",
  "elk.edgeRouting": "ORTHOGONAL",
  "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
  "elk.layered.spacing.nodeNodeBetweenLayers": "96",
  "elk.spacing.nodeNode": "40",
  "elk.spacing.edgeNode": "24",
  "elk.layered.spacing.edgeNodeBetweenLayers": "24",
  "elk.layered.highDegreeNodes.treatment": "true",
  "elk.layered.cycleBreaking.strategy": "GREEDY",
  "elk.layered.thoroughness": "7",
  "elk.separateConnectedComponents": "true",
  "elk.spacing.componentComponent": "80",
  "elk.aspectRatio": "1.6",
};

function portId(node: string, handle: string, side: "E" | "W"): string {
  return `${node}::${handle}::${side}`;
}

/**
 * One port per visible column on each side, plus one on the header. FIXED_ORDER
 * with explicit indices: ELK numbers ports clockwise from the top-left, so
 * east ports count down the card and west ports count back up it.
 */
function ports(id: string, layout: NodeLayout): ElkPort[] {
  const rows = layout.handles.filter((h) => h.type === "source");
  const count = rows.length;
  const east: ElkPort[] = rows.map((h, i) => ({
    id: portId(id, h.id.slice(0, h.id.lastIndexOf(":")), "E"),
    x: layout.width,
    y: h.y,
    width: 1,
    height: 1,
    layoutOptions: { "elk.port.side": "EAST", "elk.port.index": String(i) },
  }));
  const west: ElkPort[] = rows.map((h, i) => ({
    id: portId(id, h.id.slice(0, h.id.lastIndexOf(":")), "W"),
    x: -1,
    y: h.y,
    width: 1,
    height: 1,
    layoutOptions: { "elk.port.side": "WEST", "elk.port.index": String(count + (count - 1 - i)) },
  }));
  return [...east, ...west];
}

function anchor(layout: NodeLayout, column: string): string {
  return layout.visible.has(column) ? column : HEADER_HANDLE;
}

export function toElkGraph(graph: SchemaGraph, collapsed: ReadonlySet<string>): ElkNode {
  const layouts = new Map<string, NodeLayout>();
  const children: ElkNode[] = graph.relations.map((r) => {
    const id = relationKey(r);
    const layout = nodeLayout(r, collapsed.has(id));
    layouts.set(id, layout);
    return {
      id,
      width: layout.width,
      height: layout.height,
      ports: ports(id, layout),
      layoutOptions: { "elk.portConstraints": "FIXED_ORDER" },
    };
  });

  const edges: ElkExtendedEdge[] = [];
  for (const e of graph.edges) {
    const childLayout = layouts.get(e.child);
    const parentLayout = layouts.get(e.parent);
    if (!childLayout || !parentLayout) continue;
    e.pairs.forEach((pair, i) => {
      edges.push({
        id: `${e.id}#${i}`,
        sources: [portId(e.parent, anchor(parentLayout, pair.refColumn), "E")],
        targets: [portId(e.child, anchor(childLayout, pair.column), "W")],
      });
    });
  }
  return { id: "root", layoutOptions: ELK_LAYOUT_OPTIONS, children, edges };
}

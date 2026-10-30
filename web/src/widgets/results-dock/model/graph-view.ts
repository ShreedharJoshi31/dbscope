import type { CellValue, GraphNode, GraphRelationship, ResultGraph } from "@/shared/api";

/** Adds an expansion to a drawn graph: new nodes and relationships once, stubs replaced by full nodes. */
export function mergeGraph(base: ResultGraph, add: ResultGraph): ResultGraph {
  const nodes = new Map(base.nodes.map((n) => [n.id, n]));
  for (const n of add.nodes) {
    const had = nodes.get(n.id);
    if (!had || (had.stub && !n.stub)) nodes.set(n.id, n);
  }
  const rels = new Map(base.relationships.map((r) => [r.id, r]));
  for (const r of add.relationships) if (!rels.has(r.id)) rels.set(r.id, r);
  return { nodes: [...nodes.values()], relationships: [...rels.values()], truncated: base.truncated || add.truncated };
}

/** Properties that usually name a node, in the order tried for its caption. */
const NAME_PROPS = ["name", "title", "label", "email", "username", "key", "id", "code", "uuid"];

function text(v: CellValue | undefined): string | undefined {
  if (v === null || v === undefined || typeof v === "object") return undefined;
  return String(v);
}

/** A short caption for a node: a naming property, else its first scalar one, else its label. */
export function caption(node: GraphNode): string {
  if (node.stub) return "not returned";
  for (const k of NAME_PROPS) {
    const t = text(node.props[k]);
    if (t) return t;
  }
  for (const k of Object.keys(node.props).sort()) {
    const t = text(node.props[k]);
    if (t) return t;
  }
  return node.labels[0] ?? "node";
}

export const GRAPH_COLORS = 6;

/** A stable colour slot (1..6) per label, in order of first appearance. */
export function labelColors(nodes: GraphNode[]): Map<string, number> {
  const out = new Map<string, number>();
  for (const n of nodes) {
    const l = n.labels[0];
    if (l && !out.has(l)) out.set(l, (out.size % GRAPH_COLORS) + 1);
  }
  return out;
}

/** How many relationships of the drawn graph touch each node. */
export function drawnDegrees(relationships: GraphRelationship[]): Map<string, number> {
  const out = new Map<string, number>();
  for (const r of relationships) {
    out.set(r.start, (out.get(r.start) ?? 0) + 1);
    if (r.end !== r.start) out.set(r.end, (out.get(r.end) ?? 0) + 1);
  }
  return out;
}

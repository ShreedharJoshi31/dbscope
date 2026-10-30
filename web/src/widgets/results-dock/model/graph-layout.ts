import {
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  forceX,
  forceY,
  type SimulationNodeDatum,
} from "d3-force";
import type { ResultGraph } from "@/shared/api";

export interface Point {
  x: number;
  y: number;
}

interface SimNode extends SimulationNodeDatum {
  id: string;
}

/** Room each drawn node needs: its circle and the caption under it. */
export const NODE_SPACING = 48;

/**
 * Force layout of node centres. Nodes already placed stay where they are, so
 * expanding a node only arranges its new neighbours around it; those start
 * on a ring around a placed neighbour rather than at the origin.
 */
export function layoutGraph(
  graph: ResultGraph,
  placed: Record<string, Point> = {},
  ticks = 300,
): Record<string, Point> {
  const neighbour = new Map<string, string>();
  for (const r of graph.relationships) {
    if (placed[r.start] && !placed[r.end]) neighbour.set(r.end, r.start);
    if (placed[r.end] && !placed[r.start]) neighbour.set(r.start, r.end);
  }
  const fresh = graph.nodes.filter((n) => !placed[n.id]);
  const columns = Math.ceil(Math.sqrt(fresh.length || 1));
  const sim: SimNode[] = graph.nodes.map((n) => {
    const at = placed[n.id];
    if (at) return { id: n.id, x: at.x, y: at.y, fx: at.x, fy: at.y };
    const i = fresh.indexOf(n);
    const around = placed[neighbour.get(n.id) ?? ""];
    if (around) {
      const angle = (2 * Math.PI * i) / fresh.length;
      return { id: n.id, x: around.x + 110 * Math.cos(angle), y: around.y + 110 * Math.sin(angle) };
    }
    return { id: n.id, x: (i % columns) * 100, y: Math.floor(i / columns) * 100 };
  });
  const ids = new Set(sim.map((n) => n.id));
  const links = graph.relationships
    .filter((r) => r.start !== r.end && ids.has(r.start) && ids.has(r.end))
    .map((r) => ({ source: r.start, target: r.end }));

  const simulation = forceSimulation<SimNode>(sim)
    .force(
      "link",
      forceLink<SimNode, { source: string; target: string }>(links)
        .id((d) => d.id)
        .distance(90)
        .strength(0.6),
    )
    .force("charge", forceManyBody<SimNode>().strength(-260).distanceMax(700))
    .force("collide", forceCollide<SimNode>(NODE_SPACING).iterations(2))
    // Weak pulls to the middle keep unconnected pieces from drifting off.
    .force("x", forceX<SimNode>(0).strength(0.03))
    .force("y", forceY<SimNode>(0).strength(0.03))
    .stop();
  for (let i = 0; i < ticks; i++) simulation.tick();

  const out: Record<string, Point> = {};
  for (const n of sim) out[n.id] = { x: Math.round(n.x ?? 0), y: Math.round(n.y ?? 0) };
  return out;
}

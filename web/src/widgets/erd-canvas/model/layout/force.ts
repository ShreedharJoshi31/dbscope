import type { XYPosition } from "@xyflow/react";
import {
  forceCenter,
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  type SimulationNodeDatum,
} from "d3-force";
import type { LayoutNode } from "@/widgets/erd-canvas/model/elements";
import type { LayoutLink } from "@/widgets/erd-canvas/model/layout/types";

interface SimNode extends SimulationNodeDatum {
  id: string;
  width: number;
  height: number;
}

const PADDING = 16;

/**
 * Force-directed layout, run to completion synchronously (call it in a
 * worker). Collision uses each card's half-diagonal, so cards never overlap;
 * positions are returned as top-left corners like React Flow expects.
 */
export function forceLayout(nodes: LayoutNode[], links: LayoutLink[], ticks = 300): Record<string, XYPosition> {
  const columns = Math.ceil(Math.sqrt(nodes.length));
  const sim: SimNode[] = nodes.map((n, i) => ({
    ...n,
    // Deterministic start on a grid; d3's default phyllotaxis would also work but packs tall cards tightly.
    x: (i % columns) * 340,
    y: Math.floor(i / columns) * 260,
  }));
  const ids = new Set(nodes.map((n) => n.id));
  const simLinks = links.filter((l) => l.source !== l.target && ids.has(l.source) && ids.has(l.target));

  const simulation = forceSimulation<SimNode>(sim)
    .force(
      "link",
      forceLink<SimNode, { source: string; target: string }>(simLinks.map((l) => ({ ...l })))
        .id((d) => d.id)
        .distance(260)
        .strength(0.4),
    )
    .force("charge", forceManyBody<SimNode>().strength(-900).distanceMax(2000))
    .force("collide", forceCollide<SimNode>((d) => Math.hypot(d.width, d.height) / 2 + PADDING).iterations(2))
    .force("center", forceCenter(0, 0))
    .stop();

  for (let i = 0; i < ticks; i++) simulation.tick();

  const positions: Record<string, XYPosition> = {};
  for (const n of sim) {
    positions[n.id] = { x: Math.round((n.x ?? 0) - n.width / 2), y: Math.round((n.y ?? 0) - n.height / 2) };
  }
  return positions;
}

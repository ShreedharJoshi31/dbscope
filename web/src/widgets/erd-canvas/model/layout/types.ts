import type { XYPosition } from "@xyflow/react";
import type { LayoutNode } from "@/widgets/erd-canvas/model/elements";

export type LayoutAlgorithm = "layered" | "force" | "grid";

export const LAYOUT_LABELS: Record<LayoutAlgorithm, string> = {
  layered: "Layered",
  force: "Force",
  grid: "Grid",
};

/** ELK layered takes about a minute for 1,500 tables (measured in Node), so large schemas start on the grid. */
export const LAYERED_DEFAULT_LIMIT = 400;

export function defaultAlgorithm(nodeCount: number): LayoutAlgorithm {
  return nodeCount <= LAYERED_DEFAULT_LIMIT ? "layered" : "grid";
}

export interface LayoutLink {
  /** Parent (referenced) relation. */
  source: string;
  /** Child relation holding the foreign key. */
  target: string;
}

/** What the module worker receives for force and grid layouts. */
export interface SimpleLayoutRequest {
  id: number;
  algorithm: "force" | "grid";
  nodes: LayoutNode[];
  links: LayoutLink[];
  /** Schema of each node, so the grid can keep schemas together. */
  groups: Record<string, string>;
}

export interface SimpleLayoutResponse {
  id: number;
  positions: Record<string, XYPosition>;
  error?: string;
}

export interface LayoutResult {
  positions: Map<string, XYPosition>;
  ms: number;
  algorithm: LayoutAlgorithm;
}

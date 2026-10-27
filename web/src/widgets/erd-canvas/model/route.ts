import { getSmoothStepPath, Position } from "@xyflow/react";

export interface RouteInput {
  /** Right-hand handle of the child column. */
  sourceX: number;
  sourceY: number;
  /** Left-hand handle of the parent column. */
  targetX: number;
  targetY: number;
  childWidth: number;
  parentWidth: number;
  lane: number;
  laneCount: number;
  pairIndex: number;
  selfRef: boolean;
}

export interface Route {
  path: string;
  labelX: number;
  labelY: number;
  childSide: "left" | "right";
  parentSide: "left" | "right";
}

export const LANE_GAP = 14;
const PAIR_GAP = 4;
const LOOP_OFFSET = 24;
const RADIUS = 6;

/**
 * Picks the sides an edge leaves and enters by from where the two tables are,
 * the way ChartDB does at render time: facing sides when the tables are apart
 * horizontally, both right sides when they overlap (and for self references,
 * which loop out to the right). Parallel foreign keys get separate vertical
 * channels; the column pairs of one composite key share a channel.
 */
export function routeEdge(input: RouteInput): Route {
  const { sourceX, sourceY, targetX, targetY, childWidth, parentWidth } = input;
  const childLeft = sourceX - childWidth;
  const parentRight = targetX + parentWidth;
  const spread = (input.lane - (input.laneCount - 1) / 2) * LANE_GAP + input.pairIndex * PAIR_GAP;

  let sx = sourceX;
  let tx = targetX;
  let childSide: Route["childSide"] = "right";
  let parentSide: Route["parentSide"] = "left";

  if (input.selfRef || (childLeft < parentRight && sourceX > targetX)) {
    // Overlapping horizontally, or the same table: loop around the right.
    tx = parentRight;
    parentSide = "right";
  } else if (childLeft >= parentRight) {
    // Child is to the right of the parent: leave left, enter right.
    sx = childLeft;
    tx = parentRight;
    childSide = "left";
    parentSide = "right";
  }

  const facing = childSide !== parentSide;
  const [path, labelX, labelY] = getSmoothStepPath({
    sourceX: sx,
    sourceY,
    sourcePosition: childSide === "right" ? Position.Right : Position.Left,
    targetX: tx,
    targetY,
    targetPosition: parentSide === "right" ? Position.Right : Position.Left,
    borderRadius: RADIUS,
    offset: facing ? 16 : LOOP_OFFSET + Math.abs(spread),
    ...(facing ? { centerX: (sx + tx) / 2 + spread } : {}),
  });
  return { path, labelX, labelY, childSide, parentSide };
}

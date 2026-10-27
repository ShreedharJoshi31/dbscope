import type { XYPosition } from "@xyflow/react";

export type Direction = "up" | "down" | "left" | "right";

export interface Box {
  id: string;
  position: XYPosition;
  width: number;
  height: number;
}

function centre(b: Box): XYPosition {
  return { x: b.position.x + b.width / 2, y: b.position.y + b.height / 2 };
}

/**
 * The table an arrow key moves to: the nearest one whose centre lies in that
 * direction, within a 45° cone either side; distance off the axis counts
 * double so the move stays in a straight line where possible.
 */
export function nearestInDirection(boxes: readonly Box[], fromId: string, direction: Direction): string | null {
  const from = boxes.find((b) => b.id === fromId);
  if (!from) return null;
  const o = centre(from);
  let best: string | null = null;
  let bestScore = Number.POSITIVE_INFINITY;
  for (const b of boxes) {
    if (b.id === fromId) continue;
    const c = centre(b);
    const dx = c.x - o.x;
    const dy = c.y - o.y;
    const along = direction === "left" ? -dx : direction === "right" ? dx : direction === "up" ? -dy : dy;
    const across = direction === "left" || direction === "right" ? Math.abs(dy) : Math.abs(dx);
    if (along <= 0 || across > along) continue;
    const score = along + across * 2;
    if (score < bestScore) {
      bestScore = score;
      best = b.id;
    }
  }
  return best;
}

/** Where keyboard navigation starts when nothing is selected: the top-left table. */
export function firstInReadingOrder(boxes: readonly Box[]): string | null {
  let best: Box | null = null;
  for (const b of boxes) {
    if (!best || b.position.y + b.position.x * 0.5 < best.position.y + best.position.x * 0.5) best = b;
  }
  return best?.id ?? null;
}

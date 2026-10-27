import type { CanvasLayoutInput } from "@/shared/api";

export const SAVE_DEBOUNCE_MS = 1000;

export interface LayoutSaver {
  /** Replaces the pending snapshot and restarts the timer. */
  schedule: (layout: CanvasLayoutInput) => void;
  /** Saves the pending snapshot now, if any. */
  flush: () => void;
  cancel: () => void;
}

/**
 * Debounces layout saves: a drag, collapse or relayout schedules a save one
 * second after the last change, so a burst of edits costs one PUT /layout.
 */
export function createLayoutSaver(
  save: (layout: CanvasLayoutInput) => void,
  delayMs: number = SAVE_DEBOUNCE_MS,
): LayoutSaver {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let pending: CanvasLayoutInput | null = null;

  const fire = () => {
    timer = null;
    const layout = pending;
    pending = null;
    if (layout) save(layout);
  };

  return {
    schedule(layout) {
      pending = layout;
      if (timer !== null) clearTimeout(timer);
      timer = setTimeout(fire, delayMs);
    },
    flush() {
      if (timer !== null) clearTimeout(timer);
      fire();
    },
    cancel() {
      if (timer !== null) clearTimeout(timer);
      timer = null;
      pending = null;
    },
  };
}

/** Rounds positions so the saved JSON stays small and stable. */
export function snapshotLayout(
  key: string,
  positions: Iterable<[string, { x: number; y: number }]>,
  collapsed: Iterable<string>,
): CanvasLayoutInput {
  const out: Record<string, { x: number; y: number }> = {};
  for (const [id, p] of positions) out[id] = { x: Math.round(p.x), y: Math.round(p.y) };
  return { key, positions: out, collapsed: [...collapsed].sort() };
}

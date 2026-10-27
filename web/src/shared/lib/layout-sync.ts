import { create } from "zustand";

type Flusher = () => Promise<void>;

const flushers = new Set<Flusher>();

/**
 * Lets a canvas save its pending layout on demand and then stop saving until
 * it remounts (useLayoutEpoch); returns the unregister function.
 */
export function registerLayoutFlusher(flush: Flusher): () => void {
  flushers.add(flush);
  return () => flushers.delete(flush);
}

/** Saves every canvas's pending layout, waits for the saves, and freezes the canvases. */
export async function flushLayoutSaves(): Promise<void> {
  await Promise.all([...flushers].map((f) => f()));
}

interface LayoutEpoch {
  epoch: number;
  /** Remounts canvases so they read their saved layout again (after an import replaced it). */
  bump: () => void;
}

export const useLayoutEpoch = create<LayoutEpoch>((set) => ({
  epoch: 0,
  bump: () => set((s) => ({ epoch: s.epoch + 1 })),
}));

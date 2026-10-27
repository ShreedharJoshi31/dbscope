import { type DiffChange, type DiffOverlay, useCanvasStore } from "@/entities/schema/model/canvas-store";

/**
 * Shows a migration's schema diff on the canvas. Writes only the canvas
 * store, never React Flow's nodes or edges, so it cannot trigger a relayout.
 * New relations are not added to the canvas; callers list them instead.
 */
export function showMigrationDiff(diff: DiffOverlay): void {
  useCanvasStore.getState().setDiff(diff);
}

export function clearMigrationOverlay(): void {
  useCanvasStore.getState().setDiff(null);
}

export function hasMigrationOverlay(s: { diff: DiffOverlay | null }): boolean {
  return s.diff !== null;
}

export interface DiffChangeMeta {
  /** Lower case, as the badge reads: "added", "altered", "dropped". */
  label: string;
  tone: "success" | "warn" | "danger";
  description: string;
}

export const DIFF_CHANGE_META: Record<DiffChange, DiffChangeMeta> = {
  added: { label: "added", tone: "success", description: "Created by the migration" },
  altered: { label: "altered", tone: "warn", description: "Changed by the migration" },
  dropped: { label: "dropped", tone: "danger", description: "Dropped by the migration" },
};

/** Changed relations on the canvas, and the ones (usually new tables) that are not. */
export function partitionDiffRelations(
  diff: Pick<DiffOverlay, "relations">,
  onCanvas: (key: string) => boolean,
): { shown: string[]; missing: string[] } {
  const shown: string[] = [];
  const missing: string[] = [];
  for (const key of Object.keys(diff.relations).sort()) (onCanvas(key) ? shown : missing).push(key);
  return { shown, missing };
}

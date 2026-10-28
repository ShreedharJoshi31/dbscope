import { create } from "zustand";
import { useRunStore } from "@/entities/query";
import { type Drift, diffModels, useCanvasStore } from "@/entities/schema";
import { usePanelStore } from "@/features/toggle-panel";
import type { ColumnChangeKind, Model } from "@/shared/api";

export const DRIFT_SOURCE = "drift";

export interface Comparison {
  /** "the snapshot from 3 days ago" or "staging-orders". */
  label: string;
  drift: Drift;
}

interface CompareState {
  open: boolean;
  comparison: Comparison | null;
  setOpen: (open: boolean) => void;
}

export const useCompareStore = create<CompareState>((set) => ({
  open: false,
  comparison: null,
  setOpen: (open) => set({ open }),
}));

export function openCompare(): void {
  useCompareStore.getState().setOpen(true);
}

/** Diffs this connection's model with another, shows it on the canvas and opens the Drift tab. */
export function showComparison(here: Model, there: Model, label: string): Drift {
  const drift = diffModels(here, there);
  const columns: Record<string, Record<string, { change: ColumnChangeKind }>> = {};
  for (const [rel, cols] of Object.entries(drift.columns)) {
    columns[rel] = Object.fromEntries(Object.entries(cols).map(([c, v]) => [c, { change: v.change }]));
  }
  const relations = Object.fromEntries(
    Object.entries(drift.relations).filter(([, c]) => c !== "not-visible") as [
      string,
      "added" | "dropped" | "altered",
    ][],
  );
  useCanvasStore.getState().setDiff({ sourceId: DRIFT_SOURCE, label: `vs ${label}`, relations, columns, impact: {} });
  useCompareStore.setState({ comparison: { label, drift }, open: false });
  useRunStore.getState().setDockTab("drift");
  usePanelStore.getState().setOpen("dock", true);
  return drift;
}

export function clearComparison(): void {
  const canvas = useCanvasStore.getState();
  if (canvas.diff?.sourceId === DRIFT_SOURCE) canvas.setDiff(null);
  useCompareStore.setState({ comparison: null });
}

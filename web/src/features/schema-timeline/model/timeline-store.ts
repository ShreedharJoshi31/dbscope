import { create } from "zustand";
import { compareStep, type DiffOverlay, type StepChanges, type TimelineStep, useCanvasStore } from "@/entities/schema";
import type { ColumnChangeKind, Model } from "@/shared/api";

export const TIMELINE_SOURCE = "timeline";

/** A past step on the canvas, in place of the live schema. */
export interface ShownStep {
  profileId: string;
  id: number;
  model: Model;
  /** Null for the first step: nothing was observed before it. */
  changes: StepChanges | null;
  /** "14 Nov, 09:00". */
  label: string;
}

interface TimelineState {
  shown: ShownStep | null;
  /** The step being loaded; the track already shows it as chosen. */
  pending: number | null;
  error: string | null;
}

export const useTimelineStore = create<TimelineState>(() => ({ shown: null, pending: null, error: null }));

// Only the latest choice is applied when stepping faster than snapshots load.
let request = 0;

const STEP_DESCRIPTIONS = {
  added: "Added since the previous snapshot",
  altered: "Changed since the previous snapshot",
  dropped: "Removed since the previous snapshot",
} as const;

function toOverlay(changes: StepChanges | null, label: string): DiffOverlay {
  const relations: Record<string, "added" | "altered" | "dropped"> = {};
  const columns: Record<string, Record<string, { change: ColumnChangeKind }>> = {};
  for (const key of changes?.added ?? []) relations[key] = "added";
  for (const key of changes?.changed ?? []) relations[key] = "altered";
  for (const key of changes?.removed ?? []) relations[key] = "dropped";
  for (const [rel, cols] of Object.entries(changes?.columns ?? {})) {
    columns[rel] = Object.fromEntries(Object.entries(cols).map(([c, v]) => [c, { change: v.change }]));
  }
  return { sourceId: TIMELINE_SOURCE, label, relations, columns, impact: {}, descriptions: STEP_DESCRIPTIONS };
}

/** Puts a step's model on the canvas, with what it changed against the step before it. */
export async function showStep(
  profileId: string,
  steps: readonly TimelineStep[],
  index: number,
  label: string,
  load: (id: number) => Promise<Model>,
): Promise<void> {
  const step = steps[index];
  if (!step) return;
  const token = ++request;
  useTimelineStore.setState({ pending: step.id, error: null });
  const previous = steps[index - 1];
  try {
    const [model, before] = await Promise.all([load(step.id), previous ? load(previous.id) : null]);
    if (token !== request) return;
    const changes = before ? compareStep(model, before) : null;
    // The overlay first: the timeline watches for its overlay being replaced.
    useCanvasStore.getState().setDiff(toOverlay(changes, label));
    useTimelineStore.setState({ shown: { profileId, id: step.id, model, changes, label }, pending: null });
  } catch (error) {
    if (token !== request) return;
    useTimelineStore.setState({ pending: null, error: error instanceof Error ? error.message : String(error) });
  }
}

/** Back to the live schema; clears only the timeline's overlay, never a comparison or migration. */
export function showLive(): void {
  request++;
  const canvas = useCanvasStore.getState();
  if (canvas.diff?.sourceId === TIMELINE_SOURCE) canvas.setDiff(null);
  useTimelineStore.setState({ shown: null, pending: null, error: null });
}

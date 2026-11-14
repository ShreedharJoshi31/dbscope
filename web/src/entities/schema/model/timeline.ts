import { type Drift, diffModels } from "@/entities/schema/model/drift";
import type { Model, SnapshotInfo } from "@/shared/api";

export const NO_OBSERVATION = "no observation";
export const BEFORE_FIRST_OBSERVATION = "before the first observation";

/**
 * The time before a step. Nobody read the schema inside it, so the change
 * that led to the step happened somewhere in it; it never means "no change".
 */
export type TimelineGap =
  | { kind: "before-first"; label: typeof BEFORE_FIRST_OBSERVATION; from: null; to: string }
  | { kind: "unobserved"; label: typeof NO_OBSERVATION; from: string; to: string };

export interface TimelineStep {
  id: number;
  /** When this structure was first read. */
  takenAt: string;
  /** The last read that found it unchanged. */
  lastSeenAt: string;
  relations: number;
  schemas: readonly string[];
  gap: TimelineGap;
}

export interface Timeline {
  /** Oldest first. */
  steps: TimelineStep[];
  /** The current time (ms) the timeline was built for; relative labels use it. */
  now: number;
}

/** Orders the snapshot list (newest first from the API) oldest first, with the gap before each step. */
export function buildTimeline(snapshots: readonly SnapshotInfo[], now: number): Timeline {
  // Ids are the store's insertion order, which pruning and dedup also follow; clocks can be skewed.
  const ordered = [...snapshots].sort((a, b) => a.id - b.id);
  const steps = ordered.map((s, i): TimelineStep => {
    const previous = ordered[i - 1];
    return {
      id: s.id,
      takenAt: s.takenAt,
      lastSeenAt: s.lastSeenAt,
      relations: s.relations,
      schemas: s.schemas,
      gap: previous
        ? { kind: "unobserved", label: NO_OBSERVATION, from: previous.lastSeenAt, to: s.takenAt }
        : { kind: "before-first", label: BEFORE_FIRST_OBSERVATION, from: null, to: s.takenAt },
    };
  });
  return { steps, now };
}

/** What a step changed against the previous one. Keys are "schema.name". */
export interface StepChanges {
  added: string[];
  removed: string[];
  changed: string[];
  /** In a schema only one of the two snapshots read: neither added nor removed. */
  notCompared: string[];
  /** Schemas read by only one of the two snapshots. */
  notComparedSchemas: string[];
  /** Column changes by relation, as diffModels reports them. */
  columns: Drift["columns"];
  drift: Drift;
}

/** Compares a step's model with the previous step's, using diffModels (here = the step). */
export function compareStep(step: Model, previous: Model): StepChanges {
  const drift = diffModels(step, previous);
  const out: StepChanges = {
    added: [],
    removed: [],
    changed: [],
    notCompared: [],
    notComparedSchemas: [],
    columns: drift.columns,
    drift,
  };
  for (const key of Object.keys(drift.relations).sort()) {
    const change = drift.relations[key];
    if (change === "added") out.added.push(key);
    else if (change === "dropped") out.removed.push(key);
    else if (change === "altered") out.changed.push(key);
    else out.notCompared.push(key);
  }
  const ours = step.schemas ?? [];
  const theirs = previous.schemas ?? [];
  // diffModels compares everything when a side lists no schemas; so does this.
  if (ours.length > 0 && theirs.length > 0) {
    const a = new Set(ours);
    const b = new Set(theirs);
    out.notComparedSchemas = [...ours.filter((s) => !b.has(s)), ...theirs.filter((s) => !a.has(s))].sort();
  }
  return out;
}

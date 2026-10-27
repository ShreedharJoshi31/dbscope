import { useEffect, useMemo, useState } from "react";
import { type CanvasLayoutInput, putLayout, useCanvasLayout } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { safeSessionStorage } from "@/shared/lib/storage";
import { createLayoutSaver } from "@/widgets/erd-canvas/model/layout-saver";

const SAMPLE_PREFIX = "dbscope.sampleLayout:";

function readSample(key: string): CanvasLayoutInput | null {
  try {
    const value = JSON.parse(safeSessionStorage.getItem(SAMPLE_PREFIX + key) ?? "null") as CanvasLayoutInput | null;
    return value && typeof value.positions === "object" ? value : null;
  } catch {
    return null;
  }
}

export interface LayoutStorage {
  /** undefined while loading; null when nothing is saved for this key. */
  restored: CanvasLayoutInput | null | undefined;
  schedule: (layout: CanvasLayoutInput) => void;
}

/**
 * Restores and saves canvas positions. A connection's layout goes to the agent
 * (GET/PUT /layout, debounced 1 s); the read-only sample keeps its layout in
 * this tab's sessionStorage.
 */
export function useLayoutStorage(profileId: string | undefined, layoutKey: string, sample: boolean): LayoutStorage {
  const remote = useCanvasLayout(sample ? undefined : profileId, layoutKey);
  const [sampleLayout] = useState(() => (sample ? readSample(layoutKey) : null));

  const saver = useMemo(
    () =>
      createLayoutSaver((layout) => {
        if (sample) {
          safeSessionStorage.setItem(SAMPLE_PREFIX + layout.key, JSON.stringify(layout));
          return;
        }
        if (!profileId) return;
        putLayout(profileId, layout).catch((error: unknown) => {
          announce(`Could not save the canvas layout: ${error instanceof Error ? error.message : "unknown error"}`);
        });
      }),
    [sample, profileId],
  );
  useEffect(() => () => saver.flush(), [saver]);

  let restored: LayoutStorage["restored"];
  if (sample) restored = sampleLayout;
  else if (remote.isPending) restored = undefined;
  else restored = remote.data ?? null; // A failed read starts from a fresh layout instead of blocking the canvas.

  return { restored, schedule: saver.schedule };
}

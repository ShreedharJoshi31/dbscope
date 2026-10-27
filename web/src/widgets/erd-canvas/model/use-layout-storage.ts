import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { type CanvasLayoutInput, putLayout, useCanvasLayout } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { registerLayoutFlusher } from "@/shared/lib/layout-sync";
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

  // Saves run one after another, so waiting on the last waits on all of them.
  const inflight = useRef<Promise<void>>(Promise.resolve());
  // Set once an import is about to replace the saved layout: this canvas
  // saves nothing more and is remounted afterwards.
  const frozen = useRef(false);
  const saver = useMemo(
    () =>
      createLayoutSaver((layout) => {
        if (sample) {
          safeSessionStorage.setItem(SAMPLE_PREFIX + layout.key, JSON.stringify(layout));
          return;
        }
        if (!profileId) return;
        inflight.current = inflight.current.then(() =>
          putLayout(profileId, layout).catch((error: unknown) => {
            announce(`Could not save the canvas layout: ${error instanceof Error ? error.message : "unknown error"}`);
          }),
        );
      }),
    [sample, profileId],
  );
  useEffect(() => () => (frozen.current ? saver.cancel() : saver.flush()), [saver]);
  useEffect(
    () =>
      registerLayoutFlusher(() => {
        saver.flush();
        frozen.current = true;
        return inflight.current;
      }),
    [saver],
  );
  const schedule = useCallback(
    (layout: CanvasLayoutInput) => {
      if (!frozen.current) saver.schedule(layout);
    },
    [saver],
  );

  let restored: LayoutStorage["restored"];
  if (sample) restored = sampleLayout;
  else if (remote.isPending) restored = undefined;
  else restored = remote.data ?? null; // A failed read starts from a fresh layout instead of blocking the canvas.

  return { restored, schedule };
}

import { useCallback, useEffect } from "react";
import { type PanelSize, usePanelRef } from "react-resizable-panels";
import { type PanelId, usePanelStore } from "@/features/toggle-panel";

/**
 * Two-way sync between the panel store (shortcuts, rail, palette) and a
 * collapsible Panel (drag, keyboard). The store decides whether a panel is
 * open: a panel that mounts (page load, or the layout switching at 1280px)
 * is expanded or collapsed to match it. Drags and keyboard resizes update it.
 */
export function useSyncedPanel(id: PanelId) {
  const panelRef = usePanelRef();
  const open = usePanelStore((s) => s.open[id]);

  const apply = useCallback(
    (shouldBeOpen: boolean) => {
      const panel = panelRef.current;
      if (!panel) return;
      if (shouldBeOpen && panel.isCollapsed()) panel.expand();
      if (!shouldBeOpen && !panel.isCollapsed()) panel.collapse();
    },
    [panelRef],
  );

  useEffect(() => apply(open), [open, apply]);

  const onResize = (size: PanelSize, _panelId: string | number | undefined, previous: PanelSize | undefined) => {
    const isOpen = size.inPixels > 0;
    // On mount the panel reports the size saved in its layout; the store wins over it.
    if (previous === undefined) {
      if (isOpen !== usePanelStore.getState().open[id]) queueMicrotask(() => apply(usePanelStore.getState().open[id]));
      return;
    }
    usePanelStore.getState().setOpen(id, isOpen);
  };

  return { panelRef, onResize };
}

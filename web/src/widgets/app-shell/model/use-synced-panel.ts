import { useEffect, useRef } from "react";
import { type PanelSize, usePanelRef } from "react-resizable-panels";
import { type PanelId, usePanelStore } from "@/features/toggle-panel";

/**
 * Two-way sync between the panel store (shortcuts, rail, palette) and a
 * collapsible Panel (drag, keyboard, persisted layout). The store follows the
 * panel's real size; store changes expand or collapse the panel.
 */
export function useSyncedPanel(id: PanelId) {
  const panelRef = usePanelRef();
  const open = usePanelStore((s) => s.open[id]);
  const setOpen = usePanelStore((s) => s.setOpen);
  const previous = useRef<boolean | null>(null);

  useEffect(() => {
    const panel = panelRef.current;
    // The first value comes from the store default; the persisted layout wins on mount.
    if (previous.current === null || previous.current === open || !panel) {
      previous.current = open;
      return;
    }
    previous.current = open;
    if (open && panel.isCollapsed()) panel.expand();
    if (!open && !panel.isCollapsed()) panel.collapse();
  }, [open, panelRef]);

  const onResize = (size: PanelSize) => {
    const isOpen = size.inPixels > 0;
    previous.current = isOpen;
    setOpen(id, isOpen);
  };

  return { panelRef, onResize };
}

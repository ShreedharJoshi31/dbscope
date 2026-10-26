import { create } from "zustand";

export type PanelId = "explorer" | "dock" | "assistant";

interface PanelState {
  open: Record<PanelId, boolean>;
  /** Below 1280px the assistant is an overlay drawer with its own open flag. */
  assistantDrawer: boolean;
  assistantMode: "panel" | "drawer";
  setOpen: (panel: PanelId, open: boolean) => void;
  setAssistantDrawer: (open: boolean) => void;
  setAssistantMode: (mode: "panel" | "drawer") => void;
  toggle: (panel: PanelId) => void;
}

export const usePanelStore = create<PanelState>((set, get) => ({
  // Panels report their real (persisted) size on mount and overwrite these.
  open: { explorer: true, dock: false, assistant: true },
  assistantDrawer: false,
  assistantMode: "panel",
  setOpen: (panel, open) => {
    if (get().open[panel] === open) return;
    set((s) => ({ open: { ...s.open, [panel]: open } }));
  },
  setAssistantDrawer: (assistantDrawer) => set({ assistantDrawer }),
  setAssistantMode: (assistantMode) => set({ assistantMode, assistantDrawer: false }),
  toggle: (panel) => {
    const state = get();
    if (panel === "assistant" && state.assistantMode === "drawer") {
      set({ assistantDrawer: !state.assistantDrawer });
      return;
    }
    state.setOpen(panel, !state.open[panel]);
  },
}));

export function togglePanel(panel: PanelId): void {
  usePanelStore.getState().toggle(panel);
}

export function useIsPanelVisible(panel: PanelId): boolean {
  return usePanelStore((s) =>
    panel === "assistant" && s.assistantMode === "drawer" ? s.assistantDrawer : s.open[panel],
  );
}

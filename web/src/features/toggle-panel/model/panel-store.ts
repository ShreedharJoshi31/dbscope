import { create } from "zustand";
import { safeLocalStorage } from "@/shared/lib/storage";

export type PanelId = "explorer" | "dock" | "assistant";

const PANELS_KEY = "dbscope.panels";
const DEFAULT_OPEN: Record<PanelId, boolean> = { explorer: true, dock: false, assistant: true };

function readOpen(): Record<PanelId, boolean> {
  try {
    const stored: Partial<Record<PanelId, unknown>> | null = JSON.parse(safeLocalStorage.getItem(PANELS_KEY) ?? "null");
    const pick = (id: PanelId) => (typeof stored?.[id] === "boolean" ? stored[id] : DEFAULT_OPEN[id]);
    return { explorer: pick("explorer"), dock: pick("dock"), assistant: pick("assistant") };
  } catch {
    return DEFAULT_OPEN;
  }
}

interface PanelState {
  /**
   * The single source of truth for which panels are open; persisted. Panel layouts only remember sizes.
   * For the assistant this is the docked (>= 1280px) panel, kept while the drawer is in use.
   */
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
  open: readOpen(),
  assistantDrawer: false,
  assistantMode: "panel",
  setOpen: (panel, open) => {
    if (get().open[panel] === open) return;
    const next = { ...get().open, [panel]: open };
    safeLocalStorage.setItem(PANELS_KEY, JSON.stringify(next));
    set({ open: next });
  },
  setAssistantDrawer: (assistantDrawer) => set({ assistantDrawer }),
  setAssistantMode: (assistantMode) => {
    if (get().assistantMode === assistantMode) return;
    set({ assistantMode, assistantDrawer: false });
  },
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

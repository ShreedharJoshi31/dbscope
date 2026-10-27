import { create } from "zustand";

/**
 * Commands the ERD canvas exposes to the rest of the app (global hotkeys, the
 * command palette) without those layers importing the canvas widget. The
 * mounted canvas registers handlers; nothing runs while it is not mounted.
 */
export type CanvasCommandId =
  | "focusSearch"
  | "toggleFocus"
  | "fitView"
  | "collapseAll"
  | "expandAll"
  | "layoutLayered"
  | "layoutForce"
  | "layoutGrid"
  | "resetLayout"
  | "toggleInferred"
  | "toggleMinimap";

type Handlers = Partial<Record<CanvasCommandId, () => void>>;

interface CanvasCommandsState {
  handlers: Handlers;
  register: (handlers: Handlers) => () => void;
}

export const useCanvasCommands = create<CanvasCommandsState>((set, get) => ({
  handlers: {},
  register: (handlers) => {
    set({ handlers });
    return () => {
      if (get().handlers === handlers) set({ handlers: {} });
    };
  },
}));

/** Runs a canvas command; false when no canvas is mounted to handle it. */
export function runCanvasCommand(id: CanvasCommandId): boolean {
  const handler = useCanvasCommands.getState().handlers[id];
  if (!handler) return false;
  handler();
  return true;
}

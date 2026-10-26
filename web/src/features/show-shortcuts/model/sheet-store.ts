import { create } from "zustand";

interface ShortcutSheetState {
  open: boolean;
  setOpen: (open: boolean) => void;
}

export const useShortcutSheet = create<ShortcutSheetState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
}));

export function openShortcutSheet(): void {
  useShortcutSheet.getState().setOpen(true);
}

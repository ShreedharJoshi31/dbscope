import { create } from "zustand";

interface DialogState {
  open: boolean;
  setOpen: (open: boolean) => void;
}

export const useLlmSettingsDialog = create<DialogState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
}));

export function openLlmSettings(): void {
  useLlmSettingsDialog.getState().setOpen(true);
}

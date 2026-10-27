import { create } from "zustand";

export type AnalyzeDialogMode = "files" | "paste";

interface AnalyzeDialogState {
  open: boolean;
  mode: AnalyzeDialogMode;
  openWith: (mode: AnalyzeDialogMode) => void;
  setMode: (mode: AnalyzeDialogMode) => void;
  setOpen: (open: boolean) => void;
}

export const useAnalyzeDialog = create<AnalyzeDialogState>((set) => ({
  open: false,
  mode: "files",
  openWith: (mode) => set({ open: true, mode }),
  setMode: (mode) => set({ mode }),
  setOpen: (open) => set({ open }),
}));

/** Palette: "Analyze migration files…". */
export function openAnalyzeFiles(): void {
  useAnalyzeDialog.getState().openWith("files");
}

/** Palette: "Paste migration SQL". */
export function openPasteMigration(): void {
  useAnalyzeDialog.getState().openWith("paste");
}

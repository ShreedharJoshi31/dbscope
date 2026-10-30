import { create } from "zustand";

export type AnalyzeDialogMode = "files" | "paste" | "folder";

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

/** Palette: "Check the migrations folder". */
export function openFolderMigration(): void {
  useAnalyzeDialog.getState().openWith("folder");
}

/** Palette: "Paste migration SQL". */
export function openPasteMigration(): void {
  useAnalyzeDialog.getState().openWith("paste");
}

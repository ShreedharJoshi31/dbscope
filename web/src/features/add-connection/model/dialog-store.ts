import { create } from "zustand";
import type { Profile } from "@/shared/api";

type DialogState = { mode: "create" } | { mode: "edit"; profile: Profile } | null;

interface ConnectionDialogStore {
  dialog: DialogState;
  openCreate: () => void;
  openEdit: (profile: Profile) => void;
  close: () => void;
}

export const useConnectionDialog = create<ConnectionDialogStore>((set) => ({
  dialog: null,
  openCreate: () => set({ dialog: { mode: "create" } }),
  openEdit: (profile) => set({ dialog: { mode: "edit", profile } }),
  close: () => set({ dialog: null }),
}));

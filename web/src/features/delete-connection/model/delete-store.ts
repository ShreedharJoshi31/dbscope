import { create } from "zustand";
import type { Profile } from "@/shared/api";

interface DeleteConnectionStore {
  target: Profile | null;
  open: (profile: Profile) => void;
  close: () => void;
}

export const useDeleteConnection = create<DeleteConnectionStore>((set) => ({
  target: null,
  open: (target) => set({ target }),
  close: () => set({ target: null }),
}));

/** prod-replica connections need the exact name typed; others need one click. */
export function canConfirmDelete(profile: Profile, typedName: string): boolean {
  return profile.env !== "prod-replica" || typedName === profile.name;
}

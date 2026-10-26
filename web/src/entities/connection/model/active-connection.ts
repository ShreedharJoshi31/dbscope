import { create } from "zustand";
import type { Profile } from "@/shared/api";
import { safeLocalStorage } from "@/shared/lib/storage";

const ACTIVE_KEY = "dbscope.activeConnection";

interface ActiveConnectionState {
  activeId: string | null;
  setActiveId: (id: string | null) => void;
}

export const useActiveConnectionStore = create<ActiveConnectionState>((set) => ({
  activeId: safeLocalStorage.getItem(ACTIVE_KEY),
  setActiveId: (id) => {
    if (id) safeLocalStorage.setItem(ACTIVE_KEY, id);
    else safeLocalStorage.removeItem(ACTIVE_KEY);
    set({ activeId: id });
  },
}));

/** The remembered profile, or the first one when the remembered id no longer exists. */
export function resolveActiveProfile(profiles: readonly Profile[], activeId: string | null): Profile | undefined {
  return profiles.find((p) => p.id === activeId) ?? profiles[0];
}

import { create } from "zustand";

interface AnnouncerState {
  message: string;
  id: number;
  announce: (message: string) => void;
}

/** Feeds the single polite live region mounted by the app. */
export const useAnnouncer = create<AnnouncerState>((set) => ({
  message: "",
  id: 0,
  announce: (message) => set((s) => ({ message, id: s.id + 1 })),
}));

export function announce(message: string): void {
  useAnnouncer.getState().announce(message);
}

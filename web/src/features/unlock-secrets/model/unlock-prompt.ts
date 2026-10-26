import { create } from "zustand";

interface UnlockPromptState {
  /** The user chose "Not now"; reset when a request hits the locked vault. */
  dismissed: boolean;
  dismiss: () => void;
  reopen: () => void;
}

export const useUnlockPrompt = create<UnlockPromptState>((set) => ({
  dismissed: false,
  dismiss: () => set({ dismissed: true }),
  reopen: () => set({ dismissed: false }),
}));

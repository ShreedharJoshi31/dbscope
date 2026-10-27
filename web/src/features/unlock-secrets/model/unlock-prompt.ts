import { create } from "zustand";

interface UnlockPromptState {
  /** The user chose "Not now"; reset when a request hits the locked vault. */
  dismissed: boolean;
  /** A request hit the locked vault; a vault with nothing saved yet only prompts then. */
  requested: boolean;
  dismiss: () => void;
  reopen: () => void;
}

export const useUnlockPrompt = create<UnlockPromptState>((set) => ({
  dismissed: false,
  requested: false,
  dismiss: () => set({ dismissed: true }),
  reopen: () => set({ dismissed: false, requested: true }),
}));

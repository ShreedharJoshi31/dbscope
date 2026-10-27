import { create } from "zustand";
import type { Turn } from "@/entities/assistant-turn/model/turn";

export interface Thread {
  /** Returned by the agent's `done` event; kept in memory only (the agent forgets after 30 min idle). */
  conversationId: string | null;
  turns: Turn[];
}

const EMPTY_THREAD: Thread = { conversationId: null, turns: [] };

interface ThreadState {
  /** Keyed by profile id: each connection has its own conversation. */
  threads: Record<string, Thread>;
  /** Bumped to ask the composer to take focus (palette, shortcut). */
  composerFocus: number;
  addTurn: (profileId: string, turn: Turn) => void;
  updateTurn: (profileId: string, turnId: string, update: (turn: Turn) => Turn) => void;
  setConversation: (profileId: string, conversationId: string) => void;
  newConversation: (profileId: string) => void;
  focusComposer: () => void;
}

export const useThreadStore = create<ThreadState>((set) => {
  const patch = (profileId: string, change: (thread: Thread) => Thread) =>
    set((s) => ({ threads: { ...s.threads, [profileId]: change(s.threads[profileId] ?? EMPTY_THREAD) } }));

  return {
    threads: {},
    composerFocus: 0,
    addTurn: (profileId, turn) => patch(profileId, (t) => ({ ...t, turns: [...t.turns, turn] })),
    updateTurn: (profileId, turnId, update) =>
      patch(profileId, (t) => ({ ...t, turns: t.turns.map((turn) => (turn.id === turnId ? update(turn) : turn)) })),
    setConversation: (profileId, conversationId) => patch(profileId, (t) => ({ ...t, conversationId })),
    newConversation: (profileId) => patch(profileId, () => EMPTY_THREAD),
    focusComposer: () => set((s) => ({ composerFocus: s.composerFocus + 1 })),
  };
});

export function useThread(profileId: string | undefined): Thread {
  return useThreadStore((s) => (profileId ? (s.threads[profileId] ?? EMPTY_THREAD) : EMPTY_THREAD));
}

export function getThread(profileId: string): Thread {
  return useThreadStore.getState().threads[profileId] ?? EMPTY_THREAD;
}

export function getTurn(profileId: string, turnId: string): Turn | undefined {
  return getThread(profileId).turns.find((t) => t.id === turnId);
}

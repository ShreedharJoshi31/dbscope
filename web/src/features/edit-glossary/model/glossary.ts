import { create } from "zustand";
import type { GlossaryEntry } from "@/shared/api";

/** Limits enforced by PUT /glossary (docs/API.md "Glossary"). */
export const GLOSSARY_LIMITS = { entries: 500, term: 100, definition: 1000, sql: 1000 } as const;

export interface DraftEntry extends GlossaryEntry {
  /** Local identity for React keys and focus; never sent. */
  key: string;
}

export type EntryErrors = Partial<Record<keyof GlossaryEntry, string>>;

let draftKey = 0;

export function toDraft(entry: GlossaryEntry): DraftEntry {
  draftKey += 1;
  return { ...entry, key: `g${draftKey}` };
}

export function emptyDraft(): DraftEntry {
  return toDraft({ term: "", definition: "", sql: "" });
}

const chars = (s: string) => Array.from(s).length;

/** Per-row errors keyed by draft key; empty when the glossary can be saved. */
export function validateGlossary(entries: readonly DraftEntry[]): Map<string, EntryErrors> {
  const errors = new Map<string, EntryErrors>();
  const seen = new Map<string, string>();
  for (const e of entries) {
    const found: EntryErrors = {};
    const term = e.term.trim();
    if (!term) found.term = "Enter a term.";
    else if (chars(term) > GLOSSARY_LIMITS.term) found.term = `Use at most ${GLOSSARY_LIMITS.term} characters.`;
    else if (seen.has(term.toLowerCase())) found.term = `"${term}" is already defined above.`;
    if (chars(e.definition) > GLOSSARY_LIMITS.definition) {
      found.definition = `Use at most ${GLOSSARY_LIMITS.definition} characters.`;
    }
    if (chars(e.sql) > GLOSSARY_LIMITS.sql) found.sql = `Use at most ${GLOSSARY_LIMITS.sql} characters.`;
    if (term) seen.set(term.toLowerCase(), e.key);
    if (Object.keys(found).length > 0) errors.set(e.key, found);
  }
  return errors;
}

/** The PUT body: trimmed terms, in the user's order. */
export function toGlossary(entries: readonly DraftEntry[]): GlossaryEntry[] {
  return entries.map(({ term, definition, sql }) => ({
    term: term.trim(),
    definition: definition.trim(),
    sql: sql.trim(),
  }));
}

interface GlossaryDialogState {
  open: boolean;
  setOpen: (open: boolean) => void;
}

export const useGlossaryDialog = create<GlossaryDialogState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
}));

export function openGlossary(): void {
  useGlossaryDialog.getState().setOpen(true);
}

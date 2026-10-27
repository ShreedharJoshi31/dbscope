import { BookText, LoaderCircle, Plus } from "lucide-react";
import { useState } from "react";
import { useActiveConnection } from "@/entities/connection";
import {
  type DraftEntry,
  emptyDraft,
  GLOSSARY_LIMITS,
  toDraft,
  toGlossary,
  useGlossaryDialog,
  validateGlossary,
} from "@/features/edit-glossary/model/glossary";
import { GlossaryRow } from "@/features/edit-glossary/ui/glossary-row";
import { type GlossaryEntry, type Profile, useGlossary, useSaveGlossary } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { StatusIcon } from "@/shared/ui/status-icon";

function Editor({ profile, initial, onDone }: { profile: Profile; initial: GlossaryEntry[]; onDone: () => void }) {
  const [entries, setEntries] = useState<DraftEntry[]>(() => initial.map(toDraft));
  const [showErrors, setShowErrors] = useState(false);
  const save = useSaveGlossary();
  const errors = validateGlossary(entries);

  const update = (key: string, field: keyof GlossaryEntry, value: string) =>
    setEntries((list) => list.map((e) => (e.key === key ? { ...e, [field]: value } : e)));

  const add = () => {
    const draft = emptyDraft();
    setEntries((list) => [...list, draft]);
    requestAnimationFrame(() =>
      document.querySelector<HTMLInputElement>(`[data-glossary-term="${draft.key}"]`)?.focus(),
    );
  };

  const onSave = () => {
    setShowErrors(true);
    if (errors.size > 0) return;
    save.mutate(
      { profileId: profile.id, entries: toGlossary(entries) },
      {
        onSuccess: () => {
          announce(`Glossary saved, ${entries.length} terms.`);
          onDone();
        },
      },
    );
  };

  return (
    <form
      className="flex min-h-0 flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      {entries.length === 0 ? (
        <p className="rounded-md border border-dashed border-border-default p-4 text-center text-xs text-text-muted">
          No terms yet. Add your team's words for things, such as "active user" or "churned", so the assistant uses your
          definitions instead of guessing. Answers you mark "Remember this answer" appear here.
        </p>
      ) : (
        <ul aria-label="Glossary terms" className="flex flex-col">
          {entries.map((entry, index) => (
            <GlossaryRow
              key={entry.key}
              entry={entry}
              index={index}
              errors={showErrors ? errors.get(entry.key) : undefined}
              onChange={(field, value) => update(entry.key, field, value)}
              onDelete={() => setEntries((list) => list.filter((e) => e.key !== entry.key))}
            />
          ))}
        </ul>
      )}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="self-start"
        onClick={add}
        disabled={entries.length >= GLOSSARY_LIMITS.entries}
      >
        <Plus aria-hidden />
        Add term
      </Button>
      {save.isError && (
        <p role="alert" className="flex items-start gap-1 text-xs text-status-danger-fg">
          <StatusIcon tone="danger" className="size-3.5" />
          {save.error.message}
        </p>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? "Saving…" : "Save glossary"}
        </Button>
      </DialogFooter>
    </form>
  );
}

function Body({ onDone }: { onDone: () => void }) {
  const { profile } = useActiveConnection();
  const glossary = useGlossary(profile?.id);
  if (!profile)
    return <p className="text-sm text-text-muted">Choose a connection first; each one has its own glossary.</p>;
  if (glossary.isPending) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-text-muted">
        <LoaderCircle aria-hidden className="size-4 animate-spin" />
        Loading the glossary…
      </p>
    );
  }
  if (glossary.isError) {
    return (
      <p role="alert" className="flex items-center gap-1.5 text-sm text-status-danger-fg">
        <StatusIcon tone="danger" />
        {glossary.error.message}
      </p>
    );
  }
  return <Editor profile={profile} initial={glossary.data} onDone={onDone} />;
}

/** Per-connection glossary: terms the assistant uses instead of guessing (docs/NL.md "Clarification policy"). */
export function GlossaryDialog() {
  const { open, setOpen } = useGlossaryDialog();
  const { profile } = useActiveConnection();
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="flex max-h-[90vh] flex-col overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-1.5">
            <BookText aria-hidden className="size-4" />
            Glossary{profile ? ` for ${profile.name}` : ""}
          </DialogTitle>
          <DialogDescription>
            The assistant reads these definitions with every question for this connection. They are sent to your LLM
            provider together with the schema.
          </DialogDescription>
        </DialogHeader>
        {open && <Body onDone={() => setOpen(false)} />}
      </DialogContent>
    </Dialog>
  );
}

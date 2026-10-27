import { Trash2 } from "lucide-react";
import type { DraftEntry, EntryErrors } from "@/features/edit-glossary/model/glossary";
import type { GlossaryEntry } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";
import { Textarea } from "@/shared/ui/textarea";

interface GlossaryRowProps {
  entry: DraftEntry;
  index: number;
  errors: EntryErrors | undefined;
  onChange: (field: keyof GlossaryEntry, value: string) => void;
  onDelete: () => void;
}

export function GlossaryRow({ entry, index, errors, onChange, onDelete }: GlossaryRowProps) {
  const name = entry.term.trim() || `term ${index + 1}`;
  return (
    <li
      data-testid="glossary-row"
      className="grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] items-start gap-2 border-b border-border-default py-3 last:border-b-0"
    >
      <FormField label="Term" error={errors?.term}>
        {(control) => (
          <Input
            {...control}
            data-glossary-term={entry.key}
            value={entry.term}
            onChange={(e) => onChange("term", e.target.value)}
            placeholder="active user"
          />
        )}
      </FormField>
      <div className="flex min-w-0 flex-col gap-2">
        <FormField label="Definition" error={errors?.definition}>
          {(control) => (
            <Textarea
              {...control}
              rows={2}
              value={entry.definition}
              onChange={(e) => onChange("definition", e.target.value)}
              placeholder="A user who signed in during the last 30 days"
              className="min-h-12 text-sm"
            />
          )}
        </FormField>
        <FormField
          label="SQL"
          optional
          error={errors?.sql}
          description="A condition or expression the assistant should use."
        >
          {(control) => (
            <Input
              {...control}
              value={entry.sql}
              onChange={(e) => onChange("sql", e.target.value)}
              placeholder="last_seen_at > now() - interval '30 days'"
              spellCheck={false}
              className="font-mono text-xs"
            />
          )}
        </FormField>
      </div>
      <Button variant="ghost" size="icon-sm" className="mt-5" aria-label={`Delete ${name}`} onClick={onDelete}>
        <Trash2 aria-hidden />
      </Button>
    </li>
  );
}

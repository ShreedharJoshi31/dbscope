import { Pencil } from "lucide-react";
import { useId, useState } from "react";
import type { Assumption } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { Input } from "@/shared/ui/input";

interface AssumptionChipsProps {
  assumptions: readonly Assumption[];
  disabled: boolean;
  onReask: (overrides: Assumption[]) => void;
}

function ChipEditor({
  value,
  onSubmit,
  onCancel,
}: {
  value: Assumption;
  onSubmit: (next: Assumption) => void;
  onCancel: () => void;
}) {
  const [label, setLabel] = useState(value.label);
  const [fragment, setFragment] = useState(value.sqlFragment);
  const id = useId();
  return (
    <form
      aria-label={`Edit assumption: ${value.label}`}
      className="flex flex-col gap-1.5 rounded-md border border-border-control bg-surface-canvas p-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (label.trim()) onSubmit({ label: label.trim(), sqlFragment: fragment.trim() });
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      <label htmlFor={`${id}-l`} className="text-2xs font-medium">
        Assumption
      </label>
      <Input
        id={`${id}-l`}
        value={label}
        onChange={(e) => setLabel(e.target.value)}
        autoFocus
        className="h-7 text-xs"
      />
      <label htmlFor={`${id}-f`} className="text-2xs font-medium">
        SQL fragment
      </label>
      <Input
        id={`${id}-f`}
        value={fragment}
        onChange={(e) => setFragment(e.target.value)}
        spellCheck={false}
        className="h-7 font-mono text-xs"
      />
      <div className="flex gap-1">
        <Button type="submit" size="xs" disabled={!label.trim()}>
          Re-ask with this
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** Editable assumptions (UX rule 5): editing one regenerates the answer honouring all of them. */
export function AssumptionChips({ assumptions, disabled, onReask }: AssumptionChipsProps) {
  const [editing, setEditing] = useState<number | null>(null);
  if (assumptions.length === 0) return null;
  return (
    <section data-section="assumptions" aria-label="Assumptions" className="flex flex-col gap-1">
      <h4 className="text-2xs font-semibold tracking-wide text-text-muted uppercase">Assumptions</h4>
      <ul className="flex flex-wrap gap-1">
        {assumptions.map((a, i) => {
          const key = `${i}:${a.label}`;
          if (editing === i) {
            return (
              <li key={key} className="w-full">
                <ChipEditor
                  value={a}
                  onCancel={() => setEditing(null)}
                  onSubmit={(next) => {
                    setEditing(null);
                    onReask(assumptions.map((x, j) => (j === i ? next : x)));
                  }}
                />
              </li>
            );
          }
          return (
            <li key={key} className="min-w-0">
              <button
                type="button"
                data-testid="assumption-chip"
                disabled={disabled}
                onClick={() => setEditing(i)}
                aria-label={`Edit assumption: ${a.label}${a.sqlFragment ? `, ${a.sqlFragment}` : ""}`}
                className="flex max-w-full items-center gap-1 rounded-full border border-border-control bg-surface-canvas px-2 py-0.5 text-left text-xs hover:bg-surface-hover disabled:opacity-50"
              >
                <span className="shrink-0">{a.label}</span>
                {a.sqlFragment && <span className="truncate font-mono text-2xs text-text-muted">{a.sqlFragment}</span>}
                <Pencil aria-hidden className="size-3 shrink-0 text-text-muted" />
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

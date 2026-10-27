import { useQueryClient } from "@tanstack/react-query";
import { Layers2, LoaderCircle } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { withSchemas } from "@/features/select-schemas/model/profile-input";
import { type Profile, schemaQueryKeys, useNamespaces, useUpdateProfile } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { Checkbox } from "@/shared/ui/checkbox";
import { Popover, PopoverContent, PopoverDescription, PopoverTitle, PopoverTrigger } from "@/shared/ui/popover";

interface SchemaPickerProps {
  profile: Profile;
}

function NamespaceList({ profile, onApplied }: SchemaPickerProps & { onApplied: () => void }) {
  const namespaces = useNamespaces(profile.id);
  const update = useUpdateProfile();
  const client = useQueryClient();
  const [chosen, setChosen] = useState<Set<string>>(() => new Set(profile.schemas));
  const listId = useId();

  useEffect(() => setChosen(new Set(profile.schemas)), [profile.schemas]);

  if (namespaces.isPending) {
    return (
      <p className="flex items-center gap-2 text-xs text-text-muted">
        <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
        Listing schemas…
      </p>
    );
  }
  if (namespaces.isError) {
    return (
      <p role="alert" className="text-xs text-status-danger-fg">
        Could not list schemas: {namespaces.error.message}
      </p>
    );
  }

  const all = chosen.size === 0;
  const toggle = (name: string, on: boolean) => {
    const next = new Set(chosen);
    if (on) next.add(name);
    else next.delete(name);
    setChosen(next);
  };

  const apply = () => {
    update.mutate(
      { id: profile.id, input: withSchemas(profile, [...chosen]) },
      {
        onSuccess: () => {
          void client.invalidateQueries({ queryKey: schemaQueryKeys.schema(profile.id) });
          announce(
            all
              ? "Showing every schema. Reading the schema again."
              : `Showing ${chosen.size} schemas. Reading the schema again.`,
          );
          onApplied();
        },
      },
    );
  };

  return (
    <div className="flex flex-col gap-2">
      {/* biome-ignore lint/a11y/noLabelWithoutControl: Base UI's Checkbox renders the control inside the wrapping label. */}
      <label className="flex items-center gap-2 text-sm">
        <Checkbox checked={all} onCheckedChange={(on) => on && setChosen(new Set())} />
        All non-system schemas
      </label>
      <ul
        id={listId}
        aria-label="Schemas"
        className="flex max-h-64 flex-col gap-1 overflow-y-auto border-t border-border-default pt-2"
      >
        {namespaces.data.map((ns) => (
          <li key={ns.name}>
            {/* biome-ignore lint/a11y/noLabelWithoutControl: Base UI's Checkbox renders the control inside the wrapping label. */}
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={chosen.has(ns.name)} onCheckedChange={(on) => toggle(ns.name, on)} />
              <span className="font-mono">{ns.name}</span>
              <span className="ml-auto font-mono text-2xs text-text-muted tabular-nums">{ns.relationCount}</span>
            </label>
          </li>
        ))}
      </ul>
      {update.isError && (
        <p role="alert" className="text-xs text-status-danger-fg">
          Could not save: {update.error.message}
        </p>
      )}
      <Button size="sm" className="self-end" onClick={apply} disabled={update.isPending}>
        {update.isPending ? "Saving…" : "Apply and reload"}
      </Button>
    </div>
  );
}

/** Chooses which schemas the profile introspects (PUT profile), then re-reads the schema. */
export function SchemaPicker({ profile }: SchemaPickerProps) {
  const [open, setOpen] = useState(false);
  const label = profile.schemas.length === 0 ? "All schemas" : `${profile.schemas.length} schemas`;
  const short = profile.schemas.length === 0 ? "All" : String(profile.schemas.length);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button variant="ghost" size="xs" aria-label={`Choose schemas (now: ${label})`} title="Choose schemas" />
        }
      >
        <Layers2 aria-hidden />
        {short}
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64">
        <PopoverTitle>Schemas to show</PopoverTitle>
        <PopoverDescription>
          Tables in other schemas appear only as greyed stubs when a foreign key points at them.
        </PopoverDescription>
        {open && <NamespaceList profile={profile} onApplied={() => setOpen(false)} />}
      </PopoverContent>
    </Popover>
  );
}

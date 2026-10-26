import { ChevronsUpDown, Database, Pencil, Plus, Trash2 } from "lucide-react";
import { EnvBadge, useActiveConnection } from "@/entities/connection";
import { switchConnection } from "@/features/switch-connection/model/switch-connection";
import type { Profile } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";

interface ConnectionSwitcherProps {
  onNew: () => void;
  onEdit: (profile: Profile) => void;
  onDelete: (profile: Profile) => void;
}

export function ConnectionSwitcher({ onNew, onEdit, onDelete }: ConnectionSwitcherProps) {
  const { profile: active, profiles } = useActiveConnection();

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            className="max-w-80 gap-2 px-2"
            aria-label={active ? `Connection: ${active.name}, ${active.env}. Switch connection` : "Choose a connection"}
          />
        }
      >
        <Database aria-hidden className="text-text-muted" />
        <span className="truncate font-medium">{active?.name ?? "No connection"}</span>
        {active && <EnvBadge env={active.env} />}
        <ChevronsUpDown aria-hidden className="text-text-muted" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-80">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Connections</DropdownMenuLabel>
          <DropdownMenuRadioGroup
            value={active?.id ?? null}
            onValueChange={(id) => {
              const next = profiles.find((p) => p.id === id);
              if (next) switchConnection(next);
            }}
          >
            {profiles.map((profile) => (
              <DropdownMenuRadioItem key={profile.id} value={profile.id} closeOnClick className="items-start py-1">
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="flex items-center gap-2">
                    <span className="truncate">{profile.name}</span>
                    <EnvBadge env={profile.env} />
                  </span>
                  <span className="truncate font-mono text-2xs text-text-muted">
                    {profile.user}@{profile.host}:{profile.port}/{profile.database}
                  </span>
                </span>
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={onNew}>
          <Plus aria-hidden />
          New connection…
        </DropdownMenuItem>
        {active && (
          <>
            <DropdownMenuItem onClick={() => onEdit(active)}>
              <Pencil aria-hidden />
              Edit {active.name}…
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onClick={() => onDelete(active)}>
              <Trash2 aria-hidden />
              Delete {active.name}…
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

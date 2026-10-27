import { Keyboard, ListTree, PanelBottom, Plug, Plus, Sparkles, SunMoon } from "lucide-react";
import { EnvBadge, useActiveConnection } from "@/entities/connection";
import { useConnectionDialog } from "@/features/add-connection";
import { openShortcutSheet } from "@/features/show-shortcuts";
import { switchConnection } from "@/features/switch-connection";
import { toggleExplorerView, togglePanel } from "@/features/toggle-panel";
import { toggleTheme } from "@/features/toggle-theme";
import { formatCombo, type KeyCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@/shared/ui/command";
import { useCommandPalette } from "@/widgets/command-palette/model/palette-store";
import { CanvasCommandItems } from "@/widgets/command-palette/ui/canvas-command-items";
import { MigrationCommandItems } from "@/widgets/command-palette/ui/migration-command-items";
import { AssistantCommandItems, QueryCommandItems } from "@/widgets/command-palette/ui/query-command-items";

function Shortcut({ combo }: { combo: KeyCombo }) {
  return <CommandShortcut className="font-mono tracking-normal">{formatCombo(combo)}</CommandShortcut>;
}

export function CommandPalette() {
  const { open, setOpen } = useCommandPalette();
  const { profile: active, profiles } = useActiveConnection();
  const openCreate = useConnectionDialog((s) => s.openCreate);

  /** Close first so focus returns before the action opens another dialog. */
  const run = (action: () => void) => () => {
    setOpen(false);
    requestAnimationFrame(action);
  };

  return (
    <CommandDialog
      open={open}
      onOpenChange={setOpen}
      title="Command palette"
      description="Search for a command or connection"
      className="sm:max-w-lg"
    >
      <Command>
        <CommandInput placeholder="Search or run a command…" />
        <CommandList className="max-h-96">
          <CommandEmpty>No matching commands.</CommandEmpty>
          <CommandGroup heading="Connections">
            <CommandItem onSelect={run(openCreate)}>
              <Plus aria-hidden />
              New connection…
            </CommandItem>
            {profiles
              .filter((p) => p.id !== active?.id)
              .map((profile) => (
                <CommandItem
                  key={profile.id}
                  value={`switch ${profile.name} ${profile.host} ${profile.database} ${profile.env}`}
                  onSelect={run(() => switchConnection(profile))}
                >
                  <Plug aria-hidden />
                  <span className="truncate">Switch to {profile.name}</span>
                  <EnvBadge env={profile.env} className="ml-auto" />
                </CommandItem>
              ))}
          </CommandGroup>
          <QueryCommandItems run={run} />
          <AssistantCommandItems run={run} />
          <MigrationCommandItems run={run} />
          <CanvasCommandItems run={run} />
          <CommandGroup heading="View">
            <CommandItem onSelect={run(toggleTheme)}>
              <SunMoon aria-hidden />
              Toggle theme
            </CommandItem>
            <CommandItem onSelect={run(() => toggleExplorerView("schema"))}>
              <ListTree aria-hidden />
              {SHORTCUTS.toggleExplorer.label}
              <Shortcut combo={SHORTCUTS.toggleExplorer.combo} />
            </CommandItem>
            <CommandItem onSelect={run(() => togglePanel("dock"))}>
              <PanelBottom aria-hidden />
              {SHORTCUTS.toggleDock.label}
              <Shortcut combo={SHORTCUTS.toggleDock.combo} />
            </CommandItem>
            <CommandItem onSelect={run(() => togglePanel("assistant"))}>
              <Sparkles aria-hidden />
              {SHORTCUTS.toggleAssistant.label}
              <Shortcut combo={SHORTCUTS.toggleAssistant.combo} />
            </CommandItem>
          </CommandGroup>
          <CommandGroup heading="Help">
            <CommandItem onSelect={run(openShortcutSheet)}>
              <Keyboard aria-hidden />
              {SHORTCUTS.shortcutSheet.label}
              <Shortcut combo={SHORTCUTS.shortcutSheet.combo} />
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </Command>
    </CommandDialog>
  );
}

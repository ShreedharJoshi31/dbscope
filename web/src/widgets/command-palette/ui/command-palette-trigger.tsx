import { Search } from "lucide-react";
import { ariaKeyShortcut, SHORTCUTS } from "@/shared/config/shortcuts";
import { ShortcutKeys } from "@/shared/ui/shortcut-keys";
import { useCommandPalette } from "@/widgets/command-palette/model/palette-store";

/** Top-bar entry point that looks like a search field. */
export function CommandPaletteTrigger() {
  const setOpen = useCommandPalette((s) => s.setOpen);
  const combo = SHORTCUTS.commandPalette.combo;

  return (
    <button
      type="button"
      onClick={() => setOpen(true)}
      aria-keyshortcuts={ariaKeyShortcut(combo)}
      className="flex h-7 w-full items-center gap-2 rounded-md border border-border-default bg-surface-canvas px-2 text-xs text-text-muted transition-colors hover:border-border-control hover:text-text-primary"
    >
      <Search aria-hidden className="size-3.5" />
      <span className="truncate">Search or run a command</span>
      <ShortcutKeys combo={combo} className="ml-auto" />
    </button>
  );
}

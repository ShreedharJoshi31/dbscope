import { runCanvasCommand } from "@/entities/schema";
import { openShortcutSheet } from "@/features/show-shortcuts";
import { togglePanel } from "@/features/toggle-panel";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { type HotkeyBinding, useHotkeys } from "@/shared/lib/hotkeys";
import { focusAdjacentRegion } from "@/shared/lib/regions";
import { useCommandPalette } from "@/widgets/command-palette";

const SHEET_ONLY: HotkeyBinding[] = [{ combo: SHORTCUTS.shortcutSheet.combo, handler: openShortcutSheet }];

const WORKBENCH: HotkeyBinding[] = [
  ...SHEET_ONLY,
  { combo: SHORTCUTS.commandPalette.combo, handler: () => useCommandPalette.getState().toggle() },
  { combo: SHORTCUTS.toggleExplorer.combo, handler: () => togglePanel("explorer") },
  { combo: SHORTCUTS.toggleDock.combo, handler: () => togglePanel("dock") },
  { combo: SHORTCUTS.toggleAssistant.combo, handler: () => togglePanel("assistant") },
  { combo: SHORTCUTS.previousRegion.combo, handler: () => focusAdjacentRegion(-1) },
  { combo: SHORTCUTS.nextRegion.combo, handler: () => focusAdjacentRegion(1) },
  // Single keys: useHotkeys never fires these while typing in a field.
  { combo: SHORTCUTS.canvasSearch.combo, handler: () => runCanvasCommand("focusSearch") },
  { combo: SHORTCUTS.canvasFocusMode.combo, handler: () => runCanvasCommand("toggleFocus") },
];

/** Global shortcuts. Workbench-only bindings are inactive on onboarding. */
export function AppHotkeys({ workbench }: { workbench: boolean }) {
  useHotkeys(workbench ? WORKBENCH : SHEET_ONLY);
  return null;
}

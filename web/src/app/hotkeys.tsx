import { useActiveConnection } from "@/entities/connection";
import { openSqlTab } from "@/entities/query";
import { runCanvasCommand } from "@/entities/schema";
import { stopAsk } from "@/features/ask-assistant";
import { useQueryRunner } from "@/features/run-query";
import { openShortcutSheet } from "@/features/show-shortcuts";
import { toggleExplorerView, togglePanel } from "@/features/toggle-panel";
import { SHORTCUTS } from "@/shared/config/shortcuts";
import { type HotkeyBinding, isEditableTarget, useHotkeys } from "@/shared/lib/hotkeys";
import { focusAdjacentRegion } from "@/shared/lib/regions";
import { focusAssistantComposer, useCommandPalette } from "@/widgets/command-palette";

const SHEET_ONLY: HotkeyBinding[] = [{ combo: SHORTCUTS.shortcutSheet.combo, handler: openShortcutSheet }];

/** Global shortcuts. Workbench-only bindings are inactive on onboarding. */
export function AppHotkeys({ workbench }: { workbench: boolean }) {
  const { profile } = useActiveConnection();
  const runner = useQueryRunner();

  const bindings: HotkeyBinding[] = workbench
    ? [
        ...SHEET_ONLY,
        { combo: SHORTCUTS.commandPalette.combo, handler: () => useCommandPalette.getState().toggle() },
        { combo: SHORTCUTS.toggleExplorer.combo, handler: () => toggleExplorerView("schema") },
        { combo: SHORTCUTS.toggleDock.combo, handler: () => togglePanel("dock") },
        { combo: SHORTCUTS.toggleAssistant.combo, handler: () => togglePanel("assistant") },
        { combo: SHORTCUTS.previousRegion.combo, handler: () => focusAdjacentRegion(-1) },
        { combo: SHORTCUTS.nextRegion.combo, handler: () => focusAdjacentRegion(1) },
        // The SQL editor handles these itself while it has focus; elsewhere they act on the active SQL tab.
        // In other text fields (the assistant composer) they do nothing.
        {
          combo: SHORTCUTS.runQuery.combo,
          handler: (e) => !isEditableTarget(e.target) && runner.runActive(profile, "query"),
        },
        {
          combo: SHORTCUTS.explainQuery.combo,
          handler: (e) => !isEditableTarget(e.target) && runner.runActive(profile, "explain"),
        },
        // Cancel works from anywhere: the running query first, else the assistant's stream.
        { combo: SHORTCUTS.cancelQuery.combo, handler: () => runner.cancel() || (profile && stopAsk(profile.id)) },
        { combo: SHORTCUTS.newSqlTab.combo, handler: () => openSqlTab() },
        { combo: SHORTCUTS.focusAssistant.combo, handler: focusAssistantComposer },
        // Single keys: useHotkeys never fires these while typing in a field.
        { combo: SHORTCUTS.canvasSearch.combo, handler: () => runCanvasCommand("focusSearch") },
        { combo: SHORTCUTS.canvasFocusMode.combo, handler: () => runCanvasCommand("toggleFocus") },
      ]
    : SHEET_ONLY;
  useHotkeys(bindings);
  return null;
}

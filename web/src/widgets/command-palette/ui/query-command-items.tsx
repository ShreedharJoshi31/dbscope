import {
  BookText,
  Eraser,
  FilePlus2,
  GitCompareArrows,
  History,
  MessageSquareText,
  Play,
  ScanText,
  Settings2,
  Square,
} from "lucide-react";
import { useThreadStore } from "@/entities/assistant-turn";
import { useActiveConnection } from "@/entities/connection";
import { openSqlTab, useRunStore, useSqlTabs } from "@/entities/query";
import { clearCanvasOverlay, hasCanvasOverlay, useCanvasStore } from "@/entities/schema";
import { openCompare } from "@/features/compare-schema";
import { openGlossary } from "@/features/edit-glossary";
import { openLlmSettings } from "@/features/llm-settings";
import { useQueryRunner } from "@/features/run-query";
import { toggleExplorerView, usePanelStore } from "@/features/toggle-panel";
import { formatCombo, type KeyCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { CommandGroup, CommandItem, CommandShortcut } from "@/shared/ui/command";

function Shortcut({ combo }: { combo: KeyCombo }) {
  return <CommandShortcut className="font-mono tracking-normal">{formatCombo(combo)}</CommandShortcut>;
}

/** Opens the assistant (panel or drawer) and asks its composer to take focus. */
export function focusAssistantComposer(): void {
  const panels = usePanelStore.getState();
  if (panels.assistantMode === "drawer") panels.setAssistantDrawer(true);
  else panels.setOpen("assistant", true);
  // The panel animates open; focus once it is laid out.
  requestAnimationFrame(() => useThreadStore.getState().focusComposer());
}

type Run = (action: () => void) => () => void;

export function QueryCommandItems({ run }: { run: Run }) {
  const { profile } = useActiveConnection();
  const runner = useQueryRunner();
  const hasSqlTab = useSqlTabs((s) => s.tabs.some((t) => t.id === s.active));
  const running = useRunStore((s) => s.running !== null);
  return (
    <CommandGroup heading="Query">
      {profile && (
        <CommandItem value="compare schema drift snapshot connection" onSelect={run(openCompare)}>
          <GitCompareArrows aria-hidden />
          Compare schema…
        </CommandItem>
      )}
      <CommandItem value="new sql tab query editor" onSelect={run(() => openSqlTab())}>
        <FilePlus2 aria-hidden />
        {SHORTCUTS.newSqlTab.label}
        <Shortcut combo={SHORTCUTS.newSqlTab.combo} />
      </CommandItem>
      {hasSqlTab && (
        <>
          <CommandItem value="run query" onSelect={run(() => runner.runActive(profile, "query"))}>
            <Play aria-hidden />
            {SHORTCUTS.runQuery.label}
            <Shortcut combo={SHORTCUTS.runQuery.combo} />
          </CommandItem>
          <CommandItem value="explain query plan" onSelect={run(() => runner.runActive(profile, "explain"))}>
            <ScanText aria-hidden />
            {SHORTCUTS.explainQuery.label}
            <Shortcut combo={SHORTCUTS.explainQuery.combo} />
          </CommandItem>
        </>
      )}
      {running && (
        <CommandItem value="cancel query" onSelect={run(runner.cancel)}>
          <Square aria-hidden />
          {SHORTCUTS.cancelQuery.label}
          <Shortcut combo={SHORTCUTS.cancelQuery.combo} />
        </CommandItem>
      )}
      <CommandItem value="show query history" onSelect={run(() => toggleExplorerView("history"))}>
        <History aria-hidden />
        Show query history
      </CommandItem>
    </CommandGroup>
  );
}

export function AssistantCommandItems({ run }: { run: Run }) {
  const { profile } = useActiveConnection();
  const overlay = useCanvasStore(hasCanvasOverlay);
  return (
    <CommandGroup heading="Assistant">
      <CommandItem value="ask the assistant question" onSelect={run(focusAssistantComposer)}>
        <MessageSquareText aria-hidden />
        {SHORTCUTS.focusAssistant.label}
        <Shortcut combo={SHORTCUTS.focusAssistant.combo} />
      </CommandItem>
      <CommandItem value="assistant model llm settings provider api key" onSelect={run(openLlmSettings)}>
        <Settings2 aria-hidden />
        Assistant model settings…
      </CommandItem>
      {profile && (
        <CommandItem value="glossary terms definitions" onSelect={run(openGlossary)}>
          <BookText aria-hidden />
          Edit the glossary for {profile.name}…
        </CommandItem>
      )}
      {overlay && (
        <CommandItem value="clear query overlay heatmap canvas" onSelect={run(clearCanvasOverlay)}>
          <Eraser aria-hidden />
          Clear the query overlay on the canvas
        </CommandItem>
      )}
    </CommandGroup>
  );
}

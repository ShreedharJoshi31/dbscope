import { ArrowDown, ArrowUp, ClipboardPaste, Eraser, FileUp } from "lucide-react";
import { docTabsInScope, useSqlTabs } from "@/entities/query";
import { clearMigrationOverlay, hasMigrationOverlay, useCanvasStore } from "@/entities/schema";
import { openAnalyzeFiles, openPasteMigration, stepStatement } from "@/features/analyze-migration";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { CommandGroup, CommandItem, CommandShortcut } from "@/shared/ui/command";

type Run = (action: () => void) => () => void;

export function MigrationCommandItems({ run }: { run: Run }) {
  const overlay = useCanvasStore(hasMigrationOverlay);
  const inMigrationTab = useSqlTabs((s) => docTabsInScope(s.docTabs, s.scope).some((t) => t.id === s.active));
  return (
    <CommandGroup heading="Migration">
      <CommandItem value="analyze migration files folder sql risk" onSelect={run(openAnalyzeFiles)}>
        <FileUp aria-hidden />
        Analyze migration files…
      </CommandItem>
      <CommandItem value="paste migration sql analyze" onSelect={run(openPasteMigration)}>
        <ClipboardPaste aria-hidden />
        Paste migration SQL
      </CommandItem>
      {inMigrationTab && (
        <>
          <CommandItem value="migration next statement" onSelect={run(() => stepStatement(1))}>
            <ArrowDown aria-hidden />
            {SHORTCUTS.migrationNext.label}
            <CommandShortcut className="font-mono tracking-normal">
              {formatCombo(SHORTCUTS.migrationNext.combo)}
            </CommandShortcut>
          </CommandItem>
          <CommandItem value="migration previous statement" onSelect={run(() => stepStatement(-1))}>
            <ArrowUp aria-hidden />
            {SHORTCUTS.migrationPrevious.label}
            <CommandShortcut className="font-mono tracking-normal">
              {formatCombo(SHORTCUTS.migrationPrevious.combo)}
            </CommandShortcut>
          </CommandItem>
        </>
      )}
      {overlay && (
        <CommandItem
          value="clear migration overlay canvas diff"
          onSelect={run(() => {
            clearMigrationOverlay();
            announce("Cleared the migration overlay.");
          })}
        >
          <Eraser aria-hidden />
          Clear migration overlay
        </CommandItem>
      )}
    </CommandGroup>
  );
}

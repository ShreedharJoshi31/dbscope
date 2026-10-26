import { useEffect } from "react";
import { useActiveConnectionStore } from "@/entities/connection";
import { ConnectionFormDialog } from "@/features/add-connection";
import { DeleteConnectionDialog } from "@/features/delete-connection";
import { switchConnection } from "@/features/switch-connection";
import { AppShell } from "@/widgets/app-shell";
import { AssistantPanel } from "@/widgets/assistant-panel";
import { CommandPalette, CommandPaletteTrigger, useCommandPalette } from "@/widgets/command-palette";
import { ErdCanvas } from "@/widgets/erd-canvas";
import { ResultsDock } from "@/widgets/results-dock";
import { SchemaExplorer } from "@/widgets/schema-explorer";

export function WorkbenchPage() {
  const setPaletteOpen = useCommandPalette((s) => s.setOpen);

  // Arriving from onboarding leaves focus on <body>; start keyboard users in the workspace.
  useEffect(() => {
    if (document.activeElement === document.body) {
      document.querySelector<HTMLElement>('[data-region="centre"]')?.focus();
    }
  }, []);

  return (
    <>
      <h1 className="sr-only">dbscope workbench</h1>
      <AppShell
        search={<CommandPaletteTrigger />}
        explorer={<SchemaExplorer />}
        erd={<ErdCanvas onOpenCommandPalette={() => setPaletteOpen(true)} />}
        dock={<ResultsDock />}
        assistant={<AssistantPanel />}
      />
      <CommandPalette />
      <ConnectionFormDialog
        onSaved={(profile, mode) => {
          if (mode === "create") switchConnection(profile);
        }}
      />
      <DeleteConnectionDialog
        onDeleted={(profile) => {
          const { activeId, setActiveId } = useActiveConnectionStore.getState();
          if (activeId === profile.id) setActiveId(null);
        }}
      />
    </>
  );
}

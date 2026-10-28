import { useEffect, useRef } from "react";
import { ENGINE_META, useActiveConnection, useActiveConnectionStore } from "@/entities/connection";
import { type DocTab, type SqlTab, useRunStore, useSqlTabsScope } from "@/entities/query";
import { useCanvasStore, useWorkspaceStore } from "@/entities/schema";
import { ConnectionFormDialog } from "@/features/add-connection";
import { AnalyzeMigrationDialog, MigrationDropTarget } from "@/features/analyze-migration";
import { DeleteConnectionDialog } from "@/features/delete-connection";
import { GlossaryDialog } from "@/features/edit-glossary";
import { LlmSettingsDialog } from "@/features/llm-settings";
import { RunConfirmDialog } from "@/features/run-query";
import { ImportSharedDialog } from "@/features/share-state";
import { switchConnection } from "@/features/switch-connection";
import { usePanelStore } from "@/features/toggle-panel";
import { AppShell } from "@/widgets/app-shell";
import { AssistantPanel } from "@/widgets/assistant-panel";
import { CommandPalette, CommandPaletteTrigger } from "@/widgets/command-palette";
import { ErdCanvas } from "@/widgets/erd-canvas";
import { MigrationReviewTab } from "@/widgets/migration-review";
import { QueryHistory } from "@/widgets/query-history";
import { ResultsDock } from "@/widgets/results-dock";
import { SchemaExplorer } from "@/widgets/schema-explorer";
import { SqlEditor } from "@/widgets/sql-editor";

const renderSqlTab = (tab: SqlTab) => <SqlEditor tab={tab} />;
const renderDocTab = (tab: DocTab) => <MigrationReviewTab id={tab.id} />;

/**
 * SQL tabs are kept per connection (and separately for the sample workspace);
 * the dock's results, plan and messages, and the canvas overlay a query
 * drew, belong to the connection they ran on.
 */
function useTabScope() {
  const { profile } = useActiveConnection();
  const sample = useWorkspaceStore((s) => s.source === "sample");
  const scope = sample ? "sample" : (profile?.id ?? null);
  useSqlTabsScope(scope, (profile && !sample && ENGINE_META[profile.engine].fileExtension) || "sql");
  const first = useRef(scope);
  useEffect(() => {
    if (scope !== first.current) {
      first.current = scope;
      useRunStore.getState().reset();
      useCanvasStore.getState().clearOverlay();
    }
  }, [scope]);
}

/** The dock is collapsed until there is something to show; a run or "Open in results" opens it. */
function useOpenDockOnRun() {
  const revision = useRunStore((s) => s.revision);
  const first = useRef(revision);
  useEffect(() => {
    if (revision !== first.current) usePanelStore.getState().setOpen("dock", true);
  }, [revision]);
}

export function WorkbenchPage() {
  useTabScope();
  useOpenDockOnRun();

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
        history={<QueryHistory />}
        erd={<ErdCanvas />}
        renderSqlTab={renderSqlTab}
        renderDocTab={renderDocTab}
        dock={<ResultsDock />}
        assistant={<AssistantPanel />}
      />
      <CommandPalette />
      <MigrationDropTarget />
      <AnalyzeMigrationDialog />
      <RunConfirmDialog />
      <LlmSettingsDialog />
      <GlossaryDialog />
      <ImportSharedDialog />
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

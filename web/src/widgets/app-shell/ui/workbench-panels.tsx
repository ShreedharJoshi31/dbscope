import type { ReactNode } from "react";
import { Group, Panel, useDefaultLayout } from "react-resizable-panels";
import { safeLocalStorage } from "@/shared/lib/storage";
import { useSyncedPanel } from "@/widgets/app-shell/model/use-synced-panel";
import { ResizeHandle } from "@/widgets/app-shell/ui/resize-handle";

interface WorkbenchPanelsProps {
  /** At 1280px and wider the assistant is a docked panel; below, AppShell renders a drawer. */
  wide: boolean;
  explorer: ReactNode;
  centre: ReactNode;
  dock: ReactNode;
  assistant: ReactNode;
}

/** The dock starts collapsed until the user opens it (later: until the first query run). */
const FIRST_RUN_CENTRE_LAYOUT = { editor: 100, dock: 0 };

export function WorkbenchPanels({ wide, explorer, centre, dock, assistant }: WorkbenchPanelsProps) {
  const mainLayout = useDefaultLayout({
    id: wide ? "dbscope.layout.main" : "dbscope.layout.main-narrow",
    storage: safeLocalStorage,
  });
  const centreLayout = useDefaultLayout({ id: "dbscope.layout.centre", storage: safeLocalStorage });
  const explorerPanel = useSyncedPanel("explorer");
  const dockPanel = useSyncedPanel("dock");
  const assistantPanel = useSyncedPanel("assistant");

  return (
    <Group
      orientation="horizontal"
      defaultLayout={mainLayout.defaultLayout}
      onLayoutChanged={mainLayout.onLayoutChanged}
      className="min-w-0 flex-1"
    >
      <Panel
        id="explorer"
        panelRef={explorerPanel.panelRef}
        onResize={explorerPanel.onResize}
        collapsible
        collapsedSize={0}
        defaultSize={240}
        minSize={180}
        maxSize={480}
        groupResizeBehavior="preserve-pixel-size"
      >
        <aside
          aria-label="Schema explorer"
          data-region="explorer"
          tabIndex={-1}
          className="flex h-full min-w-0 flex-col bg-surface-subtle"
        >
          {explorer}
        </aside>
      </Panel>
      <ResizeHandle label="Resize schema explorer" orientation="vertical" />

      <Panel id="centre" minSize={320}>
        <main aria-label="Workspace" className="flex h-full min-w-0 flex-col">
          <Group
            orientation="vertical"
            defaultLayout={centreLayout.defaultLayout ?? FIRST_RUN_CENTRE_LAYOUT}
            onLayoutChanged={centreLayout.onLayoutChanged}
          >
            <Panel id="editor" minSize={120}>
              <div data-region="centre" tabIndex={-1} className="flex h-full min-h-0 flex-col">
                {centre}
              </div>
            </Panel>
            <ResizeHandle label="Resize results dock" orientation="horizontal" />
            <Panel
              id="dock"
              panelRef={dockPanel.panelRef}
              onResize={dockPanel.onResize}
              collapsible
              collapsedSize={0}
              defaultSize={280}
              minSize={120}
              groupResizeBehavior="preserve-pixel-size"
            >
              <section
                aria-label="Results dock"
                data-region="dock"
                tabIndex={-1}
                className="flex h-full min-h-0 flex-col bg-surface-canvas"
              >
                {dock}
              </section>
            </Panel>
          </Group>
        </main>
      </Panel>

      {wide && <ResizeHandle label="Resize assistant" orientation="vertical" />}
      {wide && (
        <Panel
          id="assistant"
          panelRef={assistantPanel.panelRef}
          onResize={assistantPanel.onResize}
          collapsible
          collapsedSize={0}
          defaultSize={360}
          minSize={280}
          maxSize={640}
          groupResizeBehavior="preserve-pixel-size"
        >
          <aside
            aria-label="Assistant"
            data-region="assistant"
            tabIndex={-1}
            className="flex h-full min-w-0 flex-col bg-surface-subtle"
          >
            {assistant}
          </aside>
        </Panel>
      )}
    </Group>
  );
}

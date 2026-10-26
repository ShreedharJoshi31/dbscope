import { type ReactNode, useEffect } from "react";
import { useActiveConnection } from "@/entities/connection";
import { usePanelStore } from "@/features/toggle-panel";
import { useMediaQuery } from "@/shared/lib/use-media-query";
import { useDocumentTitle } from "@/widgets/app-shell/model/document-title";
import { AssistantDrawer } from "@/widgets/app-shell/ui/assistant-drawer";
import { CentreTabs } from "@/widgets/app-shell/ui/centre-tabs";
import { NavRail } from "@/widgets/app-shell/ui/nav-rail";
import { ProdBanner } from "@/widgets/app-shell/ui/prod-banner";
import { StatusBar } from "@/widgets/app-shell/ui/status-bar";
import { TopBar } from "@/widgets/app-shell/ui/top-bar";
import { WorkbenchPanels } from "@/widgets/app-shell/ui/workbench-panels";

export const WIDE_QUERY = "(min-width: 1280px)";

interface AppShellProps {
  search: ReactNode;
  explorer: ReactNode;
  erd: ReactNode;
  dock: ReactNode;
  assistant: ReactNode;
}

/** The workbench frame from docs/ARCHITECTURE.md; widgets fill its slots. */
export function AppShell({ search, explorer, erd, dock, assistant }: AppShellProps) {
  const { profile } = useActiveConnection();
  const wide = useMediaQuery(WIDE_QUERY);
  const setAssistantMode = usePanelStore((s) => s.setAssistantMode);
  useDocumentTitle(profile);

  useEffect(() => {
    setAssistantMode(wide ? "panel" : "drawer");
  }, [wide, setAssistantMode]);

  const focusCentre = () => document.querySelector<HTMLElement>('[data-region="centre"]')?.focus();

  return (
    <div className="flex h-full flex-col bg-surface-canvas">
      <header data-region="top" tabIndex={-1} className="shrink-0">
        {profile?.env === "prod-replica" && <ProdBanner profile={profile} />}
        <TopBar search={search} />
      </header>
      <div className="flex min-h-0 flex-1">
        <NavRail onShowErd={focusCentre} />
        <WorkbenchPanels
          key={wide ? "wide" : "narrow"}
          wide={wide}
          explorer={explorer}
          centre={<CentreTabs erd={erd} />}
          dock={dock}
          assistant={assistant}
        />
      </div>
      <StatusBar profile={profile} />
      {!wide && <AssistantDrawer>{assistant}</AssistantDrawer>}
    </div>
  );
}

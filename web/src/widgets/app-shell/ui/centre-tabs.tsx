import { cn } from "cn";
import { FileCode2, FileWarning, Network, Plus, X } from "lucide-react";
import type { ReactNode } from "react";
import { type DocTab, docTabsInScope, ERD_TAB, openSqlTab, type SqlTab, useSqlTabs } from "@/entities/query";
import { closeMigrationReview } from "@/features/analyze-migration";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/shared/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

interface CentreTabsProps {
  erd: ReactNode;
  renderSqlTab: (tab: SqlTab) => ReactNode;
  renderDocTab: (tab: DocTab) => ReactNode;
}

function closeTab(tab: SqlTab | DocTab) {
  if ("kind" in tab && tab.kind === "migration") closeMigrationReview(tab.id);
  else useSqlTabs.getState().closeTab(tab.id);
  announce(`Closed ${tab.title}.`);
}

interface StripTabProps {
  tab: SqlTab | DocTab;
  active: boolean;
  icon: typeof FileCode2;
}

function StripTab({ tab, active, icon: Icon }: StripTabProps) {
  return (
    <div className="group/tab flex h-8 flex-none items-center">
      <TabsTrigger
        value={tab.id}
        className="h-8 rounded-none pr-1 pl-3 font-mono text-xs"
        onKeyDown={(e) => {
          if (e.key === "Delete") {
            e.preventDefault();
            closeTab(tab);
          }
        }}
        title={`${tab.title} (Delete closes the tab)`}
      >
        <Icon aria-hidden />
        {tab.title}
      </TabsTrigger>
      <button
        type="button"
        tabIndex={-1}
        aria-label={`Close ${tab.title}`}
        onClick={() => closeTab(tab)}
        className={cn(
          "mr-1 flex size-5 items-center justify-center rounded-sm text-text-muted hover:bg-surface-hover hover:text-text-primary",
          !active && "opacity-0 group-hover/tab:opacity-100 focus-visible:opacity-100",
        )}
      >
        <X aria-hidden className="size-3" />
      </button>
    </div>
  );
}

/** The centre strip: the ERD, SQL tabs and migration tabs. Every panel stays mounted so they keep their state. */
export function CentreTabs({ erd, renderSqlTab, renderDocTab }: CentreTabsProps) {
  const tabs = useSqlTabs((s) => s.tabs);
  const active = useSqlTabs((s) => s.active);
  const scope = useSqlTabs((s) => s.scope);
  const allDocTabs = useSqlTabs((s) => s.docTabs);
  const docTabs = docTabsInScope(allDocTabs, scope);
  const newTab = SHORTCUTS.newSqlTab.combo;

  return (
    <Tabs
      value={active}
      onValueChange={(v) => useSqlTabs.getState().setActive(String(v))}
      className="flex h-full min-h-0 flex-col gap-0"
    >
      <div className="flex h-8 shrink-0 items-end gap-1 border-b border-border-default bg-surface-subtle px-2">
        <TabsList variant="line" aria-label="Open tabs" className="h-8 min-w-0 gap-0 overflow-x-auto p-0">
          <TabsTrigger value={ERD_TAB} className="h-8 flex-none rounded-none px-3 text-xs">
            <Network aria-hidden />
            ERD
          </TabsTrigger>
          {tabs.map((tab) => (
            <StripTab key={tab.id} tab={tab} active={active === tab.id} icon={FileCode2} />
          ))}
          {docTabs.map((tab) => (
            <StripTab key={tab.id} tab={tab} active={active === tab.id} icon={FileWarning} />
          ))}
        </TabsList>
        <Tooltip>
          <TooltipTrigger
            render={
              <button
                type="button"
                aria-label={`New SQL tab (${formatCombo(newTab)})`}
                onClick={() => openSqlTab()}
                className="mb-1 flex size-6 shrink-0 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover hover:text-text-primary"
              />
            }
          >
            <Plus aria-hidden className="size-3.5" />
          </TooltipTrigger>
          <TooltipContent side="bottom">New SQL tab {formatCombo(newTab)}</TooltipContent>
        </Tooltip>
      </div>
      <TabsContent value={ERD_TAB} keepMounted className="min-h-0 flex-1 bg-surface-canvas">
        {erd}
      </TabsContent>
      {tabs.map((tab) => (
        <TabsContent key={tab.id} value={tab.id} keepMounted className="min-h-0 flex-1 bg-surface-canvas">
          {renderSqlTab(tab)}
        </TabsContent>
      ))}
      {docTabs.map((tab) => (
        <TabsContent key={tab.id} value={tab.id} keepMounted className="min-h-0 flex-1 bg-surface-canvas">
          {renderDocTab(tab)}
        </TabsContent>
      ))}
    </Tabs>
  );
}

import { cn } from "cn";
import { FileCode2, Network, Plus, X } from "lucide-react";
import type { ReactNode } from "react";
import { ERD_TAB, openSqlTab, type SqlTab, useSqlTabs } from "@/entities/query";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { announce } from "@/shared/lib/announcer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/shared/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

interface CentreTabsProps {
  erd: ReactNode;
  renderSqlTab: (tab: SqlTab) => ReactNode;
}

function closeTab(tab: SqlTab) {
  useSqlTabs.getState().closeTab(tab.id);
  announce(`Closed ${tab.title}.`);
}

/** The centre strip: the ERD plus SQL tabs. Every panel stays mounted so the canvas and editors keep their state. */
export function CentreTabs({ erd, renderSqlTab }: CentreTabsProps) {
  const tabs = useSqlTabs((s) => s.tabs);
  const active = useSqlTabs((s) => s.active);
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
            <div key={tab.id} className="group/tab flex h-8 flex-none items-center">
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
                <FileCode2 aria-hidden />
                {tab.title}
              </TabsTrigger>
              <button
                type="button"
                tabIndex={-1}
                aria-label={`Close ${tab.title}`}
                onClick={() => closeTab(tab)}
                className={cn(
                  "mr-1 flex size-5 items-center justify-center rounded-sm text-text-muted hover:bg-surface-hover hover:text-text-primary",
                  active !== tab.id && "opacity-0 group-hover/tab:opacity-100 focus-visible:opacity-100",
                )}
              >
                <X aria-hidden className="size-3" />
              </button>
            </div>
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
    </Tabs>
  );
}

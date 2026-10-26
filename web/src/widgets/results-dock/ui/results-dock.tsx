import { FileWarning, ListTree, type LucideIcon, MessageSquareText, PanelBottomClose, Table2 } from "lucide-react";
import { togglePanel } from "@/features/toggle-panel";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/shared/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

interface DockTab {
  value: string;
  label: string;
  icon: LucideIcon;
  title: string;
  description: string;
}

const TABS: DockTab[] = [
  {
    value: "results",
    label: "Results",
    icon: Table2,
    title: "No results yet",
    description: "Rows from the queries you run appear here with the row count and timing.",
  },
  {
    value: "messages",
    label: "Messages",
    icon: MessageSquareText,
    title: "No messages",
    description: "Server notices, warnings and errors from your queries appear here, with SQLSTATE and hints.",
  },
  {
    value: "plan",
    label: "Plan",
    icon: ListTree,
    title: "No plan",
    description: "The EXPLAIN plan for a query appears here as a tree with estimated and actual costs.",
  },
  {
    value: "risk",
    label: "Risk",
    icon: FileWarning,
    title: "No migration analysed",
    description: "Findings from migration analysis appear here, ordered from critical to low risk.",
  },
];

export function ResultsDock() {
  const hide = () => togglePanel("dock");
  const combo = SHORTCUTS.toggleDock.combo;

  return (
    <Tabs defaultValue="results" className="flex h-full min-h-0 flex-col gap-0">
      <div className="flex h-8 shrink-0 items-end border-b border-border-default bg-surface-subtle px-2">
        <TabsList variant="line" aria-label="Results dock" className="h-8 gap-0 p-0">
          {TABS.map((tab) => (
            <TabsTrigger key={tab.value} value={tab.value} className="h-8 flex-none rounded-none px-3 text-xs">
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                className="mb-1 ml-auto"
                aria-label={`Hide results dock (${formatCombo(combo)})`}
                onClick={hide}
              />
            }
          >
            <PanelBottomClose aria-hidden />
          </TooltipTrigger>
          <TooltipContent side="top">Hide results dock {formatCombo(combo)}</TooltipContent>
        </Tooltip>
      </div>
      {TABS.map((tab) => (
        <TabsContent key={tab.value} value={tab.value} className="min-h-0 flex-1">
          <EmptyState
            icon={tab.icon}
            title={tab.title}
            description={tab.description}
            action={{ label: "Hide dock", onClick: hide, combo }}
          />
        </TabsContent>
      ))}
    </Tabs>
  );
}

import { PanelBottomClose } from "lucide-react";
import type { ReactNode } from "react";
import { type DockTab, useRunStore } from "@/entities/query";
import { togglePanel } from "@/features/toggle-panel";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { Button } from "@/shared/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/shared/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";
import { MessagesTab } from "@/widgets/results-dock/ui/messages-tab";
import { PlanTab } from "@/widgets/results-dock/ui/plan-tab";
import { ResultsTab } from "@/widgets/results-dock/ui/results-tab";
import { RiskTab } from "@/widgets/results-dock/ui/risk-tab";

function MessagesLabel() {
  const errors = useRunStore((s) => s.messages.filter((m) => m.level === "error").length);
  return (
    <>
      Messages
      {errors > 0 && (
        <span className="ml-1 rounded-full border border-status-danger-fg px-1 font-mono text-2xs text-status-danger-fg">
          {errors}
          <span className="sr-only"> errors</span>
        </span>
      )}
    </>
  );
}

const TABS: { value: DockTab; label: ReactNode; content: ReactNode }[] = [
  { value: "results", label: "Results", content: <ResultsTab /> },
  { value: "messages", label: <MessagesLabel />, content: <MessagesTab /> },
  { value: "plan", label: "Plan", content: <PlanTab /> },
  { value: "risk", label: "Risk", content: <RiskTab /> },
];

export function ResultsDock() {
  const tab = useRunStore((s) => s.dockTab);
  const hide = () => togglePanel("dock");
  const combo = SHORTCUTS.toggleDock.combo;

  return (
    <Tabs
      value={tab}
      onValueChange={(v) => useRunStore.getState().setDockTab(v as DockTab)}
      className="flex h-full min-h-0 flex-col gap-0"
    >
      <div className="flex h-8 shrink-0 items-end border-b border-border-default bg-surface-subtle px-2">
        <TabsList variant="line" aria-label="Results dock" className="h-8 gap-0 p-0">
          {TABS.map((t) => (
            <TabsTrigger key={t.value} value={t.value} className="h-8 flex-none rounded-none px-3 text-xs">
              {t.label}
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
      {TABS.map((t) => (
        <TabsContent key={t.value} value={t.value} className="min-h-0 flex-1">
          {t.content}
        </TabsContent>
      ))}
    </Tabs>
  );
}

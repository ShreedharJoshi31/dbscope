import { cn } from "cn";
import { Activity, Info, LoaderCircle, TriangleAlert } from "lucide-react";
import { useEffect, useRef } from "react";
import { type OverlayEntry, useCanvasStore } from "@/entities/schema";
import {
  METRIC_LABELS,
  STATEMENT_METRICS,
  useWorkloadView,
  type WorkloadMetric,
  workloadEntries,
  workloadSummary,
} from "@/features/show-workload/model/workload";
import { useWorkload } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { SegmentedControl } from "@/shared/ui/segmented-control";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/tooltip";

const ALL: WorkloadMetric[] = ["time", "calls", "reads", "writes"];

/**
 * Toolbar control for workload heat (docs/WORKLOAD.md). The badges share the
 * canvas overlay with query plans: running a query replaces them, and the
 * control then switches itself off.
 */
export function WorkloadControl({ profileId }: { profileId: string | undefined }) {
  const { on, metric, setOn, setMetric } = useWorkloadView();
  const workload = useWorkload(profileId, on);
  const applied = useRef<Record<string, OverlayEntry> | null>(null);
  const statementsOk = workload.data?.statements.status === "ok";
  const options = ALL.filter((m) => statementsOk || !STATEMENT_METRICS.has(m)).map((value) => ({
    value,
    label: METRIC_LABELS[value],
  }));
  const shown: WorkloadMetric = statementsOk || !STATEMENT_METRICS.has(metric) ? metric : "reads";

  // The overlay when Workload was turned on: if it changes before the data
  // arrives (a query plan ran meanwhile), the user moved on and nothing is applied.
  const baseline = useRef<object | null>(null);
  const announced = useRef("");
  useEffect(() => {
    if (on) baseline.current = useCanvasStore.getState().overlay;
  }, [on]);

  useEffect(() => {
    if (!on || !workload.data) return;
    const current = useCanvasStore.getState().overlay;
    if (current !== baseline.current && current !== applied.current) {
      setOn(false);
      return;
    }
    const entries = workloadEntries(workload.data, shown);
    // Clear first (a query plan's dimming goes too), then mark the badges as ours.
    applied.current = null;
    useCanvasStore.getState().clearOverlay();
    applied.current = entries;
    baseline.current = entries;
    useCanvasStore.getState().setOverlay(entries);
    // Announce what the user changed, not every background refresh.
    if (announced.current !== shown) {
      announced.current = shown;
      announce(`Workload shown by ${METRIC_LABELS[shown].toLowerCase()} on ${Object.keys(entries).length} tables.`);
    }
  }, [on, workload.data, shown, setOn]);

  // Something else (a query plan, Clear overlay) replaced the badges: stop claiming to show them.
  useEffect(
    () =>
      useCanvasStore.subscribe((s) => {
        if (applied.current && s.overlay !== applied.current) {
          applied.current = null;
          useWorkloadView.getState().setOn(false);
        }
      }),
    [],
  );

  const removeOurs = () => {
    if (applied.current && useCanvasStore.getState().overlay === applied.current) {
      useCanvasStore.getState().clearOverlay();
    }
    applied.current = null;
    announced.current = "";
  };
  // Switching off removes the badges if they are still ours.
  // biome-ignore lint/correctness/useExhaustiveDependencies: removeOurs reads refs only.
  useEffect(() => {
    if (!on) removeOurs();
  }, [on]);
  // Another connection: never show this one's numbers on it, and do not read
  // its pg_stat_statements (perhaps a production replica) unasked.
  // biome-ignore lint/correctness/useExhaustiveDependencies: profileId is the trigger; removeOurs reads refs only.
  useEffect(
    () => () => {
      removeOurs();
      useWorkloadView.getState().setOn(false);
    },
    [profileId],
  );

  if (!profileId) return null; // the sample schema has no traffic

  return (
    <>
      <Tooltip>
        <TooltipTrigger
          render={
            <button
              type="button"
              aria-label={on ? "Hide workload" : "Show workload: which tables real queries use"}
              aria-pressed={on}
              onClick={() => setOn(!on)}
              className={cn(
                "flex h-7 shrink-0 items-center gap-1 rounded-md px-1.5 text-xs text-text-muted transition-colors",
                "hover:bg-surface-hover hover:text-text-primary",
                on && "bg-surface-selected text-text-primary",
              )}
            />
          }
        >
          {on && workload.isFetching ? (
            <LoaderCircle aria-hidden className="size-4 animate-spin" />
          ) : (
            <Activity aria-hidden className="size-4" strokeWidth={1.75} />
          )}
          Workload
        </TooltipTrigger>
        <TooltipContent side="bottom">
          {on ? "Hide workload" : "Show which tables real queries use (pg_stat_statements and table counters)"}
        </TooltipContent>
      </Tooltip>
      {on && workload.isSuccess && (
        <>
          <span id="erd-workload-metric" className="sr-only">
            Workload metric
          </span>
          <SegmentedControl
            aria-labelledby="erd-workload-metric"
            options={options}
            value={shown}
            onValueChange={setMetric}
            size="sm"
          />
          <Tooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  aria-label="What the workload numbers cover"
                  className="flex size-7 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover"
                />
              }
            >
              {statementsOk ? (
                <Info aria-hidden className="size-4" />
              ) : (
                <TriangleAlert aria-hidden className="size-4 text-status-warn-fg" />
              )}
            </TooltipTrigger>
            <TooltipContent side="bottom" className="max-w-sm">
              {workloadSummary(workload.data, shown)}
            </TooltipContent>
          </Tooltip>
        </>
      )}
      {on && workload.isError && (
        <span role="alert" className="text-2xs text-status-danger-fg">
          Could not read the workload: {workload.error.message}
        </span>
      )}
    </>
  );
}

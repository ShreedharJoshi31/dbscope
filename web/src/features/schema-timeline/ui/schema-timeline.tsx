import { useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import { ChevronLeft, ChevronRight, LoaderCircle } from "lucide-react";
import { Fragment, type KeyboardEvent, useCallback, useEffect, useId, useMemo, useRef } from "react";
import { buildTimeline, NO_OBSERVATION, type TimelineStep, useCanvasStore } from "@/entities/schema";
import {
  type ShownStep,
  showLive,
  showStep,
  TIMELINE_SOURCE,
  useTimelineStore,
} from "@/features/schema-timeline/model/timeline-store";
import { fetchSnapshot, schemaQueryKeys, useProfileSchema, useSnapshots } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { formatAge, useNow } from "@/shared/lib/relative-time";
import { Button } from "@/shared/ui/button";

const SHORT = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
const FULL = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" });

function format(f: Intl.DateTimeFormat, iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : f.format(d);
}

/** Bar widths in px: the longest observed span gets the most, so spans compare at a glance. */
const BAR_MIN = 8;
const BAR_EXTRA = 40;

function span(step: TimelineStep): number {
  const ms = Date.parse(step.lastSeenAt) - Date.parse(step.takenAt);
  return Number.isFinite(ms) && ms > 0 ? ms : 0;
}

function stepName(step: TimelineStep, now: number): string {
  const before =
    step.gap.kind === "unobserved"
      ? `Before it, ${NO_OBSERVATION} from ${format(FULL, step.gap.from)} to ${format(FULL, step.gap.to)}.`
      : "The first observation.";
  return `Snapshot from ${format(FULL, step.takenAt)} (${formatAge(step.takenAt, now)}), observed until ${format(FULL, step.lastSeenAt)}. ${before}`;
}

function describeShown(s: ShownStep): string {
  if (!s.changes)
    return `Showing the snapshot from ${s.label}, the first observation. There is nothing before it to compare with.`;
  const { added, removed, changed } = s.changes;
  return `Showing the snapshot from ${s.label}: ${added.length} added, ${removed.length} removed, ${changed.length} changed since the previous snapshot.`;
}

function Gap({ from, to }: { from: string; to: string }) {
  return (
    <div
      aria-hidden
      data-testid="timeline-gap"
      title={`No observation from ${format(FULL, from)} to ${format(FULL, to)}. The schema changed at some point in this time.`}
      className="flex min-w-24 flex-1 flex-col items-stretch gap-1 self-start px-1 pt-[5px]"
    >
      <span className="border-t border-dashed border-border-control" />
      <span className="text-center text-2xs whitespace-nowrap text-text-muted">{NO_OBSERVATION}</span>
    </div>
  );
}

const OPTION =
  "flex shrink-0 cursor-pointer flex-col items-start gap-1 rounded-sm px-1 py-0.5 text-2xs text-text-muted outline-none hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus aria-selected:font-semibold aria-selected:text-text-primary";

/**
 * Steps the canvas through the connection's stored schemas, oldest first, and
 * marks what each changed against the one before it. Hidden until there are
 * two snapshots to step between.
 */
export function SchemaTimeline({ profileId }: { profileId: string }) {
  const snapshots = useSnapshots(profileId);
  const live = useProfileSchema(profileId);
  const now = useNow(60_000);
  const timeline = useMemo(() => buildTimeline(snapshots.data ?? [], now), [snapshots.data, now]);
  const shown = useTimelineStore((s) => s.shown);
  const pending = useTimelineStore((s) => s.pending);
  const error = useTimelineStore((s) => s.error);
  const diffSource = useCanvasStore((s) => s.diff?.sourceId ?? null);
  const client = useQueryClient();
  const baseId = useId();

  // Reading the live schema can save or touch a snapshot: list them again after it.
  const seen = useRef(live.dataUpdatedAt);
  const { refetch } = snapshots;
  useEffect(() => {
    if (seen.current === live.dataUpdatedAt) return;
    seen.current = live.dataUpdatedAt;
    void refetch();
  }, [live.dataUpdatedAt, refetch]);

  // Esc, a comparison or a migration replaced the timeline's overlay: go back to live and leave theirs alone.
  useEffect(() => {
    const s = useTimelineStore.getState();
    if (diffSource !== TIMELINE_SOURCE && (s.shown || s.pending !== null)) showLive();
  }, [diffSource]);

  useEffect(() => () => showLive(), []);

  const { steps } = timeline;
  const liveIndex = steps.length;
  const chosen = pending ?? shown?.id ?? null;
  const found = chosen === null ? -1 : steps.findIndex((s) => s.id === chosen);
  const index = found === -1 ? liveIndex : found;
  const optionId = (i: number) => `${baseId}-option-${i}`;

  useEffect(() => {
    document.getElementById(`${baseId}-option-${index}`)?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [baseId, index]);

  const load = useCallback(
    (id: number) =>
      client.fetchQuery({
        queryKey: schemaQueryKeys.snapshot(profileId, id),
        queryFn: () => fetchSnapshot(profileId, id),
        staleTime: Number.POSITIVE_INFINITY,
      }),
    [client, profileId],
  );

  if (steps.length < 2) return null;

  const select = (i: number) => {
    const next = Math.max(0, Math.min(liveIndex, i));
    if (next === index && pending === null) return;
    if (next === liveIndex) {
      showLive();
      announce("Showing the live schema.");
      return;
    }
    const step = steps[next];
    if (!step) return;
    void showStep(profileId, steps, next, format(SHORT, step.takenAt), load).then(() => {
      const s = useTimelineStore.getState().shown;
      if (s?.id === step.id) announce(describeShown(s));
    });
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const moves: Record<string, number> = {
      ArrowLeft: index - 1,
      ArrowRight: index + 1,
      Home: 0,
      End: liveIndex,
    };
    const target = moves[event.key];
    if (target === undefined) return;
    event.preventDefault();
    select(target);
    // Roving tabindex: focus follows the chosen stop.
    document.getElementById(optionId(Math.max(0, Math.min(liveIndex, target))))?.focus();
  };

  const longest = Math.max(...steps.map(span));

  return (
    <section
      aria-label="Schema timeline"
      data-testid="schema-timeline"
      className="pointer-events-auto flex max-w-full min-w-0 flex-col gap-1 rounded-md border border-border-default bg-surface-raised p-1 text-xs shadow-sm"
    >
      <div className="flex min-w-0 items-center gap-1">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Previous snapshot"
          title="Previous snapshot"
          disabled={index === 0}
          onClick={() => select(index - 1)}
        >
          <ChevronLeft aria-hidden />
        </Button>
        <div
          role="listbox"
          aria-label="Schema snapshots, oldest first"
          aria-orientation="horizontal"
          aria-busy={pending !== null}
          onKeyDown={onKeyDown}
          className="flex min-w-0 items-start overflow-x-auto px-0.5 py-0.5 outline-none"
        >
          {steps.map((step, i) => (
            <Fragment key={step.id}>
              {step.gap.kind === "unobserved" && <Gap from={step.gap.from} to={step.gap.to} />}
              {/* biome-ignore lint/a11y/useKeyWithClickEvents: the listbox handles the keys for its options. */}
              <div
                id={optionId(i)}
                role="option"
                aria-selected={i === index}
                tabIndex={i === index ? 0 : -1}
                aria-label={stepName(step, timeline.now)}
                data-testid="timeline-step"
                data-step={step.id}
                onClick={() => select(i)}
                className={OPTION}
              >
                <span className="flex items-center">
                  <span
                    className={cn(
                      "size-2.5 rounded-full border-2 border-text-muted",
                      i === index && "border-accent-fg bg-accent-fg",
                    )}
                  />
                  <span
                    title={`Observed from ${format(FULL, step.takenAt)} to ${format(FULL, step.lastSeenAt)}`}
                    className={cn("h-1 rounded-r-full bg-text-muted", i === index && "bg-accent-fg")}
                    style={{ width: BAR_MIN + (longest > 0 ? (BAR_EXTRA * span(step)) / longest : 0) }}
                  />
                </span>
                <span className="font-mono whitespace-nowrap tabular-nums">{format(SHORT, step.takenAt)}</span>
              </div>
            </Fragment>
          ))}
          <span aria-hidden className="mt-[5px] w-4 shrink-0 border-t border-border-control" />
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: the listbox handles the keys for its options. */}
          <div
            id={optionId(liveIndex)}
            role="option"
            aria-selected={index === liveIndex}
            tabIndex={index === liveIndex ? 0 : -1}
            aria-label="Live: the current schema"
            data-testid="timeline-live"
            onClick={() => select(liveIndex)}
            className={OPTION}
          >
            <span
              className={cn(
                "size-2.5 rounded-full border-2 border-text-muted",
                index === liveIndex && "border-accent-fg bg-accent-fg",
              )}
            />
            <span>Live</span>
          </div>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Next snapshot"
          title="Next snapshot"
          disabled={index === liveIndex}
          onClick={() => select(index + 1)}
        >
          <ChevronRight aria-hidden />
        </Button>
        {pending !== null && <LoaderCircle aria-hidden className="size-3.5 shrink-0 animate-spin text-text-muted" />}
      </div>
      {error && (
        <p role="alert" className="px-1 text-status-danger-fg">
          Could not load the snapshot: {error}
        </p>
      )}
    </section>
  );
}

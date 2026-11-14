import { Eraser, Link2, SquareMinus, SquarePen, SquarePlus } from "lucide-react";
import {
  clearMigrationOverlay,
  DIFF_CHANGE_META,
  type DiffChange,
  type DiffImpact,
  partitionDiffRelations,
  useCanvasStore,
} from "@/entities/schema";
import { clearComparison, DRIFT_SOURCE } from "@/features/compare-schema";
import { showLive, TIMELINE_SOURCE, useTimelineStore } from "@/features/schema-timeline";
import { announce } from "@/shared/lib/announcer";
import { formatCompact } from "@/shared/lib/format-number";
import { Button } from "@/shared/ui/button";

const ICON = { added: SquarePlus, altered: SquarePen, dropped: SquareMinus } as const;
const TONE = {
  added: "text-status-success-fg",
  altered: "text-status-warn-fg",
  dropped: "text-status-danger-fg",
} as const;

const KIND_LABEL: Record<DiffImpact["kind"], string> = {
  fk_child: "foreign key",
  view: "view",
  matview: "materialized view",
  function: "function",
  trigger: "trigger",
  policy: "policy",
  index: "index",
  generated_column: "generated column",
  query: "query",
};

const MAX_IMPACT = 12;

function ChangeKey({ change, count }: { change: DiffChange; count: number }) {
  const Icon = ICON[change];
  return (
    <li className={`flex items-center gap-1 font-semibold ${TONE[change]}`}>
      <Icon aria-hidden className="size-3.5" />
      {DIFF_CHANGE_META[change].label}
      <span className="font-mono font-normal text-text-muted tabular-nums">{count}</span>
    </li>
  );
}

/**
 * Legend and impact list for a migration diff on the canvas. The colours are
 * explained with icon + text (UX rule 7); selecting a changed table lists its
 * dependents, including ones that are not tables (functions, indexes, queries).
 */
export function MigrationLegend() {
  const diff = useCanvasStore((s) => s.diff);
  const graph = useCanvasStore((s) => s.graph);
  const root = useCanvasStore((s) => s.derived.impactRoot);
  const selected = useCanvasStore((s) => s.selectedId);
  const step = useTimelineStore((s) => s.shown);
  if (!diff) return null;
  // A schema comparison reuses the overlay; it has no dependents to explain.
  const comparison = diff.sourceId === DRIFT_SOURCE;
  // So does a timeline step, compared with the step before it.
  const timeline = diff.sourceId === TIMELINE_SOURCE;
  const first = timeline && !step?.changes;

  const counts = { added: 0, altered: 0, dropped: 0 };
  for (const change of Object.values(diff.relations)) counts[change] += 1;
  const { missing } = partitionDiffRelations(diff, (k) => Boolean(graph?.byKey.has(k)));
  const impact = root ? (diff.impact[root] ?? []) : [];
  const notCompared = timeline ? (step?.changes?.notComparedSchemas ?? []) : [];

  return (
    <section
      aria-label={timeline ? "Timeline overlay" : comparison ? "Comparison overlay" : "Migration overlay"}
      data-testid="migration-legend"
      className="absolute top-3 left-3 z-10 flex max-h-[60%] w-72 flex-col gap-2 overflow-y-auto rounded-md border border-border-default bg-surface-raised p-2 text-xs shadow-sm"
    >
      <h3 className="font-semibold break-all text-text-primary">
        {timeline ? "Snapshot from " : comparison ? "Compared " : "Migration: "}
        <span className="font-mono">{diff.label}</span>
      </h3>
      <Button
        variant="outline"
        size="xs"
        className="self-start"
        onClick={() => {
          if (timeline) showLive();
          else if (comparison) clearComparison();
          else clearMigrationOverlay();
          announce(
            timeline
              ? "Showing the live schema."
              : comparison
                ? "Cleared the comparison."
                : "Cleared the migration overlay.",
          );
        }}
      >
        <Eraser aria-hidden />
        {timeline ? "Back to live" : comparison ? "Clear comparison" : "Clear migration overlay"}
      </Button>
      {first ? (
        <p className="text-text-muted">The first observation. There is no earlier snapshot to compare it with.</p>
      ) : (
        <ul aria-label="Changed relations" className="flex flex-wrap gap-x-3 gap-y-1">
          {(["added", "altered", "dropped"] as const).map((c) => (
            <ChangeKey key={c} change={c} count={counts[c]} />
          ))}
        </ul>
      )}
      {!comparison && !timeline && (
        <ul aria-label="Dependency certainty" className="flex flex-col gap-0.5 text-text-muted">
          <li className="flex items-center gap-1">
            <span className="flex items-center gap-0.5 rounded-xs border border-accent-fg px-1 text-2xs font-semibold text-accent-fg">
              <Link2 aria-hidden className="size-3" />
              catalog
            </span>
            depends on it, recorded in the catalog
          </li>
          <li className="flex items-center gap-1">
            <span className="flex items-center gap-0.5 rounded-xs border border-dashed border-accent-fg px-1 text-2xs font-semibold text-accent-fg">
              <Link2 aria-hidden className="size-3" />
              heuristic
            </span>
            matched by name, may be wrong
          </li>
        </ul>
      )}
      {missing.length > 0 &&
        (timeline ? (
          <section aria-label="Removed at this step" data-testid="timeline-removed">
            <h4 className="font-semibold text-text-primary">Removed at this step ({missing.length})</h4>
            <ul className="flex flex-col gap-0.5">
              {missing.map((k) => (
                <li key={k} className="font-mono break-all text-text-primary">
                  {k}
                </li>
              ))}
            </ul>
          </section>
        ) : (
          <p className="text-text-muted">
            Not on the canvas:{" "}
            {missing.map((k, i) => (
              <span key={k}>
                {i > 0 && ", "}
                <span className="font-mono text-text-primary">{k}</span> ({diff.relations[k]})
              </span>
            ))}
          </p>
        ))}
      {notCompared.length > 0 && (
        <p data-testid="timeline-not-compared" className="text-text-muted">
          Not compared, read in only one of the two snapshots: schema{notCompared.length === 1 ? "" : "s"}{" "}
          <span className="font-mono text-text-primary">{notCompared.join(", ")}</span>
        </p>
      )}
      {timeline ? (
        !first && (
          <p className="text-text-muted">
            Changes since the previous snapshot. Nobody read the schema between the two, so when each change happened is
            not known.
          </p>
        )
      ) : comparison ? (
        <p className="text-text-muted">Every difference, with what changed, is in the Drift tab of the results dock.</p>
      ) : root ? (
        <section aria-labelledby="impact-heading" data-testid="impact-list" className="flex flex-col gap-1">
          <h4 id="impact-heading" className="font-semibold text-text-primary">
            Depends on <span className="font-mono">{root}</span> ({impact.length})
          </h4>
          {impact.length === 0 ? (
            <p className="text-text-muted">Nothing found that depends on it.</p>
          ) : (
            <ul className="flex flex-col gap-0.5">
              {impact.slice(0, MAX_IMPACT).map((item) => (
                <li
                  key={`${item.via}|${item.kind}|${item.name}`}
                  data-testid="impact-item"
                  data-certainty={item.certainty}
                  className="flex items-baseline gap-1"
                >
                  <span className="shrink-0 text-text-muted">{KIND_LABEL[item.kind] ?? item.kind}</span>
                  <span className="min-w-0 truncate font-mono text-text-primary" title={item.name}>
                    {item.name}
                  </span>
                  <span className="ml-auto shrink-0 text-2xs text-text-muted">
                    {item.calls ? `${formatCompact(item.calls)} calls · ` : ""}
                    {item.certainty}
                    {item.via !== root ? ` · via ${item.via.slice(root.length + 1)}` : ""}
                  </span>
                </li>
              ))}
              {impact.length > MAX_IMPACT && <li className="text-text-muted">and {impact.length - MAX_IMPACT} more</li>}
            </ul>
          )}
        </section>
      ) : (
        <p className="text-text-muted">
          {selected ? `${selected} is not changed by this migration. ` : ""}Select a changed table to highlight what
          depends on it.
        </p>
      )}
    </section>
  );
}

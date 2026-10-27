import { cn } from "cn";
import { ArrowDown, ArrowUp, ArrowUpDown, FileWarning, LoaderCircle } from "lucide-react";
import { useMemo } from "react";
import {
  type MigrationReview,
  nextRiskSort,
  RatingBadge,
  type RiskSortKey,
  ratingMeta,
  reviewsInScope,
  riskRows,
  sortRiskRows,
  useReviewStore,
} from "@/entities/migration-finding";
import { docTabsInScope, useSqlTabs } from "@/entities/query";
import { describeReviewError, openAnalyzeFiles, openStatement } from "@/features/analyze-migration";
import { Button } from "@/shared/ui/button";
import { EmptyState } from "@/shared/ui/empty-state";
import { StatusIcon } from "@/shared/ui/status-icon";

/** The review the Risk tab shows: the active migration tab's, else the last one analyzed or viewed in this scope. */
function useRiskReview(): MigrationReview | undefined {
  const active = useSqlTabs((s) => s.active);
  const scope = useSqlTabs((s) => s.scope);
  const docTabs = useSqlTabs((s) => s.docTabs);
  const reviews = useReviewStore((s) => s.reviews);
  const current = useReviewStore((s) => s.current);
  const open = new Set(docTabsInScope(docTabs, scope).map((t) => t.id));
  if (open.has(active) && reviews[active]) return reviews[active];
  if (current && open.has(current) && reviews[current]) return reviews[current];
  return reviewsInScope(reviews, scope)
    .filter((r) => open.has(r.id))
    .at(-1);
}

const COLUMNS: { key: RiskSortKey; label: string; className: string }[] = [
  { key: "rating", label: "Rating", className: "w-24" },
  { key: "rule", label: "Rule", className: "w-44" },
  { key: "location", label: "File:line", className: "w-40" },
  { key: "relation", label: "Relation", className: "w-32" },
  { key: "message", label: "Message", className: "" },
];

function SortIcon({ active, dir }: { active: boolean; dir: "asc" | "desc" }) {
  if (!active) return <ArrowUpDown aria-hidden className="size-3 text-text-muted" />;
  return dir === "asc" ? <ArrowUp aria-hidden className="size-3" /> : <ArrowDown aria-hidden className="size-3" />;
}

export function RiskTab() {
  const review = useRiskReview();
  const sort = useReviewStore((s) => s.riskSort);
  const model = review?.model ?? null;
  const rows = useMemo(() => (model ? sortRiskRows(riskRows(model), sort) : []), [model, sort]);

  if (!review) {
    return (
      <EmptyState
        icon={FileWarning}
        title="No migration analyzed"
        description="Drop .sql migration files anywhere on the workbench. Their findings appear here, critical first, with the file and line of each."
        action={{ label: "Analyze migration files…", onClick: openAnalyzeFiles }}
      />
    );
  }
  if (review.status === "running" && !model) {
    return (
      <p role="status" className="flex h-full items-center justify-center gap-2 text-sm text-text-muted">
        <LoaderCircle aria-hidden className="size-4 animate-spin" />
        Analyzing {review.title}…
      </p>
    );
  }
  if (review.error || !model) {
    const copy = review.error ? describeReviewError(review.error) : null;
    return (
      <div role="alert" className="flex h-full flex-col items-center justify-center gap-2 p-4 text-center text-sm">
        <p className="flex items-center gap-1.5 font-semibold text-status-danger-fg">
          <StatusIcon tone="danger" />
          {review.title}: {copy?.title ?? "no report"}
        </p>
        <Button variant="outline" size="sm" onClick={() => useSqlTabs.getState().setActive(review.id)}>
          Open the migration tab
        </Button>
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border-default px-2 py-1 text-xs text-text-muted">
        <span className="font-mono text-text-primary">{review.title}</span>
        <span>
          {rows.length} finding{rows.length === 1 ? "" : "s"} · overall
        </span>
        <RatingBadge rating={model.rating} />
      </div>
      {rows.length === 0 ? (
        <EmptyState
          icon={FileWarning}
          title="No findings"
          description={`Every statement in ${review.title} is rated Low with nothing to fix.`}
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <table data-testid="risk-table" className="w-full table-fixed border-collapse text-xs">
            <caption className="sr-only">
              Migration findings for {review.title}. Select a row to open its statement.
            </caption>
            <thead className="sticky top-0 z-10 bg-surface-subtle">
              <tr>
                {COLUMNS.map((c) => {
                  const active = sort.key === c.key;
                  return (
                    <th
                      key={c.key}
                      scope="col"
                      aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}
                      className={cn("h-7 border-b border-border-default p-0 text-left font-medium", c.className)}
                    >
                      <button
                        type="button"
                        data-sort={c.key}
                        onClick={() => useReviewStore.getState().setRiskSort(nextRiskSort(sort, c.key))}
                        className="flex h-7 w-full items-center gap-1 px-2 text-text-primary outline-none hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus focus-visible:ring-inset"
                      >
                        {c.label}
                        <SortIcon active={active} dir={sort.dir} />
                      </button>
                    </th>
                  );
                })}
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr
                  key={r.id}
                  data-testid="risk-row"
                  data-rating={r.rating}
                  data-rule={r.ruleId}
                  className="border-b border-border-default hover:bg-surface-hover"
                >
                  <td className="px-2 py-1 align-top">
                    <RatingBadge rating={r.rating} />
                  </td>
                  <td className="truncate px-2 py-1 align-top font-mono" title={r.ruleId}>
                    {r.ruleId}
                  </td>
                  <td className="px-2 py-1 align-top">
                    <button
                      type="button"
                      aria-label={`Open ${r.file} line ${r.line}: ${ratingMeta(r.rating).label}, ${r.ruleId}`}
                      onClick={() => openStatement(review.id, r.statementIndex)}
                      className="max-w-full truncate text-left font-mono text-text-link hover:underline"
                    >
                      {`${r.file}:${r.line}`}
                    </button>
                  </td>
                  <td className="truncate px-2 py-1 align-top font-mono" title={r.relation}>
                    {r.relation}
                  </td>
                  <td className="px-2 py-1 align-top break-words text-text-primary">{r.message}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

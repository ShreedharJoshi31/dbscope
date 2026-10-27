import { Check, Crosshair, Download, Eraser, FileText } from "lucide-react";
import { useState } from "react";
import {
  baseWords,
  type MigrationReview,
  RATINGS,
  RatingBadge,
  type ReviewModel,
  ratingMeta,
  reviewMarkdown,
} from "@/entities/migration-finding";
import { clearMigrationOverlay, partitionDiffRelations, useCanvasStore } from "@/entities/schema";
import { ReviewOptionsFields, reanalyze, showReviewOnCanvas } from "@/features/analyze-migration";
import { announce } from "@/shared/lib/announcer";
import { copyText, downloadText } from "@/shared/lib/clipboard";
import { Button } from "@/shared/ui/button";
import { StatusIcon } from "@/shared/ui/status-icon";

function reportFileName(review: MigrationReview): string {
  const base = review.title.endsWith(".sql") ? review.title.slice(0, -4) : "migrations";
  return `${base.replace(/[^\w.-]+/g, "-")}-dbscope-report.json`;
}

function plural(n: number, one: string): string {
  return `${n} ${one}${n === 1 ? "" : "s"}`;
}

function CopyMarkdown({ review, model }: { review: MigrationReview; model: ReviewModel }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={() =>
        void copyText(reviewMarkdown(model, { target: review.targetLabel })).then((ok) => {
          announce(ok ? "Copied the review as Markdown." : "The browser refused clipboard access.");
          if (ok) {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }
        })
      }
    >
      {copied ? <Check aria-hidden /> : <FileText aria-hidden />}
      {copied ? "Copied" : "Copy as Markdown"}
    </Button>
  );
}

/** Changed relations; new ones are listed by name because they are not on the canvas (no nodes are added). */
function SchemaChanges({ model }: { model: ReviewModel }) {
  const graph = useCanvasStore((s) => s.graph);
  const { diff } = model;
  const relations = Object.fromEntries([
    ...diff.altered.map((k) => [k, "altered"] as const),
    ...diff.added.map((k) => [k, "added"] as const),
    ...diff.dropped.map((k) => [k, "dropped"] as const),
  ]);
  const { missing } = partitionDiffRelations({ relations }, (k) => Boolean(graph?.byKey.has(k)));
  const parts = [
    diff.added.length > 0 && `${diff.added.length} added`,
    diff.altered.length > 0 && `${diff.altered.length} altered`,
    diff.dropped.length > 0 && `${diff.dropped.length} dropped`,
    diff.columns.length > 0 && plural(diff.columns.length, "column change"),
  ].filter(Boolean);
  if (parts.length === 0) return <span className="text-xs text-text-muted">No schema changes</span>;
  return (
    <span
      data-testid="schema-changes"
      className="flex min-w-0 flex-wrap items-center gap-x-2 text-xs text-text-primary"
    >
      <span>Schema changes: {parts.join(", ")}</span>
      {graph && missing.length > 0 && (
        <span data-testid="new-relations" className="text-text-muted">
          · Not on the canvas (new): <span className="font-mono text-text-primary">{missing.join(", ")}</span>
        </span>
      )}
    </span>
  );
}

interface ReviewHeaderProps {
  review: MigrationReview;
  model: ReviewModel;
  onGoTo: (index: number) => void;
}

export function ReviewHeader({ review, model, onGoTo }: ReviewHeaderProps) {
  const overlayHere = useCanvasStore((s) => s.diff?.sourceId === review.id);
  const base = baseWords(model.report.base);
  const files = model.files.length;
  return (
    <header className="flex shrink-0 flex-col gap-2 border-b border-border-default bg-surface-subtle px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-semibold text-text-primary">Overall</span>
        <span data-testid="overall-rating">
          <RatingBadge rating={model.rating} size="md" />
        </span>
        <ul aria-label="Statements per rating" className="flex items-center gap-1">
          {RATINGS.map((r) => (
            <li key={r} data-testid={`count-${r}`}>
              <RatingBadge rating={r} suffix={model.counts[r]} />
              <span className="sr-only">
                {" "}
                {model.counts[r]} {ratingMeta(r).label} statements
              </span>
            </li>
          ))}
        </ul>
        <span className="text-xs text-text-muted">
          {plural(model.statements.length, "statement")} in {plural(files, "file")} ·{" "}
          <span data-testid="pg-version" className="text-text-primary">
            {model.pgLabel} rules
          </span>{" "}
          · against {review.targetLabel}
          {base && base !== review.targetLabel ? ` (${base})` : ""}
        </span>
        <div className="ml-auto flex flex-wrap items-center gap-1">
          <Button variant="outline" size="sm" onClick={() => showReviewOnCanvas(review.id)}>
            <Crosshair aria-hidden />
            Show on canvas
          </Button>
          {overlayHere && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                clearMigrationOverlay();
                announce("Cleared the migration overlay.");
              }}
            >
              <Eraser aria-hidden />
              Clear migration overlay
            </Button>
          )}
          <CopyMarkdown review={review} model={model} />
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              downloadText(reportFileName(review), `${JSON.stringify(model.report, null, 2)}\n`, "application/json");
              announce("Downloaded the JSON report.");
            }}
          >
            <Download aria-hidden />
            Download JSON
          </Button>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        <ReviewOptionsFields compact value={review.options} onChange={(next) => reanalyze(review.id, next)} />
        <SchemaChanges model={model} />
      </div>
      {model.unanalyzable.length > 0 && (
        <section
          aria-labelledby={`${review.id}-unanalyzable`}
          data-testid="unanalyzable"
          className="flex flex-col gap-1 rounded-md border border-status-warn-fg bg-surface-canvas p-2"
        >
          <h3
            id={`${review.id}-unanalyzable`}
            className="flex items-center gap-1.5 text-xs font-semibold text-text-primary"
          >
            <StatusIcon tone="warn" className="size-3.5" />
            Not analyzed ({model.unanalyzable.length})
          </h3>
          <ul className="flex flex-col gap-0.5">
            {model.unanalyzable.map((u) => (
              <li key={`${u.file}:${u.line}:${u.message}`} className="flex items-start gap-2 text-xs">
                {u.statementIndex !== null ? (
                  <button
                    type="button"
                    className="shrink-0 font-mono text-text-link hover:underline"
                    onClick={() => u.statementIndex !== null && onGoTo(u.statementIndex)}
                  >
                    {`${u.file}:${u.line}`}
                  </button>
                ) : (
                  <span className="shrink-0 font-mono text-text-primary">{`${u.file}:${u.line}`}</span>
                )}
                <span className="min-w-0 text-text-muted">{u.explanation}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      {model.warnings.length > 0 && (
        <ul aria-label="Notes about the analysis" className="flex flex-col gap-0.5">
          {model.warnings.map((w) => (
            <li key={w} className="flex items-start gap-1.5 text-xs text-text-muted">
              <StatusIcon tone="info" className="mt-px size-3.5" />
              {w}
            </li>
          ))}
        </ul>
      )}
    </header>
  );
}

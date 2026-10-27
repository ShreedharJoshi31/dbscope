import { FileSearch } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useActiveConnection } from "@/entities/connection";
import { useReviewStore } from "@/entities/migration-finding";
import { useRunStore } from "@/entities/query";
import { openAnalyzeFiles } from "@/features/analyze-migration";
import { usePanelStore } from "@/features/toggle-panel";
import { formatCombo, SHORTCUTS } from "@/shared/config/shortcuts";
import { EmptyState } from "@/shared/ui/empty-state";
import type { FileCodeHandle } from "@/widgets/migration-review/ui/file-code";
import { FileList, initiallyExpanded } from "@/widgets/migration-review/ui/file-list";
import { ReviewErrorView, ReviewRunning } from "@/widgets/migration-review/ui/review-error";
import { ReviewHeader } from "@/widgets/migration-review/ui/review-header";
import { StatementDetail } from "@/widgets/migration-review/ui/statement-detail";

/** The migration tab in the centre strip: files with statement markers on the left, the selected statement on the right. */
export function MigrationReviewTab({ id }: { id: string }) {
  const review = useReviewStore((s) => s.reviews[id]);
  const { profile } = useActiveConnection();
  const model = review?.model ?? null;
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set());
  const handles = useRef(new Map<number, FileCodeHandle>());
  const pending = useRef<{ index: number; marker: boolean } | null>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const shownModel = useRef<unknown>(null);

  // A new report (first result or a re-run with other options): expand the risky files, show the Risk list.
  useEffect(() => {
    if (!model || shownModel.current === model) return;
    const first = shownModel.current === null;
    shownModel.current = model;
    setExpanded(initiallyExpanded(model));
    if (first) {
      useRunStore.getState().setDockTab("risk");
      usePanelStore.getState().setOpen("dock", true);
    }
  }, [model]);

  const select = useCallback((index: number) => useReviewStore.getState().select(id, index), [id]);
  const openDetail = useCallback((index: number) => useReviewStore.getState().select(id, index, "detail"), [id]);

  const registerHandle = useCallback(
    (fileIndex: number, handle: FileCodeHandle | null) => {
      if (!handle) {
        handles.current.delete(fileIndex);
        return;
      }
      handles.current.set(fileIndex, handle);
      const want = pending.current;
      const s = want ? useReviewStore.getState().reviews[id]?.model?.statements[want.index] : undefined;
      if (want && s?.fileIndex === fileIndex) {
        pending.current = null;
        handle.reveal(want.index, want.marker);
      }
    },
    [id],
  );

  const reveal = review?.reveal;
  useEffect(() => {
    if (!reveal || !model) return;
    const s = model.statements[reveal.index];
    if (!s) return;
    if (reveal.focus === "detail") {
      requestAnimationFrame(() => headingRef.current?.focus());
    }
    const marker = reveal.focus === "marker";
    const handle = handles.current.get(s.fileIndex);
    if (handle) {
      handle.reveal(s.index, marker);
      return;
    }
    // The file is collapsed or its editor is still loading: reveal once it registers.
    pending.current = { index: s.index, marker };
    setExpanded((prev) => (prev.has(s.fileIndex) ? prev : new Set([...prev, s.fileIndex])));
  }, [reveal, model]);

  if (!review) {
    return (
      <EmptyState
        icon={FileSearch}
        title="This migration review is gone"
        description="Reports are kept in memory only; drop the files again to analyze them."
        action={{ label: "Analyze migration files…", onClick: openAnalyzeFiles }}
      />
    );
  }
  if (review.status === "running" && !model) return <ReviewRunning review={review} />;
  if (review.error) {
    return <ReviewErrorView review={review} error={review.error} statementTimeoutMs={profile?.statementTimeoutMs} />;
  }
  if (!model) return <ReviewRunning review={review} />;

  const statement = model.statements[review.selected];
  const unanalyzable = model.unanalyzable.find((u) => u.statementIndex === review.selected);

  return (
    <div data-testid="migration-review" data-review={id} className="flex h-full min-h-0 flex-col">
      <ReviewHeader
        review={review}
        model={model}
        onGoTo={(index) => useReviewStore.getState().select(id, index, "marker")}
      />
      {review.status === "running" && (
        <p role="status" className="shrink-0 border-b border-border-default px-3 py-1 text-xs text-text-muted">
          Analyzing again with the new options…
        </p>
      )}
      {model.statements.length === 0 ? (
        <EmptyState
          icon={FileSearch}
          title="No SQL statements in these files"
          description="The files hold only comments, psql meta-commands or nothing at all. Drop the migration files that change the schema."
          action={{ label: "Analyze migration files…", onClick: openAnalyzeFiles }}
        />
      ) : (
        <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_minmax(320px,42%)]">
          <section
            aria-label={`Migration files. ${formatCombo(SHORTCUTS.migrationNext.combo)} and ${formatCombo(SHORTCUTS.migrationPrevious.combo)} move between statements.`}
            className="min-h-0 overflow-y-auto border-r border-border-default"
          >
            <FileList
              model={model}
              selected={review.selected}
              expanded={expanded}
              onToggle={(i) =>
                setExpanded((prev) => {
                  const next = new Set(prev);
                  if (next.has(i)) next.delete(i);
                  else next.add(i);
                  return next;
                })
              }
              onSelect={select}
              onOpenDetail={openDetail}
              registerHandle={registerHandle}
            />
          </section>
          {statement ? (
            <StatementDetail
              review={review}
              statement={statement}
              total={model.statements.length}
              unanalyzable={unanalyzable}
              headingRef={headingRef}
            />
          ) : (
            <div />
          )}
        </div>
      )}
    </div>
  );
}

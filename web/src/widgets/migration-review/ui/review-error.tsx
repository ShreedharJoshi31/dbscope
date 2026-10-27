import { useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, OctagonX } from "lucide-react";
import { useEffect, useState } from "react";
import type { MigrationReview, ReviewError } from "@/entities/migration-finding";
import { openSampleWorkspace } from "@/entities/schema";
import { useConnectionDialog } from "@/features/add-connection";
import { cancelAnalysis, describeReviewError, openAnalyzeFiles, reanalyze } from "@/features/analyze-migration";
import { useUnlockPrompt } from "@/features/unlock-secrets";
import { queryKeys } from "@/shared/api";
import { Button } from "@/shared/ui/button";

export function ReviewRunning({ review }: { review: MigrationReview }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const seconds = Math.max(0, Math.floor((now - review.startedAt) / 1000));
  const files = review.sources.filter((s) => s.name.toLowerCase().endsWith(".sql")).length;
  return (
    <div role="status" className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
      <p className="flex items-center gap-2 text-sm text-text-primary">
        <LoaderCircle aria-hidden className="size-4 animate-spin text-text-muted" />
        Analyzing {files} file{files === 1 ? "" : "s"} against {review.targetLabel}…
      </p>
      <p className="font-mono text-xs text-text-muted tabular-nums">{seconds} s</p>
      <Button variant="outline" size="sm" onClick={() => cancelAnalysis(review.id)}>
        Cancel
      </Button>
    </div>
  );
}

interface ReviewErrorViewProps {
  review: MigrationReview;
  error: ReviewError;
  statementTimeoutMs: number | undefined;
}

/** Stays on screen until the user acts; never only a vanishing toast (UX rule 10). */
export function ReviewErrorView({ review, error, statementTimeoutMs }: ReviewErrorViewProps) {
  const copy = describeReviewError(error, statementTimeoutMs);
  const client = useQueryClient();
  const openCreate = useConnectionDialog((s) => s.openCreate);
  const status = error.kind === "api" ? error.error.status : undefined;
  const code = error.kind === "api" ? error.error.code : error.problem.code;

  const buttons = copy.actions.map((action, i) => {
    const variant = i === 0 ? "outline" : "link";
    switch (action) {
      case "retry":
        return (
          <Button key={action} variant={variant} size="sm" onClick={() => reanalyze(review.id)}>
            Try again
          </Button>
        );
      case "unlock":
        return (
          <Button
            key={action}
            variant={variant}
            size="sm"
            onClick={() => {
              useUnlockPrompt.getState().reopen();
              void client.invalidateQueries({ queryKey: queryKeys.secretsStatus });
            }}
          >
            Unlock the vault
          </Button>
        );
      case "pick":
        return (
          <Button key={action} variant={variant} size="sm" onClick={openAnalyzeFiles}>
            Choose other files…
          </Button>
        );
      case "connect":
        return (
          <span key={action} className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={openCreate}>
              Add a connection
            </Button>
            <Button variant="link" size="sm" onClick={openSampleWorkspace}>
              Open the sample schema
            </Button>
          </span>
        );
    }
    return null;
  });

  return (
    <div
      role="alert"
      data-testid="review-error"
      data-code={code}
      className="flex h-full min-h-0 items-center justify-center overflow-auto p-6"
    >
      <div className="flex max-w-md flex-col gap-3">
        <p className="flex items-start gap-2 text-sm font-semibold text-status-danger-fg">
          <OctagonX aria-hidden className="mt-0.5 size-4 shrink-0" />
          {copy.title}
        </p>
        {status !== undefined && status > 0 && (
          <p className="font-mono text-2xs text-text-muted">
            HTTP {status} · {code}
          </p>
        )}
        {copy.message && <p className="font-mono text-xs break-words text-text-primary">{copy.message}</p>}
        <p className="text-xs text-text-muted">{copy.hint}</p>
        {copy.details.length > 0 && (
          <ul className="flex flex-col font-mono text-2xs text-text-primary">
            {copy.details.map((d) => (
              <li key={d} className="truncate">
                {d}
              </li>
            ))}
          </ul>
        )}
        <div className="flex flex-wrap items-center gap-2">{buttons}</div>
      </div>
    </div>
  );
}

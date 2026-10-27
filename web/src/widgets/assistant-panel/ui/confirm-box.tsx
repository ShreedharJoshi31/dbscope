import { ShieldAlert, TriangleAlert } from "lucide-react";
import type { Turn } from "@/entities/assistant-turn";
import { costLimit, useActiveConnection } from "@/entities/connection";
import { formatCompact, formatGrouped } from "@/shared/lib/format-number";
import { Button } from "@/shared/ui/button";

interface ConfirmBoxProps {
  turn: Turn;
  /** "Anthropic (claude-opus-5-5)" or the endpoint: where rows would go. */
  destination: string;
  onConfirm: () => void;
  onDecline: () => void;
}

/** The agent stopped before running: over the cost limit, or rows would leave a prod-replica (UX rule 4). */
export function ConfirmBox({ turn, destination, onConfirm, onDecline }: ConfirmBoxProps) {
  const { profile } = useActiveConnection();
  const c = turn.confirmation;
  if (!c) return null;
  const name = profile?.name ?? "this connection";
  return (
    <section
      data-section="confirm"
      aria-label="Confirmation needed"
      className="flex flex-col gap-2 rounded-md border border-status-warn-fg bg-surface-canvas p-2 text-sm"
    >
      {c.reason === "cost" ? (
        <p className="flex items-start gap-1.5">
          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-status-warn-fg" />
          <span>
            <span className="font-medium">This query looks expensive.</span> EXPLAIN estimates a cost of{" "}
            <span className="font-mono">{formatGrouped(Math.round(c.estimatedCost))}</span> (about{" "}
            <span className="font-mono">{formatCompact(c.estimatedRows)}</span> rows)
            {profile ? (
              <>
                , over the limit of <span className="font-mono">{formatGrouped(costLimit(profile))}</span> for {name}
              </>
            ) : null}
            . Nothing has run yet.
          </span>
        </p>
      ) : (
        <p className="flex items-start gap-1.5">
          <ShieldAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-env-prod-fg" />
          <span>
            <span className="font-medium">Send sample rows from this prod-replica connection to {destination}?</span>{" "}
            {name} summarizes results from up to {profile?.maxRowsToLLM ?? 50} rows. Production data would leave this
            machine. You confirm once per dbscope session.
          </span>
        </p>
      )}
      <div className="flex gap-1">
        <Button size="sm" onClick={onConfirm}>
          {c.reason === "cost" ? "Run anyway" : "Send rows"}
        </Button>
        <Button size="sm" variant="outline" onClick={onDecline}>
          {c.reason === "cost" ? "Don't run" : "Not now"}
        </Button>
      </div>
    </section>
  );
}

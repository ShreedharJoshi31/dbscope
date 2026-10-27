import { Check, X } from "lucide-react";
import type { FkEdge } from "@/entities/schema";
import { useInferredFkDecision } from "@/features/review-inferred-fk/model/use-inferred-fk-decision";
import { Button } from "@/shared/ui/button";

export function confidenceLabel(confidence: number | undefined): string {
  return confidence === undefined ? "unknown" : `${Math.round(confidence * 100)}%`;
}

export function pairsLabel(edge: FkEdge): string {
  const cols = edge.pairs.map((p) => p.column).join(", ");
  const refs = edge.pairs.map((p) => p.refColumn).join(", ");
  return `${edge.child}(${cols}) → ${edge.parent}(${refs})`;
}

interface InferredFkActionsProps {
  edge: FkEdge;
  onDone?: () => void;
}

/** Explanation plus Accept and Reject for one inferred foreign key. */
export function InferredFkActions({ edge, onDone }: InferredFkActionsProps) {
  const { decide, pending, error } = useInferredFkDecision();
  const busy = pending === edge.id;

  if (edge.accepted) {
    return (
      <p className="flex items-center gap-1 text-xs text-text-muted">
        <Check aria-hidden className="size-3.5 text-status-success-fg" />
        Accepted: dbscope treats this as a foreign key.
      </p>
    );
  }

  const run = async (status: "accepted" | "rejected") => {
    if (await decide(edge, status)) onDone?.();
  };

  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-text-muted">
        Guessed from the column name; the database does not declare it. Confidence{" "}
        <span className="font-mono text-text-primary">{confidenceLabel(edge.confidence)}</span>.
      </p>
      <div className="flex gap-2">
        <Button size="sm" variant="outline" disabled={busy} onClick={() => run("accepted")}>
          <Check aria-hidden />
          Accept
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => run("rejected")}>
          <X aria-hidden />
          Reject
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-xs text-status-danger-fg">
          {error}
        </p>
      )}
    </div>
  );
}

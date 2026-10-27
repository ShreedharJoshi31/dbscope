import { cn } from "cn";
import { CircleCheck, CircleHelp } from "lucide-react";
import { useState } from "react";
import { useCanvasStore } from "@/entities/schema";
import { confidenceLabel, InferredFkActions, pairsLabel } from "@/features/review-inferred-fk";
import { Popover, PopoverContent, PopoverDescription, PopoverTitle, PopoverTrigger } from "@/shared/ui/popover";

interface InferredEdgeLabelProps {
  fkId: string;
  x: number;
  y: number;
  dim: boolean;
}

/**
 * Badge in the middle of an inferred relationship. "FK? 80%" for a guess
 * (hover shows the confidence, click opens Accept/Reject); "accepted" once the
 * user confirmed it. Text and icon carry the state, colour only repeats it.
 */
export function InferredEdgeLabel({ fkId, x, y, dim }: InferredEdgeLabelProps) {
  const edge = useCanvasStore((s) => s.graph?.edgeById.get(fkId));
  const [open, setOpen] = useState(false);
  if (!edge) return null;

  const accepted = edge.accepted;
  const Icon = accepted ? CircleCheck : CircleHelp;
  const text = accepted ? "accepted" : `FK? ${confidenceLabel(edge.confidence)}`;
  const title = accepted
    ? `Accepted inferred foreign key ${pairsLabel(edge)}`
    : `Inferred foreign key, confidence ${confidenceLabel(edge.confidence)}: ${pairsLabel(edge)}. Click to accept or reject.`;

  return (
    <div
      className={cn("nodrag nopan pointer-events-auto absolute", dim && "opacity-25")}
      style={{ transform: `translate(-50%, -50%) translate(${x}px, ${y}px)` }}
    >
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          data-testid="inferred-fk-label"
          data-fk={edge.fk.name}
          title={title}
          aria-label={title}
          className={cn(
            "flex h-5 items-center gap-1 rounded-full border bg-surface-canvas px-1.5 font-mono text-2xs font-semibold",
            accepted ? "border-status-success-fg text-status-success-fg" : "border-status-warn-fg text-status-warn-fg",
          )}
          onMouseEnter={() => useCanvasStore.getState().setHoverEdge(fkId)}
          onMouseLeave={() => useCanvasStore.getState().setHoverEdge(null)}
        >
          <Icon aria-hidden className="size-3" />
          {text}
        </PopoverTrigger>
        <PopoverContent side="top" className="w-80">
          <PopoverTitle>{accepted ? "Accepted foreign key" : "Inferred foreign key"}</PopoverTitle>
          <PopoverDescription className="font-mono break-all">{pairsLabel(edge)}</PopoverDescription>
          <InferredFkActions edge={edge} onDone={() => setOpen(false)} />
        </PopoverContent>
      </Popover>
    </div>
  );
}

import { cn } from "cn";
import { Clock } from "lucide-react";
import { timeTravelLines } from "@/entities/schema/model/time-travel";
import type { TimeTravel } from "@/shared/api";

/** "as of" when the table can be read at a past time; muted and dashed when this user lacks the privilege. */
export function TimeTravelBadge({ timeTravel }: { timeTravel: TimeTravel }) {
  const lines = timeTravelLines(timeTravel);
  return (
    <span
      data-testid="time-travel-badge"
      data-kind={timeTravel.kind}
      data-available={timeTravel.available}
      title={lines.join("\n")}
      className={cn(
        "flex shrink-0 items-center gap-0.5 rounded-xs border px-1 text-2xs font-semibold",
        timeTravel.available
          ? "border-status-info-fg text-status-info-fg"
          : "border-dashed border-border-control text-text-muted",
      )}
    >
      <Clock aria-hidden className="size-3" />
      as of
      <span className="sr-only">. {lines.join(". ")}</span>
    </span>
  );
}

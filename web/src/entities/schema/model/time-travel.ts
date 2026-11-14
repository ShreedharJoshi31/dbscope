import type { TimeTravel, TimeTravelKind } from "@/shared/api";

export const TIME_TRAVEL_KIND_LABEL: Record<TimeTravelKind, string> = {
  flashback: "Flashback query",
  temporal: "Temporal table",
  history: "History table",
  "system-versioned": "System-versioned table",
};

/** One line saying how the relation can be read as of a past time, or what the user is missing. */
export function timeTravelSummary(tt: TimeTravel): string {
  if (tt.kind === "history") return tt.historyOf ? `History table of ${tt.historyOf}` : "History table";
  if (!tt.available) {
    if (tt.kind === "flashback") return "Time travel needs the FLASHBACK privilege";
    if (tt.kind === "temporal") return "Time travel needs SELECT on the history table";
    return "Time travel is not available to this user";
  }
  const label = TIME_TRAVEL_KIND_LABEL[tt.kind].toLowerCase();
  return `Time travel: ${label}${tt.retention ? ` (retention ${tt.retention})` : ""}`;
}

/** The tooltip lines of the time travel badge: the summary, then the note when there is one. */
export function timeTravelLines(tt: TimeTravel): string[] {
  return tt.note ? [timeTravelSummary(tt), tt.note] : [timeTravelSummary(tt)];
}

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TimeTravelBadge } from "@/entities/schema/ui/time-travel-badge";
import type { TimeTravel } from "@/shared/api";

describe("TimeTravelBadge", () => {
  it.each<[string, TimeTravel, string]>([
    [
      "a system-versioned table with its retention",
      { kind: "system-versioned", available: true, retention: "3 months" },
      "Time travel: system-versioned table (retention 3 months)",
    ],
    [
      "a temporal table with infinite retention",
      { kind: "temporal", available: true, retention: "infinite" },
      "Time travel: temporal table (retention infinite)",
    ],
    [
      "a history table with its temporal table",
      { kind: "history", available: true, historyOf: "dbo.acct" },
      "History table of dbo.acct",
    ],
    ["an Oracle table the user may flash back", { kind: "flashback", available: true }, "Time travel: flashback query"],
    [
      "a partitioned history trap",
      {
        kind: "system-versioned",
        available: true,
        note: "History is partitioned; dropped history partitions read as empty, not as an error",
      },
      "Time travel: system-versioned table\nHistory is partitioned; dropped history partitions read as empty, not as an error",
    ],
  ])("labels %s as 'as of' and explains it in the tooltip", (_, timeTravel, title) => {
    render(<TimeTravelBadge timeTravel={timeTravel} />);
    const badge = screen.getByTestId("time-travel-badge");
    expect(badge).toHaveAttribute("title", title);
    expect(badge).toHaveAttribute("data-kind", timeTravel.kind);
    expect(badge).toHaveAttribute("data-available", "true");
    expect(badge).toHaveTextContent(`as of. ${title.replace("\n", ". ")}`);
    expect(badge).not.toHaveClass("border-dashed");
    expect(badge.querySelector("svg")).not.toBeNull();
  });

  it.each<[string, TimeTravel, string]>([
    [
      "an Oracle table without FLASHBACK",
      {
        kind: "flashback",
        available: false,
        note: "Needs the FLASHBACK privilege on this table, or FLASHBACK ANY TABLE",
      },
      "Time travel needs the FLASHBACK privilege\nNeeds the FLASHBACK privilege on this table, or FLASHBACK ANY TABLE",
    ],
    [
      "a temporal table without SELECT on its history",
      {
        kind: "temporal",
        available: false,
        retention: "3 months",
        note: "Needs SELECT on the history table dbo.acct_history",
      },
      "Time travel needs SELECT on the history table\nNeeds SELECT on the history table dbo.acct_history",
    ],
  ])("is muted and names the missing privilege for %s", (_, timeTravel, title) => {
    render(<TimeTravelBadge timeTravel={timeTravel} />);
    const badge = screen.getByTestId("time-travel-badge");
    expect(badge).toHaveAttribute("title", title);
    expect(badge).toHaveAttribute("data-available", "false");
    expect(badge).toHaveClass("border-dashed", "text-text-muted");
    expect(screen.getByText(`. ${title.replace("\n", ". ")}`)).toHaveClass("sr-only");
  });
});

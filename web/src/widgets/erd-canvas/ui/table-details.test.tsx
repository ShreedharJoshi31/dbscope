import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { buildSchemaGraph, useCanvasStore } from "@/entities/schema";
import type { Model, Relation, TimeTravel } from "@/shared/api";
import { TableDetails } from "@/widgets/erd-canvas/ui/table-details";

function table(name: string, timeTravel?: TimeTravel): Relation {
  return {
    schema: "dbo",
    name,
    kind: "table",
    rowEstimate: 1,
    pages: 0,
    columns: [{ name: "id", type: "int", nullable: false }],
    ...(timeTravel && { timeTravel }),
  };
}

function open(relations: Relation[], id: string) {
  const model: Model = {
    serverVersion: 170000,
    schemas: ["dbo"],
    relations,
    enums: [],
    introspectedAt: "",
    durationMs: 0,
  };
  useCanvasStore.setState({ graph: buildSchemaGraph(model), selectedId: id, detailsOpen: true });
  render(<TableDetails onClose={() => {}} />);
}

/** The time travel definition list as "term: value" pairs. */
function rows(): string[] {
  const section = screen.getByRole("heading", { name: "Time travel" }).closest("section") as HTMLElement;
  return within(section)
    .getAllByRole("term")
    .map((dt) => `${dt.textContent}: ${dt.nextElementSibling?.textContent}`);
}

afterEach(() => {
  useCanvasStore.setState({ graph: null, selectedId: null, detailsOpen: false });
});

describe("TableDetails time travel", () => {
  it("shows the kind, availability, retention and the note", () => {
    open(
      [
        table("acct", {
          kind: "temporal",
          available: false,
          retention: "3 months",
          note: "Needs SELECT on the history table dbo.acct_history",
        }),
      ],
      "dbo.acct",
    );
    expect(screen.getByRole("heading", { name: "Time travel" })).toBeInTheDocument();
    expect(rows()).toEqual(["Kind: Temporal table", "Available: Not for this user", "Retention: 3 months"]);
    expect(screen.getByText("Needs SELECT on the history table dbo.acct_history")).toBeInTheDocument();
  });

  it("links a history table to its temporal table", () => {
    open(
      [
        table("acct", { kind: "temporal", available: true, retention: "infinite" }),
        table("acct_history", { kind: "history", available: true, historyOf: "dbo.acct" }),
      ],
      "dbo.acct_history",
    );
    expect(rows()).toEqual(["Kind: History table", "Available: Yes", "History of: dbo.acct"]);
    expect(screen.getByRole("button", { name: "dbo.acct" })).toBeInTheDocument();
  });

  it("has no time travel row for a table without it", () => {
    open([table("plain")], "dbo.plain");
    expect(screen.getByTestId("table-details")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Time travel" })).toBeNull();
  });
});

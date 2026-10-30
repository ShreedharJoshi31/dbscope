import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Blockers, durationWords, Pending, Usages } from "@/widgets/migration-review/ui/review-context";

describe("review context", () => {
  it("words durations in the unit that reads well", () => {
    expect([0.4, 45, 600, 7200].map(durationWords)).toEqual(["under a second", "45 s", "10 min", "2 h"]);
  });

  it("lists blocking sessions with their age and query", () => {
    render(
      <Blockers
        at="2026-10-31T09:30:00Z"
        blockers={[
          {
            relation: "public.orders",
            session: "4242",
            user: "app",
            state: "idle in transaction",
            ageSeconds: 600,
            query: "SELECT * FROM orders",
            lock: "AccessShareLock",
          },
        ]}
      />,
    );
    const section = screen.getByTestId("blockers");
    expect(section).toHaveTextContent("1 open session would block this migration");
    expect(section).toHaveTextContent("session 4242 (app), idle in transaction, open 10 min, holds AccessShareLock");
    expect(section).toHaveTextContent("SELECT * FROM orders");
  });

  it("renders nothing without blockers or usages", () => {
    const { container } = render(
      <>
        <Blockers blockers={[]} at={undefined} />
        <Usages usages={[]} />
      </>,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("says which files the history skipped and which changed", () => {
    render(
      <Pending
        pending={{
          tool: "flyway",
          table: "flyway_schema_history",
          applied: ["V1__a.sql", "V2__b.sql"],
          changed: ["V2__b.sql"],
        }}
      />,
    );
    expect(screen.getByTestId("pending")).toHaveTextContent(
      "2 applied files skipped per flyway's flyway_schema_history.1 changed since applied, checked again.",
    );
  });

  it("groups code usages by name", () => {
    render(
      <Usages
        usages={[
          { name: "customers.email", file: "app/models/customer.rb", line: 14, text: "validates :email" },
          { name: "customers.email", file: "web/src/api.ts", line: 3, text: "user.email" },
        ]}
      />,
    );
    const section = screen.getByTestId("usages");
    expect(section).toHaveTextContent("customers.email");
    expect(section).toHaveTextContent("app/models/customer.rb:14");
    expect(section).toHaveTextContent("web/src/api.ts:3");
  });
});

import { screen, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { type RunMeta, type RunOutput, useRunStore, useSqlTabs } from "@/entities/query";
import { ApiError, type PlanNode } from "@/shared/api";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";
import { columnWidths, MAX_COLUMN, MIN_COLUMN, moveCell } from "@/widgets/results-dock/model/grid";
import { MessagesTab } from "@/widgets/results-dock/ui/messages-tab";
import { PlanTab } from "@/widgets/results-dock/ui/plan-tab";

function meta(sql: string, overrides: Partial<RunMeta> = {}): RunMeta {
  return { id: 1, kind: "query", analyze: false, sql, tabId: "q1", source: "editor", startedAt: 0, ...overrides };
}

beforeEach(() => {
  server.use(http.get("/api/profiles", () => HttpResponse.json([makeProfile({ id: "p1" })])));
});

afterEach(() => {
  useRunStore.setState({ running: null, output: null, lastError: null, messages: [] });
  useSqlTabs.setState({ tabs: [], active: "erd", reveal: null, scope: null });
});

describe("Messages tab", () => {
  it("maps a guard position into the submitted SQL and offers to show it in the editor", async () => {
    const sql = "SELECT id\nFROM t\nWHER id = 1";
    useSqlTabs.setState({ tabs: [{ id: "q1", title: "query-1.sql", sql }], active: "q1" });
    const m = meta(sql);
    useRunStore.getState().start(m);
    useRunStore.getState().fail(
      m,
      new ApiError("rejected", 'syntax error at or near "WHER"', 422, "syntax", {
        position: sql.indexOf("WHER") + 1,
      }),
    );

    const { user } = renderWithProviders(<MessagesTab />);
    const item = screen.getByTestId("run-message");
    expect(within(item).getByText("The SQL has a syntax error")).toBeInTheDocument();
    expect(within(item).getByText("Line 3, column 1 of your SQL.")).toBeInTheDocument();
    expect(within(item).getByTestId("sql-error-mark")).toHaveTextContent("WHER");

    await user.click(within(item).getByRole("button", { name: "Show line 3, column 1 in the editor" }));
    expect(useSqlTabs.getState().reveal).toMatchObject({ tabId: "q1", from: 17, to: 21 });
    expect(useSqlTabs.getState().active).toBe("q1");
  });

  it("shows a database error's SQLSTATE and marks its position in the executed SQL", () => {
    const executed = "SELECT nme FROM shop.customers LIMIT 501";
    const m = meta("select nme from shop.customers");
    useRunStore.getState().start(m);
    useRunStore
      .getState()
      .fail(
        m,
        new ApiError("database", 'column "nme" does not exist', 400, undefined, { sqlstate: "42703", position: 8 }),
        executed,
      );

    renderWithProviders(<MessagesTab />);
    expect(screen.getByText("SQLSTATE")).toHaveTextContent("SQLSTATE 42703");
    expect(screen.getByText(/Character 8 of the executed SQL/)).toBeInTheDocument();
    expect(screen.getByTestId("sql-error-mark")).toHaveTextContent("nme");
    expect(screen.getByRole("figure", { name: "Executed SQL" })).toHaveTextContent(executed);
  });

  it("explains when the executed SQL is unknown instead of marking the wrong place", () => {
    const m = meta("select nme from t");
    useRunStore.getState().start(m);
    useRunStore
      .getState()
      .fail(m, new ApiError("database", "boom", 400, undefined, { sqlstate: "42703", position: 8 }));
    renderWithProviders(<MessagesTab />);
    expect(screen.getByText(/may not line up with what you typed/)).toBeInTheDocument();
    expect(screen.queryByTestId("sql-error-mark")).not.toBeInTheDocument();
  });

  it("offers a retry for a recovery conflict", () => {
    const m = meta("select 1");
    useRunStore.getState().start(m);
    useRunStore.getState().fail(m, new ApiError("recovery_conflict", "conflict with recovery", 503));
    renderWithProviders(<MessagesTab />);
    expect(screen.getByText(/hot_standby_feedback/)).toBeInTheDocument();
  });
});

const plan: PlanNode = {
  nodeType: "Hash Join",
  startupCost: 1,
  totalCost: 1000,
  planRows: 10,
  costShare: 0.2,
  flags: [],
  children: [
    {
      nodeType: "Seq Scan",
      relation: "shop.orders",
      startupCost: 0,
      totalCost: 800,
      planRows: 2_000_000,
      costShare: 0.8,
      flags: [
        { level: "danger", code: "seq_scan_filter", message: "Seq Scan reads about 2M rows to keep an estimated 10" },
      ],
      children: [],
    },
  ],
};

describe("Plan tab", () => {
  it("summarizes top flags, shows estimates and cost shares as text, and collapses nodes", async () => {
    const output: RunOutput = {
      meta: meta("select", { kind: "explain" }),
      result: null,
      executedSql: "SELECT 1",
      plan,
      overlay: { relations: {}, joins: [] },
      warnings: [],
    };
    useRunStore.setState({ output });
    const { user } = renderWithProviders(<PlanTab />);
    expect(screen.getByText(/Estimates only/)).toBeInTheDocument();
    const findings = screen.getByRole("list", { name: "Top plan findings" });
    expect(findings).toHaveTextContent(
      "Seq Scan reads about 2M rows to keep an estimated 10 (Seq Scan on shop.orders)",
    );
    expect(screen.getByTitle("80% of the plan's cost")).toHaveTextContent("80% of the plan's cost");
    expect(screen.getAllByTestId("plan-node")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "Collapse Hash Join" }));
    expect(screen.getAllByTestId("plan-node")).toHaveLength(1);
  });
});

describe("grid helpers", () => {
  it("sizes columns from the header and sample values, within bounds", () => {
    const widths = columnWidths(
      [
        { name: "id", type: "int4", typeOid: 23 },
        { name: "body", type: "text", typeOid: 25 },
        { name: "customer_id", type: "bigint", typeOid: 20, relation: "shop.orders", column: "customer_id" },
      ],
      [[1, "x".repeat(500), 3]],
    );
    expect(widths[0]).toBe(MIN_COLUMN);
    expect(widths[1]).toBe(MAX_COLUMN);
    expect(widths[2]).toBeGreaterThan(widths[0] ?? 0);
  });

  it("moves the active cell with arrows, paging and Home/End, staying in bounds", () => {
    const size = { rows: 100, cols: 5, page: 10 };
    expect(moveCell({ row: 0, col: 0 }, "ArrowUp", false, size)).toEqual({ row: 0, col: 0 });
    expect(moveCell({ row: 0, col: 0 }, "PageDown", false, size)).toEqual({ row: 10, col: 0 });
    expect(moveCell({ row: 5, col: 2 }, "End", false, size)).toEqual({ row: 5, col: 4 });
    expect(moveCell({ row: 5, col: 2 }, "End", true, size)).toEqual({ row: 99, col: 4 });
    expect(moveCell({ row: 5, col: 2 }, "Home", true, size)).toEqual({ row: 0, col: 0 });
    expect(moveCell({ row: 5, col: 2 }, "a", false, size)).toBeNull();
  });
});

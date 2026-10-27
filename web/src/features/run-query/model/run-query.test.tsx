import { screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useRunStore } from "@/entities/query";
import { useCanvasStore } from "@/entities/schema";
import { cancelRun, executeRun, RunConfirmDialog, requestRun, useRunPrompt } from "@/features/run-query";
import type { QueryRequest, QueryResult } from "@/shared/api";
import { makeProfile, renderWithProviders, signIn } from "@/test/render";
import { server } from "@/test/server";

const profile = makeProfile({ id: "p1", name: "reports", env: "prod-replica" });

function result(overrides: Partial<QueryResult> = {}): QueryResult {
  return {
    columns: [{ name: "id", type: "bigint", typeOid: 20, relation: "shop.orders", column: "id" }],
    rows: [[1], [2]],
    truncated: false,
    rowCount: 2,
    durationMs: 12,
    executedSql: "SELECT id FROM shop.orders LIMIT 501",
    plan: {
      nodeType: "Seq Scan",
      startupCost: 0,
      totalCost: 2_400_000,
      planRows: 2,
      costShare: 1,
      flags: [],
      children: [],
    },
    overlay: { relations: { "shop.orders": { level: "warn", scans: [], reasons: ["big"] } }, joins: [] },
    warnings: [],
    ...overrides,
  };
}

afterEach(() => {
  useRunStore.setState({ running: null, output: null, lastError: null, messages: [], last: null });
  useRunPrompt.setState({ prompt: null });
  useCanvasStore.getState().clearOverlay();
});

describe("run-query", () => {
  it("409 opens the cost confirmation; Run anyway re-sends with confirmCost and shows the result", async () => {
    const bodies: QueryRequest[] = [];
    server.use(
      http.post("/api/profiles/p1/query", async ({ request }) => {
        const body = (await request.json()) as QueryRequest;
        bodies.push(body);
        if (!body.confirmCost) {
          return HttpResponse.json(
            {
              error: {
                code: "cost_confirmation_required",
                message: "over the limit",
                estimatedCost: 2_450_000,
                estimatedRows: 1_800_000,
              },
            },
            { status: 409 },
          );
        }
        return HttpResponse.json(
          result({ warnings: ["Ran with an estimated cost of 2450000 because you confirmed it."] }),
        );
      }),
    );
    const { user } = renderWithProviders(<RunConfirmDialog />);
    await executeRun({ profile, sql: "SELECT id FROM shop.orders", kind: "query", analyze: false, tabId: "q1" });

    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("This query looks expensive");
    expect(dialog).toHaveTextContent("2,450,000");
    expect(dialog).toHaveTextContent("1.8M");
    expect(dialog).toHaveTextContent("The limit for reports is 1,000,000");
    expect(useRunStore.getState().output).toBeNull();
    expect(useRunStore.getState().messages).toHaveLength(0); // a confirmation stop is not an error

    await user.click(screen.getByRole("button", { name: "Run anyway" }));
    await waitFor(() => expect(useRunStore.getState().output?.result?.rowCount).toBe(2));
    expect(bodies).toEqual([
      { sql: "SELECT id FROM shop.orders" },
      { sql: "SELECT id FROM shop.orders", confirmCost: true },
    ]);
    expect(useRunStore.getState().messages[0]).toMatchObject({ level: "warning" });
    // The result's plan overlay went to the canvas store.
    expect(useCanvasStore.getState().overlay["shop.orders"]).toMatchObject({ tone: "warn", label: "Warning" });
  });

  it("asks before ANALYZE on a prod-replica, and Cancel sends nothing", async () => {
    let calls = 0;
    server.use(
      http.post("/api/profiles/p1/explain", () => {
        calls++;
        return HttpResponse.json({ executedSql: "x", plan: result().plan, overlay: result().overlay, warnings: [] });
      }),
    );
    const { user } = renderWithProviders(<RunConfirmDialog />);
    requestRun({ profile, sql: "SELECT 1", kind: "explain", analyze: true, tabId: "q1" });
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("ANALYZE runs the query on reports");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(calls).toBe(0);

    requestRun({ profile, sql: "SELECT 1", kind: "explain", analyze: true, tabId: "q1" });
    await user.click(await screen.findByRole("button", { name: "Run with ANALYZE" }));
    await waitFor(() => expect(useRunStore.getState().output?.result).toBeNull());
    expect(calls).toBe(1);
    expect(useRunStore.getState().dockTab).toBe("plan");
  });

  it("a guard rejection is recorded with its position for the editor and the Messages tab", async () => {
    signIn();
    server.use(
      http.post("/api/profiles/p1/query", () =>
        HttpResponse.json(
          { error: { code: "rejected", message: 'syntax error at or near "WHER"', detail: "syntax", position: 17 } },
          { status: 422 },
        ),
      ),
    );
    await executeRun({ profile, sql: "SELECT * FROM t WHER 1", kind: "query", analyze: false, tabId: "q1" });
    const { lastError, messages, dockTab } = useRunStore.getState();
    expect(lastError?.error).toMatchObject({ code: "rejected", detail: "syntax", position: 17 });
    expect(messages[0]).toMatchObject({ level: "error", meta: { tabId: "q1" } });
    expect(dockTab).toBe("messages");
  });

  it("cancelling aborts the request and records a cancellation, not an error", async () => {
    signIn();
    let aborted = false;
    // A fetch that never answers until aborted, like a long statement (MSW logs socket errors on abort).
    vi.spyOn(globalThis, "fetch").mockImplementation(
      (_input, init) =>
        new Promise<Response>((_, reject) => {
          init?.signal?.addEventListener("abort", () => {
            aborted = true;
            reject(new DOMException("The operation was aborted.", "AbortError"));
          });
        }),
    );
    const run = executeRun({ profile, sql: "SELECT pg_sleep(10)", kind: "query", analyze: false, tabId: "q1" });
    await waitFor(() => expect(useRunStore.getState().running).not.toBeNull());
    expect(cancelRun()).toBe(true);
    await run;
    expect(aborted).toBe(true);
    expect(useRunStore.getState().running).toBeNull();
    expect(useRunStore.getState().messages[0]).toMatchObject({ level: "info", text: "Cancelled" });
    expect(useRunStore.getState().lastError).toBeNull();
  });
});

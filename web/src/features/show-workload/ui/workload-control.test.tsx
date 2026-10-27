import { act, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { useCanvasStore } from "@/entities/schema";
import { useWorkloadView, WorkloadControl } from "@/features/show-workload";
import type { WorkloadSnapshot } from "@/shared/api";
import { renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

const snap: WorkloadSnapshot = {
  generatedAt: "2026-10-28T10:00:00Z",
  inRecovery: false,
  statements: {
    status: "not_installed",
    dealloc: 0,
    totalCalls: 0,
    totalTimeMs: 0,
    hiddenCalls: 0,
    unattributedCalls: 0,
    unreadCalls: 0,
    read: 0,
  },
  tables: {
    "public.orders": { seqScans: 40, idxScans: 60, rowsWritten: 5, liveRows: 100, queryCalls: 0, queryTimeMs: 0 },
    "public.users": { seqScans: 1, idxScans: 0, rowsWritten: 0, liveRows: 3, queryCalls: 0, queryTimeMs: 0 },
  },
};

afterEach(() => {
  useWorkloadView.setState({ on: false, metric: "time" });
  useCanvasStore.getState().clearOverlay();
});

/** Switches the active connection from inside the providers. */
function Switcher() {
  const [id, setId] = useState("p1");
  return (
    <>
      <button type="button" onClick={() => setId("p2")}>
        other connection
      </button>
      <WorkloadControl profileId={id} />
    </>
  );
}

describe("WorkloadControl", () => {
  it("shows table counters when pg_stat_statements is missing, and turns off when a query plan replaces it", async () => {
    let requests = 0;
    server.use(
      http.get("/api/profiles/p1/workload", () => {
        requests++;
        return HttpResponse.json(snap);
      }),
    );
    const { user } = renderWithProviders(<WorkloadControl profileId="p1" />);
    expect(requests).toBe(0); // nothing is read until asked

    await user.click(screen.getByRole("button", { name: /Show workload/ }));
    await waitFor(() => expect(useCanvasStore.getState().overlay["public.orders"]?.label).toBe("100 scans"));
    expect(useCanvasStore.getState().overlay["public.users"]?.label).toBe("1 scan");
    // Query time and calls need pg_stat_statements, so only scans and writes are offered.
    expect(screen.getAllByRole("radio").map((r) => r.textContent)).toEqual(["Scans", "Writes"]);
    expect(screen.getByRole("radio", { name: "Scans" })).toHaveAttribute("aria-checked", "true");

    act(() =>
      useCanvasStore
        .getState()
        .setHeatmap(
          { "public.users": { tone: "warn", label: "Seq Scan" } },
          { joins: [], involved: new Set(["public.users"]) },
        ),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Show workload/ })).toHaveAttribute("aria-pressed", "false"),
    );
    expect(useCanvasStore.getState().overlay["public.users"]?.label).toBe("Seq Scan");
    expect(requests).toBe(1);
  });

  it("does not overwrite a query plan applied while it was loading", async () => {
    let release = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    server.use(
      http.get("/api/profiles/p1/workload", async () => {
        await gate;
        return HttpResponse.json(snap);
      }),
    );
    const { user } = renderWithProviders(<WorkloadControl profileId="p1" />);
    await user.click(screen.getByRole("button", { name: /Show workload/ }));
    const plan = { "public.users": { tone: "warn" as const, label: "Seq Scan" } };
    act(() => useCanvasStore.getState().setHeatmap(plan, { joins: [], involved: new Set(["public.users"]) }));
    release();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Show workload/ })).toHaveAttribute("aria-pressed", "false"),
    );
    expect(useCanvasStore.getState().overlay).toBe(plan);
  });

  it("turns off and removes its badges when the connection changes", async () => {
    server.use(
      http.get("/api/profiles/p1/workload", () => HttpResponse.json(snap)),
      http.get("/api/profiles/p2/workload", () => HttpResponse.json(snap)),
    );
    const { user } = renderWithProviders(<Switcher />);
    await user.click(screen.getByRole("button", { name: /Show workload/ }));
    await waitFor(() => expect(useCanvasStore.getState().overlay["public.orders"]).toBeDefined());
    await user.click(screen.getByRole("button", { name: "other connection" }));
    await waitFor(() => expect(useCanvasStore.getState().overlay).toEqual({}));
    expect(useWorkloadView.getState().on).toBe(false);
  });

  it("renders nothing for the sample schema", () => {
    const { container } = renderWithProviders(<WorkloadControl profileId={undefined} />);
    expect(container.querySelector("button")).toBeNull();
  });
});

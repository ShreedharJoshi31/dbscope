import { act, renderHook, waitFor } from "@testing-library/react";
import { ReactFlowProvider } from "@xyflow/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildSchemaGraph, useCanvasStore } from "@/entities/schema";
import { sampleModel } from "@/test/fixtures";
import { gridLayout } from "@/widgets/erd-canvas/model/layout/grid";
import { useCanvasModel } from "@/widgets/erd-canvas/model/use-canvas-model";

const runLayout = vi.hoisted(() => vi.fn());
vi.mock("@/widgets/erd-canvas/model/layout/run-layout", () => ({
  LayoutCancelled: class LayoutCancelled extends Error {},
  runLayout,
}));

const graph = buildSchemaGraph(sampleModel());
const wrapper = ({ children }: { children: ReactNode }) => <ReactFlowProvider>{children}</ReactFlowProvider>;

beforeEach(() => {
  runLayout.mockImplementation(async (algorithm: string, g: typeof graph) => {
    const nodes = g.relations.map((r) => ({ id: `${r.schema}.${r.name}`, width: 280, height: 100 }));
    return { positions: new Map(Object.entries(gridLayout(nodes))), ms: 1, algorithm };
  });
});
afterEach(() => {
  act(() => {
    useCanvasStore.getState().setGraph(null);
    useCanvasStore.getState().setOverlay({});
  });
});

function setup(restored: Parameters<typeof useCanvasModel>[0]["restored"] = null) {
  const schedule = vi.fn();
  const hook = renderHook(
    () =>
      useCanvasModel({ graph, layoutKey: "app,billing,shop", restored, showInferred: true, preferred: null, schedule }),
    { wrapper },
  );
  return { ...hook, schedule };
}

describe("useCanvasModel", () => {
  it("lays out once and never rebuilds nodes or edges for selection, hover, search, focus or overlays", async () => {
    const { result } = setup();
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(runLayout).toHaveBeenCalledTimes(1);
    expect(runLayout).toHaveBeenCalledWith("layered", graph, expect.any(Set));
    const { nodes, edges } = result.current;
    expect(nodes).toHaveLength(42);
    expect(edges).toHaveLength(56); // 55 foreign keys, one of them composite with two pairs
    Object.freeze(nodes);
    Object.freeze(edges);
    for (const n of nodes) Object.freeze(n);

    act(() => {
      const s = useCanvasStore.getState();
      s.setGraph(graph);
      s.select("shop.orders", { reveal: true });
      s.setHoverEdge("shop.order_items|order_items_order_id_fkey");
      s.setSelectedEdge("shop.orders|orders_customer_id_fkey");
      s.toggleFocus();
      s.setFocusHops(2);
      s.setSearchQuery("customer");
      s.stepMatch(1);
      s.setOverlay({
        "shop.orders": { tone: "danger", label: "Locks table" },
        "app.users": { tone: "info", label: "Hot" },
      });
      s.patchOverlay("shop.orders", { tone: "warn", label: "Rewrites" });
    });

    expect(result.current.nodes).toBe(nodes);
    expect(result.current.edges).toBe(edges);
    expect(runLayout).toHaveBeenCalledTimes(1);
  });

  it("restores saved positions and collapsed tables without running a layout", async () => {
    const { result } = setup({
      key: "app,billing,shop",
      positions: Object.fromEntries(graph.relations.map((r, i) => [`${r.schema}.${r.name}`, { x: i * 10, y: 5 }])),
      collapsed: ["shop.orders", "gone.table"],
    });
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(runLayout).not.toHaveBeenCalled();
    expect(result.current.nodes.find((n) => n.id === "app.api_keys")?.position).toEqual({ x: 0, y: 5 });
    expect([...result.current.collapsed]).toEqual(["shop.orders"]);
    expect(result.current.nodes.find((n) => n.id === "shop.orders")?.data.collapsed).toBe(true);
  });

  it("collapsing one table replaces only that node and schedules a save", async () => {
    const { result, schedule } = setup();
    await waitFor(() => expect(result.current.ready).toBe(true));
    schedule.mockClear();
    const before = result.current.nodes;
    act(() => result.current.toggleCollapsed("shop.orders"));
    const after = result.current.nodes;
    const changed = after.filter((n, i) => n !== before[i]).map((n) => n.id);
    expect(changed).toEqual(["shop.orders"]);
    expect(schedule).toHaveBeenCalledWith(
      expect.objectContaining({ key: "app,billing,shop", collapsed: ["shop.orders"] }),
    );
  });
});

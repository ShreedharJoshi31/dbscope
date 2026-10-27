import { afterEach, describe, expect, it } from "vitest";
import { nodeVisual, useCanvasStore } from "@/entities/schema/model/canvas-store";
import { buildSchemaGraph } from "@/entities/schema/model/graph";
import { sampleModel } from "@/test/fixtures";

const graph = buildSchemaGraph(sampleModel());
const store = () => useCanvasStore.getState();

afterEach(() => {
  store().setGraph(null);
  store().clearSearch();
  store().select(null);
  store().setOverlay({});
  useCanvasStore.setState({ focus: null, focusHops: 1, hoverEdge: null, selectedEdge: null });
});

describe("canvas store", () => {
  it("focus mode keeps the selected table and its neighbours within N hops", () => {
    store().setGraph(graph);
    expect(store().toggleFocus()).toBe(false); // nothing selected
    store().select("shop.refunds");
    expect(store().toggleFocus()).toBe(true);
    expect([...(store().derived.keep ?? [])].sort()).toEqual(["shop.orders", "shop.payments", "shop.refunds"]);
    expect(nodeVisual(store(), "shop.variants")).toBe("|dim");
    store().setFocusHops(3);
    expect(store().derived.keep?.has("shop.variants")).toBe(true);
    expect(store().clearOne()).toBe("focus");
    expect(store().derived.keep).toBeNull();
  });

  it("search marks matches, and Enter cycles them with a reveal request", () => {
    store().setGraph(graph);
    store().setSearchQuery("address_id");
    expect(store().search.matches.map((m) => `${m.nodeId}.${m.column}`)).toEqual([
      "shop.orders.billing_address_id",
      "shop.orders.shipping_address_id",
    ]);
    const first = store().stepMatch(1);
    expect(first?.column).toBe("billing_address_id");
    expect(store().reveal).toMatchObject({ id: "shop.orders", column: "billing_address_id" });
    expect(store().derived.columnMarks.get("shop.orders")?.get("billing_address_id")).toBe("current-match");
    expect(store().derived.columnMarks.get("shop.orders")?.get("shipping_address_id")).toBe("match");
    store().stepMatch(1);
    store().stepMatch(1);
    expect(store().search.index).toBe(0); // wrapped around
    expect(nodeVisual(store(), "app.users")).toBe("|dim");
    expect(store().clearOne()).toBe("search");
    expect(store().derived.keep).toBeNull();
  });

  it("an active relationship highlights both endpoint columns and the chain", () => {
    store().setGraph(graph);
    store().setHoverEdge("shop.shipment_items|shipment_items_order_id_line_no_fkey");
    const marks = store().derived.columnMarks;
    expect(marks.get("shop.shipment_items")?.get("order_id")).toBe("endpoint");
    expect(marks.get("shop.shipment_items")?.get("line_no")).toBe("endpoint");
    expect(marks.get("shop.order_items")?.get("line_no")).toBe("endpoint");
    expect(store().derived.nodeMarks.get("shop.orders")).toBe("chain");
    store().setHoverEdge(null);
    expect(store().derived.columnMarks.size).toBe(0);
  });

  it("drops state that no longer exists when the graph changes", () => {
    store().setGraph(graph);
    store().select("app.events");
    store().setSelectedEdge("app.events|app.events(user_id)->app.users(id)");
    const model = sampleModel();
    model.relations = (model.relations ?? []).filter((r) => r.name !== "events");
    store().setGraph(buildSchemaGraph(model));
    expect(store().selectedId).toBeNull();
    expect(store().selectedEdge).toBeNull();
  });

  it("overlay entries are keyed by id and patchable", () => {
    store().setOverlay({ "shop.orders": { tone: "danger", label: "High risk" } });
    store().patchOverlay("app.users", { tone: "info", label: "Hot", intensity: 0.4 });
    store().patchOverlay("shop.orders", null);
    expect(store().overlay).toEqual({ "app.users": { tone: "info", label: "Hot", intensity: 0.4 } });
  });

  it("Esc clears one thing at a time, innermost first", () => {
    store().setGraph(graph);
    store().select("shop.orders");
    store().setDetailsOpen(true);
    store().toggleFocus();
    store().setSearchQuery("ord");
    expect([
      store().clearOne(),
      store().clearOne(),
      store().clearOne(),
      store().clearOne(),
      store().clearOne(),
    ]).toEqual(["search", "details", "focus", "selection", null]);
  });
});

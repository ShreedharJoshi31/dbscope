import { afterEach, describe, expect, it } from "vitest";
import { edgeVisual, MAX_FLOW_EDGES, nodeVisual, useCanvasStore } from "@/entities/schema/model/canvas-store";
import { clearMigrationOverlay, showMigrationDiff } from "@/entities/schema/model/diff-overlay";
import { buildSchemaGraph } from "@/entities/schema/model/graph";
import { heatmapFromOverlay, showQueryOverlay } from "@/entities/schema/model/heatmap";
import { sampleModel } from "@/test/fixtures";

const graph = buildSchemaGraph(sampleModel());
const store = () => useCanvasStore.getState();

afterEach(() => {
  store().setGraph(null);
  store().clearSearch();
  store().select(null);
  store().clearOverlay();
  store().setDiff(null);
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

  it("a query overlay badges relations with icon tone and text, animates only joined keys and dims the rest", () => {
    store().setGraph(graph);
    const overlay = {
      relations: {
        "shop.orders": {
          level: "danger" as const,
          scans: [{ nodeType: "Seq Scan", planRows: 2_100_000 }],
          reasons: ["Seq Scan reads about 2.1M rows to keep an estimated 2K"],
        },
        "shop.customers": {
          level: "good" as const,
          scans: [{ nodeType: "Index Scan", index: "customers_pkey", planRows: 1, actualRows: 1 }],
          reasons: [],
        },
      },
      joins: [{ from: "shop.orders", to: "shop.customers", method: "Hash Join" as const, joinType: "Inner" }],
    };
    const { entries } = heatmapFromOverlay(overlay);
    expect(entries["shop.orders"]).toEqual({
      tone: "danger",
      label: "Problem",
      details: ["Seq Scan reads about 2.1M rows to keep an estimated 2K", "Seq Scan: est. 2.1M rows"],
    });
    expect(entries["shop.customers"]?.details).toEqual(["Index Scan using customers_pkey: est. 1 row, actual 1 row"]);

    showQueryOverlay(overlay);
    const fk = "shop.orders|orders_customer_id_fkey";
    expect([...store().derived.flowEdges]).toEqual([fk]);
    expect(edgeVisual(store(), fk, "shop.orders", "shop.customers")).toBe("flow");
    expect(edgeVisual(store(), "shop.orders|orders_billing_address_id_fkey", "shop.orders", "shop.addresses")).toBe(
      "dim",
    );
    expect(nodeVisual(store(), "shop.orders")).toBe("");
    expect(nodeVisual(store(), "app.users")).toBe("|dim");
  });

  it("caps animated join edges", () => {
    store().setGraph(graph);
    const joins = graph.edges.map((e) => ({
      from: e.child,
      to: e.parent,
      method: "Nested Loop" as const,
      joinType: "Inner",
    }));
    showQueryOverlay({ relations: {}, joins });
    expect(store().derived.flowEdges.size).toBe(MAX_FLOW_EDGES);
  });

  it("answer highlights mark tables; Esc clears the overlay last; clearOverlay removes everything", () => {
    store().setGraph(graph);
    showQueryOverlay({ relations: { "shop.orders": { level: "warn", scans: [], reasons: [] } }, joins: [] });
    store().setHighlight(["shop.orders", "shop.payments"]);
    expect(nodeVisual(store(), "shop.payments")).toBe("highlight");
    expect(nodeVisual(store(), "app.users")).toBe("|dim");
    store().select("app.users");
    expect(store().clearOne()).toBe("selection");
    expect(store().clearOne()).toBe("overlay");
    expect(store().overlay).toEqual({});
    expect(store().derived.keep).toBeNull();
    showQueryOverlay({ relations: { "shop.orders": { level: "warn", scans: [], reasons: [] } }, joins: [] });
    store().clearOverlay();
    expect(store()).toMatchObject({ heat: null, highlight: null, overlay: {} });
  });

  it("selecting a changed relation highlights its dependents by certainty; others stay undimmed otherwise", () => {
    store().setGraph(graph);
    showMigrationDiff({
      sourceId: "m1",
      label: "0042_orders.sql",
      relations: { "shop.orders": "altered", "shop.parcel_events": "added" },
      columns: { "shop.orders": { currency: { change: "altered" } } },
      impact: {
        "shop.orders": [
          {
            nodeId: "shop.payments",
            kind: "fk_child",
            name: "shop.payments (payments_order_id_fkey)",
            certainty: "catalog",
            via: "shop.orders",
          },
          {
            nodeId: "shop.order_summaries",
            kind: "view",
            name: "shop.order_summaries",
            certainty: "heuristic",
            via: "shop.orders.currency",
          },
          { nodeId: null, kind: "function", name: "app.touch()", certainty: "catalog", via: "shop.orders" },
          { nodeId: "gone.table", kind: "view", name: "gone.table", certainty: "catalog", via: "shop.orders" },
        ],
      },
    });
    // No selection: colours only, nothing dimmed.
    expect(store().derived.keep).toBeNull();
    store().select("shop.orders");
    expect(store().derived.impactRoot).toBe("shop.orders");
    expect(store().derived.nodeMarks.get("shop.payments")).toBe("impact");
    expect(store().derived.nodeMarks.get("shop.order_summaries")).toBe("impact-heuristic");
    expect(store().derived.nodeMarks.get("shop.orders")).toBe("selected");
    expect([...(store().derived.keep ?? [])].sort()).toEqual(["shop.order_summaries", "shop.orders", "shop.payments"]);
    expect(nodeVisual(store(), "app.users")).toBe("|dim");
    // An unchanged table shows no impact.
    store().select("app.users");
    expect(store().derived.impactRoot).toBeNull();
    // The query overlay is separate: clearing it keeps the diff; Esc clears the diff last.
    showQueryOverlay({ relations: {}, joins: [] });
    store().clearOverlay();
    expect(store().diff).not.toBeNull();
    store().select(null);
    expect(store().clearOne()).toBe("migration");
    expect(store().diff).toBeNull();
    showMigrationDiff({ sourceId: "m1", label: "x", relations: {}, columns: {}, impact: {} });
    clearMigrationOverlay();
    expect(store().diff).toBeNull();
  });
});

import { describe, expect, it } from "vitest";
import { buildSchemaGraph } from "@/entities/schema";
import { sampleModel } from "@/test/fixtures";
import { buildEdges, buildNodes } from "@/widgets/erd-canvas/model/elements";
import { toElkGraph } from "@/widgets/erd-canvas/model/layout/elk-graph";
import { forceLayout } from "@/widgets/erd-canvas/model/layout/force";
import { gridLayout } from "@/widgets/erd-canvas/model/layout/grid";
import { routeEdge } from "@/widgets/erd-canvas/model/route";
import { nearestInDirection } from "@/widgets/erd-canvas/model/spatial";

const graph = buildSchemaGraph(sampleModel());
const none = new Set<string>();

describe("buildEdges", () => {
  const edges = buildEdges(graph, { collapsed: none, showInferred: true });

  it("draws one edge per column pair of a composite key, anchored to the column rows", () => {
    const composite = edges.filter((e) => e.data?.fkId === "shop.shipment_items|shipment_items_order_id_line_no_fkey");
    expect(composite.map((e) => [e.sourceHandle, e.targetHandle, e.data?.pairIndex])).toEqual([
      ["order_id:R", "order_id:L", 0],
      ["line_no:R", "line_no:L", 1],
    ]);
  });

  it("draws self references and anchors stubs to their header", () => {
    const self = edges.find((e) => e.data?.selfRef);
    expect(self).toMatchObject({
      source: "shop.categories",
      target: "shop.categories",
      sourceHandle: "parent_id:R",
      targetHandle: "id:L",
    });
    const toStub = edges.find((e) => e.target === "geo.countries");
    expect(toStub?.targetHandle).toBe("__header:L");
  });

  it("gives parallel foreign keys between two tables separate lanes", () => {
    const lanes = edges
      .filter((e) => e.source === "shop.orders" && e.target === "shop.addresses")
      .map((e) => [e.data?.lane, e.data?.laneCount]);
    expect(lanes).toEqual([
      [0, 2],
      [1, 2],
    ]);
  });

  it("hides unreviewed inferred keys on request but keeps accepted ones", () => {
    const hidden = buildEdges(graph, { collapsed: none, showInferred: false });
    expect(hidden).toHaveLength(edges.length - 2);
    const model = sampleModel();
    const fk = model.relations?.find((r) => r.name === "events")?.foreignKeys?.find((f) => f.inferred);
    if (fk) fk.confidence = 1;
    expect(buildEdges(buildSchemaGraph(model), { collapsed: none, showInferred: false })).toHaveLength(
      edges.length - 1,
    );
  });

  it("re-anchors to the header when the column is hidden by collapsing", () => {
    const collapsed = buildEdges(graph, {
      collapsed: new Set(["billing.mrr_by_plan", "app.users"]),
      showInferred: true,
    });
    // users.id is a PK, so it stays visible when collapsed.
    expect(collapsed.find((e) => e.id.startsWith("app.sessions|"))?.targetHandle).toBe("id:L");
  });
});

describe("buildNodes", () => {
  it("sets explicit size, measured and handles, and reuses unchanged node objects", () => {
    const positions = new Map([["shop.orders", { x: 5, y: 6 }]]);
    const first = buildNodes(graph, positions, none);
    const orders = first.find((n) => n.id === "shop.orders");
    expect(orders).toMatchObject({ position: { x: 5, y: 6 }, width: 280, measured: { width: 280 }, type: "table" });
    expect(orders?.handles?.length).toBe(2 + 2 * 7);
    expect(first.find((n) => n.id === "geo.countries")?.type).toBe("stub");
    const second = buildNodes(graph, positions, new Set(["shop.orders"]), new Map(first.map((n) => [n.id, n])));
    expect(second.filter((n, i) => n !== first[i]).map((n) => n.id)).toEqual(["shop.orders"]);
  });
});

describe("routeEdge", () => {
  const base = { childWidth: 280, parentWidth: 280, lane: 0, laneCount: 1, pairIndex: 0, selfRef: false };
  it("faces the tables: child right to parent left when the parent is to the right", () => {
    const r = routeEdge({ ...base, sourceX: 280, sourceY: 50, targetX: 600, targetY: 80 });
    expect([r.childSide, r.parentSide]).toEqual(["right", "left"]);
  });
  it("leaves from the child's left side when the parent is to the left", () => {
    const r = routeEdge({ ...base, sourceX: 1000, sourceY: 50, targetX: 100, targetY: 80 });
    expect([r.childSide, r.parentSide]).toEqual(["left", "right"]);
    expect(r.path.startsWith("M720 50")).toBe(true);
  });
  it("loops self references around the right side", () => {
    const r = routeEdge({ ...base, selfRef: true, sourceX: 280, sourceY: 120, targetX: 0, targetY: 60 });
    expect([r.childSide, r.parentSide]).toEqual(["right", "right"]);
    expect(r.path.endsWith("280 60")).toBe(true);
  });
  it("separates parallel lanes", () => {
    const a = routeEdge({ ...base, laneCount: 2, lane: 0, sourceX: 280, sourceY: 50, targetX: 600, targetY: 80 });
    const b = routeEdge({ ...base, laneCount: 2, lane: 1, sourceX: 280, sourceY: 50, targetX: 600, targetY: 80 });
    expect(a.path).not.toBe(b.path);
  });
});

describe("layouts", () => {
  const nodes = graph.relations.map((r) => ({ id: `${r.schema}.${r.name}`, width: 280, height: 120 }));
  const links = graph.edges.map((e) => ({ source: e.parent, target: e.child }));

  it("grid places every node without overlap", () => {
    const p = gridLayout(nodes, links);
    expect(Object.keys(p)).toHaveLength(42);
    const boxes = nodes.map((n) => ({ ...n, ...(p[n.id] as { x: number; y: number }) }));
    for (const a of boxes)
      for (const b of boxes)
        if (a !== b)
          expect(a.x + a.width <= b.x || b.x + b.width <= a.x || a.y + a.height <= b.y || b.y + b.height <= a.y).toBe(
            true,
          );
  });

  it("force is deterministic", () => {
    expect(forceLayout(nodes, links, 50)).toEqual(forceLayout(nodes, links, 50));
  });

  it("builds an ELK graph with ordered ports per column and one edge per pair, parent to child", () => {
    const elk = toElkGraph(graph, none);
    const orders = elk.children?.find((c) => c.id === "shop.orders");
    expect(orders?.layoutOptions).toEqual({ "elk.portConstraints": "FIXED_ORDER" });
    expect(orders?.ports).toHaveLength(2 * 8); // header + 7 columns, both sides
    expect(elk.edges).toHaveLength(56);
    expect(elk.edges?.find((e) => e.id === "shop.shipment_items|shipment_items_order_id_line_no_fkey#1")).toMatchObject(
      {
        sources: ["shop.order_items::line_no::E"],
        targets: ["shop.shipment_items::line_no::W"],
      },
    );
    expect(elk.layoutOptions?.["elk.layered.considerModelOrder.strategy"]).toBe("NODES_AND_EDGES");
  });
});

describe("nearestInDirection", () => {
  const boxes = [
    { id: "a", position: { x: 0, y: 0 }, width: 100, height: 100 },
    { id: "right", position: { x: 300, y: 20 }, width: 100, height: 100 },
    { id: "far-right", position: { x: 700, y: 0 }, width: 100, height: 100 },
    { id: "below", position: { x: 10, y: 400 }, width: 100, height: 100 },
  ];
  it("moves to the nearest table in the arrow's direction", () => {
    expect(nearestInDirection(boxes, "a", "right")).toBe("right");
    expect(nearestInDirection(boxes, "a", "down")).toBe("below");
    expect(nearestInDirection(boxes, "a", "left")).toBeNull();
    expect(nearestInDirection(boxes, "far-right", "left")).toBe("right");
  });
});

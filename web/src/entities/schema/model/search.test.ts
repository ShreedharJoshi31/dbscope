import { describe, expect, it } from "vitest";
import { buildSchemaGraph } from "@/entities/schema/model/graph";
import { searchGraph } from "@/entities/schema/model/search";
import { sampleModel } from "@/test/fixtures";

const graph = buildSchemaGraph(sampleModel());

describe("searchGraph", () => {
  it("ranks exact table names first, then prefixes, then substrings, then columns", () => {
    const hits = searchGraph(graph, "orders");
    expect(hits[0]).toEqual({ nodeId: "shop.orders" });
    const tables = hits.filter((h) => !h.column).map((h) => h.nodeId);
    expect(tables).toEqual(["shop.orders"]);
    expect(hits.some((h) => h.column)).toBe(false);
    expect(
      searchGraph(graph, "order")
        .filter((h) => !h.column)
        .map((h) => h.nodeId),
    ).toEqual(["shop.order_items", "shop.order_summaries", "shop.orders"]);
  });

  it("finds columns, case-insensitively", () => {
    const hits = searchGraph(graph, "TRACKING");
    expect(hits).toEqual([{ nodeId: "shop.shipments", column: "tracking_number" }]);
  });

  it("narrows to a table's columns with table.column", () => {
    expect(searchGraph(graph, "orders.billing")).toEqual([{ nodeId: "shop.orders", column: "billing_address_id" }]);
    expect(searchGraph(graph, "shop.orders.cust")).toEqual([{ nodeId: "shop.orders", column: "customer_id" }]);
  });

  it("matches qualified names and returns nothing for blank queries", () => {
    expect(searchGraph(graph, "geo.")[0]).toEqual({ nodeId: "geo.countries" });
    expect(searchGraph(graph, "   ")).toEqual([]);
    expect(searchGraph(graph, "zzz_nothing")).toEqual([]);
  });
});

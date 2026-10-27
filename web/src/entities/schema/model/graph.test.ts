import { describe, expect, it } from "vitest";
import {
  buildSchemaGraph,
  fkColumnSets,
  getSchemaGraph,
  neighboursWithin,
  relationByKey,
  relationshipChain,
} from "@/entities/schema/model/graph";
import { sampleModel } from "@/test/fixtures";

describe("buildSchemaGraph", () => {
  const graph = buildSchemaGraph(sampleModel());

  it("indexes relations by schema.name and keeps model order", () => {
    expect(graph.relations).toHaveLength(42);
    expect(relationByKey(graph, "shop.orders")?.name).toBe("orders");
    expect(relationByKey(graph, "orders")).toBeUndefined();
    expect(graph.schemas).toEqual(["app", "billing", "shop", "geo"]);
  });

  it("pairs composite foreign key columns in declaration order", () => {
    const edge = graph.edgeById.get("shop.shipment_items|shipment_items_order_id_line_no_fkey");
    expect(edge?.parent).toBe("shop.order_items");
    expect(edge?.pairs).toEqual([
      { column: "order_id", refColumn: "order_id" },
      { column: "line_no", refColumn: "line_no" },
    ]);
  });

  it("marks inferred keys, their confidence and acceptance", () => {
    const inferred = graph.edges.filter((e) => e.inferred);
    expect(inferred.map((e) => [e.fk.name, e.confidence, e.accepted])).toEqual([
      ["app.events(user_id)->app.users(id)", 0.8, false],
      ["shop.refunds(payment_id)->shop.payments(id)", 0.6, false],
    ]);
    const model = sampleModel();
    const events = model.relations?.find((r) => r.name === "events");
    const fk = events?.foreignKeys?.find((f) => f.inferred);
    if (fk) fk.confidence = 1;
    expect(buildSchemaGraph(model).edges.find((e) => e.inferred)?.accepted).toBe(true);
  });

  it("keeps edges into stub relations and counts keys whose target is missing", () => {
    const stub = relationByKey(graph, "geo.countries");
    expect(stub?.stub).toBe(true);
    expect(graph.incoming.get("geo.countries")?.map((e) => e.child)).toEqual([
      "app.user_profiles",
      "billing.accounts",
      "shop.addresses",
      "shop.warehouses",
    ]);
    expect(graph.danglingCount).toBe(0);

    const model = sampleModel();
    model.relations = (model.relations ?? []).filter((r) => !r.stub);
    const withoutStub = buildSchemaGraph(model);
    expect(withoutStub.danglingCount).toBe(4);
    expect(withoutStub.edges).toHaveLength(graph.edges.length - 4);
  });

  it("flags self references, optional parents and one-to-one keys", () => {
    const self = graph.edgeById.get("shop.categories|categories_parent_id_fkey");
    expect(self?.selfRef).toBe(true);
    expect(self?.optional).toBe(true);
    const profile = graph.edgeById.get("app.user_profiles|user_profiles_user_id_fkey");
    expect(profile?.oneToOne).toBe(true);
    expect(profile?.optional).toBe(false);
    expect(graph.edgeById.get("shop.orders|orders_customer_id_fkey")?.oneToOne).toBe(false);
  });

  it("keeps parallel foreign keys between the same tables apart", () => {
    const toAddresses = graph.outgoing.get("shop.orders")?.filter((e) => e.parent === "shop.addresses");
    expect(toAddresses?.map((e) => e.pairs[0]?.column)).toEqual(["billing_address_id", "shipping_address_id"]);
  });

  it("caches one graph per Model object", () => {
    const model = sampleModel();
    expect(getSchemaGraph(model)).toBe(getSchemaGraph(model));
    expect(getSchemaGraph(sampleModel())).not.toBe(getSchemaGraph(model));
  });

  it("treats null slices from Go as empty", () => {
    const empty = buildSchemaGraph({
      serverVersion: 170000,
      schemas: null,
      relations: null,
      enums: null,
      introspectedAt: "2026-10-26T00:00:00Z",
      durationMs: 0,
    });
    expect(empty.relations).toEqual([]);
    expect(empty.schemas).toEqual([]);
  });
});

describe("neighboursWithin", () => {
  const graph = buildSchemaGraph(sampleModel());

  it("walks foreign keys in both directions up to N hops", () => {
    const one = neighboursWithin(graph, "shop.refunds", 1);
    expect([...one].sort()).toEqual(["shop.orders", "shop.payments", "shop.refunds"]);
    const two = neighboursWithin(graph, "shop.refunds", 2);
    expect(two.has("shop.customers")).toBe(true);
    expect(two.has("shop.order_items")).toBe(true);
    expect(two.has("shop.variants")).toBe(false);
    expect(neighboursWithin(graph, "shop.refunds", 3).has("shop.variants")).toBe(true);
  });

  it("handles self references and unknown keys", () => {
    expect([...neighboursWithin(graph, "shop.categories", 1)].sort()).toEqual(["shop.categories", "shop.products"]);
    expect(neighboursWithin(graph, "nope.nothing", 2).size).toBe(0);
    expect([...neighboursWithin(graph, "app.feature_flags", 3)]).toEqual(["app.feature_flags"]);
  });

  it("follows inferred keys like declared ones", () => {
    expect(neighboursWithin(graph, "app.events", 1).has("app.users")).toBe(true);
  });
});

describe("relationshipChain", () => {
  it("lights the child, the parent, the parent's parents and the child's children", () => {
    const graph = buildSchemaGraph(sampleModel());
    const edge = graph.edgeById.get("shop.order_items|order_items_order_id_fkey");
    if (!edge) throw new Error("fixture changed");
    const chain = relationshipChain(graph, edge);
    expect(chain.has("shop.orders")).toBe(true);
    expect(chain.has("shop.order_items")).toBe(true);
    expect(chain.has("shop.customers")).toBe(true); // orders -> customers
    expect(chain.has("app.users")).toBe(true); // customers -> users (2 steps up)
    expect(chain.has("shop.shipment_items")).toBe(true); // child of order_items
    expect(chain.has("shop.refunds")).toBe(false); // a sibling of order_items, not on the chain
  });
});

describe("fkColumnSets", () => {
  it("separates unreviewed inferred columns from declared ones", () => {
    const events = sampleModel().relations?.find((r) => r.name === "events");
    if (!events) throw new Error("fixture changed");
    const sets = fkColumnSets(events);
    expect([...sets.confirmed]).toEqual(["team_id"]);
    expect([...sets.inferred]).toEqual(["user_id"]);
  });
});

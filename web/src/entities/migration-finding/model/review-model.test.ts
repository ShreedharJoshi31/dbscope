import { describe, expect, it } from "vitest";
import {
  canvasRelationFor,
  columnChanges,
  impactByRelation,
  impactNodeId,
  initialSelection,
  normalizeSql,
  relationChanges,
  statementAtLine,
  toReviewModel,
} from "@/entities/migration-finding/model/review-model";
import { migrationReport, migrationReportPg18, migrationSources, migrationSourcesPg18 } from "@/test/fixtures";

describe("toReviewModel", () => {
  const model = toReviewModel(migrationReport(), migrationSources());

  it("maps the report header: overall rating, counts per rating, PG version", () => {
    expect(model.rating).toBe("critical");
    expect(model.counts).toEqual({ critical: 2, high: 2, medium: 3, low: 1 });
    expect(model.findingCount).toBe(10);
    expect(model.pgLabel).toBe("PostgreSQL 17");
    expect(model.warnings).toEqual([]); // the agent sends null
  });

  it("groups statements by file in execution order and keeps the file text for line numbers", () => {
    expect(model.files.map((f) => [f.name, f.statements])).toEqual([
      ["0042_orders.sql", [0, 1, 2, 3]],
      ["0043_cleanup.sql", [4, 5, 6, 7]],
    ]);
    const first = model.files[0];
    expect(first?.synthesized).toBe(false);
    expect(first?.text.split("\n")[1]).toBe("ALTER TABLE shop.orders ADD COLUMN tracking_code text;");
    expect(model.statements[5]).toMatchObject({
      fileIndex: 1,
      relations: ["shop.shipments", "shop.orders", "shop.warehouses"],
      unparseable: false,
    });
    expect(model.statements[6]?.unparseable).toBe(true);
  });

  it("links unanalyzable entries to their statement and explains them", () => {
    expect(model.unanalyzable).toHaveLength(1);
    const u = model.unanalyzable[0];
    expect(u?.statementIndex).toBe(6);
    expect(u?.explanation).toContain("PostgreSQL 17 grammar cannot parse");
    expect(u?.explanation).toContain("the server will reject it too");
  });

  it("explains PostgreSQL 18 syntax against the PostgreSQL 17 grammar", () => {
    const pg18 = toReviewModel(migrationReportPg18(), migrationSourcesPg18());
    expect(pg18.pgLabel).toBe("PostgreSQL 18");
    expect(pg18.unanalyzable[0]?.explanation).toMatch(
      /^PostgreSQL 18 syntax cannot be analyzed with the PostgreSQL 17 grammar/,
    );
    expect(pg18.statements[1]?.findings[0]?.ruleId).toBe("DBS-PARSE");
  });

  it("rebuilds a file from its statements when the source is missing", () => {
    const m = toReviewModel(migrationReport(), []);
    const lines = m.files[0]?.text.split("\n") ?? [];
    expect(m.files[0]?.synthesized).toBe(true);
    expect(lines[0]).toBe("");
    expect(lines[1]).toBe("ALTER TABLE shop.orders ADD COLUMN tracking_code text;");
    expect(lines[4]).toMatch(/^CREATE TABLE shop.parcel_events/);
  });

  it("starts on the first statement with the worst rating and finds statements by line", () => {
    expect(initialSelection(model)).toBe(5);
    expect(statementAtLine(model, 0, 7)).toBe(3); // inside the multi-line CREATE TABLE
    expect(statementAtLine(model, 0, 1)).toBeNull(); // the comment line
  });

  it("normalizes line endings like the agent", () => {
    expect(normalizeSql("\uFEFFa;\r\nb;\rc;")).toBe("a;\nb;\nc;");
  });
});

describe("canvas diff and impact", () => {
  const report = migrationReport();

  it("maps relation and column changes", () => {
    expect(relationChanges(report.diff)).toEqual({
      "shop.parcel_events": "added",
      "shop.shipments": "dropped",
      "shop.customers": "altered",
      "shop.orders": "altered",
      "shop.shipment_items": "altered",
    });
    expect(columnChanges(report.diff)["shop.customers"]).toEqual({ email: { change: "renamed", to: "email_address" } });
    expect(columnChanges(report.diff)["shop.orders"]?.tracking_code).toEqual({ change: "added" });
  });

  it("groups column impact under its relation and resolves dependents to canvas nodes", () => {
    const impact = impactByRelation(report);
    const customers = impact["shop.customers"] ?? [];
    expect(customers.map((i) => [i.nodeId, i.certainty, i.via])).toContainEqual([
      "shop.order_summaries",
      "heuristic",
      "shop.customers.email",
    ]);
    expect(customers.find((i) => i.kind === "fk_child")?.nodeId).toBe("shop.addresses");
    expect(impact["shop.customers.email"]).toBeUndefined();
  });

  it("parses every impact name format", () => {
    expect(
      impactNodeId({ kind: "fk_child", name: "shop.payments (payments_order_id_fkey)", certainty: "catalog" }),
    ).toBe("shop.payments");
    expect(impactNodeId({ kind: "trigger", name: "orders_audit ON shop.orders", certainty: "heuristic" })).toBe(
      "shop.orders",
    );
    expect(impactNodeId({ kind: "policy", name: "tenant_isolation ON app.users", certainty: "catalog" })).toBe(
      "app.users",
    );
    expect(impactNodeId({ kind: "generated_column", name: "shop.orders.total", certainty: "heuristic" })).toBe(
      "shop.orders",
    );
    expect(impactNodeId({ kind: "function", name: "app.touch()", certainty: "catalog" })).toBeNull();
    expect(impactNodeId({ kind: "index", name: "shop.orders_idx", certainty: "catalog" })).toBeNull();
  });

  it("Show on canvas picks a changed relation the statement locks that is on the canvas", () => {
    const changed = relationChanges(report.diff);
    const all = () => true;
    expect(canvasRelationFor({ relations: ["shop.warehouses", "shop.shipments"] }, changed, all)).toBe(
      "shop.shipments",
    );
    expect(canvasRelationFor({ relations: ["shop.warehouses"] }, changed, all)).toBe("shop.warehouses");
    expect(canvasRelationFor({ relations: ["shop.parcel_events"] }, changed, (k) => k !== "shop.parcel_events")).toBe(
      null,
    );
  });
});

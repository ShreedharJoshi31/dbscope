import { describe, expect, it } from "vitest";
import { HEADER_HANDLE } from "@/entities/schema/model/keys";
import {
  anchorHandle,
  BODY_PADDING,
  columnOffsetY,
  FOOTER_ROW_HEIGHT,
  HEADER_HEIGHT,
  NODE_WIDTH,
  nodeLayout,
  ROW_HEIGHT,
  STUB_HEIGHT,
  STUB_WIDTH,
} from "@/entities/schema/model/sizing";
import type { Relation } from "@/shared/api";
import { sampleModel } from "@/test/fixtures";

function relation(name: string): Relation {
  const r = sampleModel().relations?.find((x) => `${x.schema}.${x.name}` === name);
  if (!r) throw new Error(`no ${name} in fixture`);
  return r;
}

describe("nodeLayout", () => {
  it("sizes a table from its column count, without the DOM", () => {
    const users = relation("app.users"); // 7 columns
    const layout = nodeLayout(users, false);
    expect(layout.width).toBe(NODE_WIDTH);
    expect(layout.height).toBe(HEADER_HEIGHT + 7 * ROW_HEIGHT + BODY_PADDING);
    expect(layout.rows.map((r) => r.column.name)).toEqual(users.columns?.map((c) => c.name));
  });

  it("collapsed mode keeps PK and FK columns (including inferred) and says how many are hidden", () => {
    const events = relation("app.events"); // id PK, team_id FK, user_id inferred FK, 3 others
    const layout = nodeLayout(events, true);
    expect(layout.rows.map((r) => r.column.name)).toEqual(["id", "user_id", "team_id"]);
    expect(layout.footers).toEqual([{ kind: "hidden", count: 3 }]);
    expect(layout.height).toBe(HEADER_HEIGHT + 3 * ROW_HEIGHT + FOOTER_ROW_HEIGHT + BODY_PADDING);
  });

  it("adds a partitions footer with the partition key", () => {
    const layout = nodeLayout(relation("app.audit_log"), false);
    expect(layout.footers).toEqual([{ kind: "partitions", count: 3, key: "RANGE (at)" }]);
    expect(layout.height).toBe(HEADER_HEIGHT + 9 * ROW_HEIGHT + FOOTER_ROW_HEIGHT + BODY_PADDING);
  });

  it("gives stubs a compact fixed size and header handles only", () => {
    const layout = nodeLayout(relation("geo.countries"), false);
    expect([layout.width, layout.height]).toEqual([STUB_WIDTH, STUB_HEIGHT]);
    expect(layout.handles.map((h) => h.id)).toEqual([`${HEADER_HANDLE}:L`, `${HEADER_HANDLE}:R`]);
    expect(anchorHandle(layout, "code", "L")).toBe(`${HEADER_HANDLE}:L`);
  });

  it("puts one handle per column on each side at the row centre", () => {
    const layout = nodeLayout(relation("shop.orders"), false);
    const customer = layout.handles.filter((h) => h.id.startsWith("customer_id:"));
    expect(customer).toEqual([
      expect.objectContaining({ id: "customer_id:L", type: "target", position: "left", x: 0 }),
      expect.objectContaining({ id: "customer_id:R", type: "source", position: "right", x: NODE_WIDTH - 1 }),
    ]);
    // customer_id is the second row: header + 1.5 rows, minus half the 1px handle.
    expect(customer[0]?.y).toBe(HEADER_HEIGHT + ROW_HEIGHT * 1.5 - 0.5);
    expect(columnOffsetY(layout, "customer_id")).toBe(HEADER_HEIGHT + ROW_HEIGHT * 1.5);
  });

  it("anchors hidden columns to the header and is cached per relation and mode", () => {
    const orders = relation("shop.orders");
    const collapsed = nodeLayout(orders, true);
    expect(anchorHandle(collapsed, "placed_at", "R")).toBe(`${HEADER_HANDLE}:R`);
    expect(anchorHandle(collapsed, "customer_id", "R")).toBe("customer_id:R");
    expect(nodeLayout(orders, true)).toBe(collapsed);
    expect(nodeLayout(orders, false)).not.toBe(collapsed);
  });
});

import { describe, expect, it } from "vitest";
import { sampleModel } from "@/test/fixtures";
import { schemaCompletion } from "@/widgets/sql-editor/model/schema-completion";

describe("schema completion", () => {
  it("builds schema → table → columns from the loaded model, with types as details", () => {
    const { namespace, defaultSchema } = schemaCompletion(sampleModel());
    const ns = namespace as Record<
      string,
      Record<string, { self: { label: string; detail?: string }; children: { label: string; detail?: string }[] }>
    >;
    // geo.countries is a stub (outside the selected schemas): no columns to complete.
    expect(Object.keys(ns).sort()).toEqual(["app", "billing", "shop"]);
    const orders = ns.shop?.orders;
    expect(orders?.self).toMatchObject({ label: "orders", detail: "table" });
    expect(orders?.children.find((c) => c.label === "placed_at")?.detail).toBe("timestamp with time zone");
    expect(defaultSchema).toBe("app"); // no "public": the first schema
  });

  it("is empty before a schema loads", () => {
    expect(schemaCompletion(undefined)).toEqual({ namespace: {}, defaultSchema: undefined });
  });
});

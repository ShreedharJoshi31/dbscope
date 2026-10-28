import { describe, expect, it } from "vitest";
import { diffModels } from "@/entities/schema/model/drift";
import type { Model } from "@/shared/api";

const model = (schemas: string[], relations: unknown[]) =>
  ({ serverVersion: 0, schemas, enums: [], relations }) as unknown as Model;
const table = (schema: string, name: string, extra: object = {}) => ({
  schema,
  name,
  kind: "table",
  rowEstimate: 1,
  pages: 0,
  columns: [{ name: "id", type: "int", nullable: false }],
  ...extra,
});

describe("diffModels", () => {
  it("names added, dropped and altered relations and columns", () => {
    const there = model(
      ["app"],
      [
        table("app", "users", {
          columns: [
            { name: "id", type: "int", nullable: false },
            { name: "old", type: "text", nullable: true },
          ],
          rowEstimate: 5,
        }),
        table("app", "gone"),
      ],
    );
    const here = model(
      ["app"],
      [
        table("app", "users", {
          columns: [
            { name: "id", type: "bigint", nullable: false },
            { name: "email", type: "text", nullable: true },
          ],
          indexes: [
            { name: "by_email", columns: ["email"], unique: true, primary: false, method: "btree", valid: true },
          ],
          findings: [{ ruleId: "R1", level: "warn", message: "no index" }],
          rowEstimate: 999,
          comment: "different",
        }),
        table("app", "fresh"),
      ],
    );
    const d = diffModels(here, there);
    expect(d.relations).toEqual({ "app.users": "altered", "app.fresh": "added", "app.gone": "dropped" });
    expect(d.columns["app.users"]).toEqual({
      id: { change: "altered", detail: "int → bigint" },
      email: { change: "added", detail: "column added (text)" },
      old: { change: "dropped", detail: "column missing here (was text)" },
    });
    expect(d.items.map((i) => i.detail)).toContain("index by_email added: unique btree (email)");
    expect(d.items.map((i) => i.detail)).toContain("new risk: no index");
  });

  it("does not call a relation in an unread schema dropped, nor count inferred keys", () => {
    const fk = {
      name: "guess",
      columns: ["id"],
      ref: { schema: "app", name: "x" },
      refColumns: ["id"],
      validated: false,
      inferred: true,
    };
    const there = model(["app", "billing"], [table("app", "a"), table("billing", "invoices")]);
    const here = model(["app"], [table("app", "a", { foreignKeys: [fk] })]);
    const d = diffModels(here, there);
    expect(d.relations).toEqual({ "billing.invoices": "not-visible" });
  });

  it("finds nothing between a model and itself", () => {
    const m = model(["app"], [table("app", "a")]);
    expect(diffModels(m, m).items).toEqual([]);
  });
});

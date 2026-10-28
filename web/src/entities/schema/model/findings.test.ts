import { describe, expect, it } from "vitest";
import { schemaFindings, worstLevel } from "@/entities/schema/model/findings";
import type { Finding, Model } from "@/shared/api";

const rel = (name: string, findings: Finding[] | undefined) => ({
  schema: "app",
  name,
  kind: "table" as const,
  rowEstimate: null,
  pages: 0,
  columns: [],
  findings,
});

describe("findings", () => {
  it("orders the most serious first and names the worst level", () => {
    const model = {
      serverVersion: 0,
      schemas: ["app"],
      enums: [],
      relations: [
        rel("b", [{ ruleId: "R-INFO", level: "info", message: "i" }]),
        rel("a", [
          { ruleId: "R-WARN", level: "warn", message: "w" },
          { ruleId: "R-DANGER", level: "danger", message: "d", column: "x" },
        ]),
        rel("c", undefined),
      ],
    } as unknown as Model;
    expect(schemaFindings(model).map((r) => `${r.relation}:${r.finding.ruleId}`)).toEqual([
      "app.a:R-DANGER",
      "app.a:R-WARN",
      "app.b:R-INFO",
    ]);
    expect(worstLevel(model.relations?.[1]?.findings)).toBe("danger");
    expect(worstLevel(undefined)).toBeUndefined();
  });
});

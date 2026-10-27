import { describe, expect, it } from "vitest";
import { toReviewModel } from "@/entities/migration-finding/model/review-model";
import { DEFAULT_RISK_SORT, nextRiskSort, riskRows, sortRiskRows } from "@/entities/migration-finding/model/risk-rows";
import { migrationReport, migrationSources } from "@/test/fixtures";

const rows = riskRows(toReviewModel(migrationReport(), migrationSources()));
const ids = (list: typeof rows) => list.map((r) => `${r.ruleId}@${r.line}`);

describe("risk rows", () => {
  it("has one row per finding with the relation it names", () => {
    expect(rows).toHaveLength(10);
    expect(rows.find((r) => r.ruleId === "DBS-RENAME")?.relation).toBe("shop.customers");
    expect(rows.find((r) => r.ruleId === "DBS-DROP-TABLE")?.relation).toBe("shop.shipments");
    expect(rows.find((r) => r.ruleId === "DBS-PARSE")?.relation).toBe("");
  });

  it("sorts critical first by default, ties in file order", () => {
    expect(ids(sortRiskRows(rows, DEFAULT_RISK_SORT)).slice(0, 5)).toEqual([
      "DBS-DROP-TABLE@2",
      "DBS-WILL-FAIL@2",
      "DBS-PARSE@3",
      "DBS-RENAME@4",
      "DBS-TYPE-REWRITE@4",
    ]);
    const asc = sortRiskRows(rows, { key: "rating", dir: "asc" });
    expect(asc[0]?.rating).toBe("medium");
    expect(asc.at(-1)?.rating).toBe("critical");
  });

  it("sorts by rule, location, relation and message", () => {
    expect(sortRiskRows(rows, { key: "rule", dir: "asc" })[0]?.ruleId).toBe("DBS-DROP-TABLE");
    expect(sortRiskRows(rows, { key: "rule", dir: "desc" })[0]?.ruleId).toBe("DBS-WILL-FAIL");
    const byLocation = sortRiskRows(rows, { key: "location", dir: "asc" });
    expect(byLocation.map((r) => `${r.file}:${r.line}`)[0]).toBe("0042_orders.sql:2");
    expect(byLocation.at(-1)?.file).toBe("0043_cleanup.sql");
    expect(sortRiskRows(rows, { key: "location", dir: "desc" })[0]?.ruleId).toBe("DBS-TYPE-REWRITE");
    const byRelation = sortRiskRows(rows, { key: "relation", dir: "asc" }).map((r) => r.relation);
    expect(byRelation[0]).toBe("");
    expect(byRelation.at(-1)).toBe("shop.shipments");
    const byMessage = sortRiskRows(rows, { key: "message", dir: "asc" }).map((r) => r.message);
    expect([...byMessage].sort((a, b) => a.localeCompare(b, "en", { sensitivity: "base" }))).toEqual(byMessage);
  });

  it("is stable and does not mutate its input", () => {
    const before = ids(rows);
    sortRiskRows(rows, { key: "rule", dir: "desc" });
    expect(ids(rows)).toEqual(before);
    expect(ids(sortRiskRows(rows, { key: "rule", dir: "asc" }))).toEqual(
      ids(sortRiskRows(rows, { key: "rule", dir: "asc" })),
    );
  });

  it("flips the active header and starts others in their natural direction", () => {
    expect(nextRiskSort(DEFAULT_RISK_SORT, "rating")).toEqual({ key: "rating", dir: "asc" });
    expect(nextRiskSort(DEFAULT_RISK_SORT, "rule")).toEqual({ key: "rule", dir: "asc" });
    expect(nextRiskSort({ key: "rule", dir: "asc" }, "rating")).toEqual({ key: "rating", dir: "desc" });
  });
});

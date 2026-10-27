import { describe, expect, it } from "vitest";
import {
  actualTotalRows,
  countPlanNodes,
  isAnalyzed,
  nodeShare,
  nodeTitle,
  planNodeIds,
  summarizeFlags,
} from "@/entities/query/model/plan";
import type { PlanNode } from "@/shared/api";

function node(partial: Partial<PlanNode>): PlanNode {
  return {
    nodeType: "Result",
    startupCost: 0,
    totalCost: 1,
    planRows: 1,
    costShare: 0,
    flags: [],
    children: [],
    ...partial,
  };
}

const plan = node({
  nodeType: "Hash Join",
  joinType: "Left",
  costShare: 0.1,
  children: [
    node({
      nodeType: "Seq Scan",
      relation: "shop.orders",
      alias: "o",
      costShare: 0.8,
      flags: [
        { level: "warn", code: "seq_scan_large", message: "Seq Scan reads about 2.1M rows" },
        { level: "info", code: "workers_shortfall", message: "Launched 1 of 2 planned parallel workers" },
      ],
    }),
    node({
      nodeType: "Index Scan",
      relation: "shop.customers",
      alias: "customers",
      index: "customers_pkey",
      costShare: 0.1,
      flags: [{ level: "danger", code: "disk_spill", message: "Sort spilled to disk; work_mem was too small" }],
    }),
  ],
});

describe("plan helpers", () => {
  it("titles nodes with join type, index, relation and a differing alias", () => {
    expect(nodeTitle(plan)).toBe("Hash Join (Left)");
    expect(nodeTitle(plan.children[0] as PlanNode)).toBe("Seq Scan on shop.orders o");
    expect(nodeTitle(plan.children[1] as PlanNode)).toBe("Index Scan using customers_pkey on shop.customers");
  });

  it("summarizes flags worst first, then by share, without duplicates", () => {
    const dup = node({ children: [plan, plan] });
    const flags = summarizeFlags(dup);
    expect(flags.map((f) => f.code)).toEqual(["disk_spill", "seq_scan_large", "workers_shortfall"]);
    expect(flags[1]).toMatchObject({ node: "Seq Scan on shop.orders o", share: 0.8 });
  });

  it("uses time share when analyzed and cost share otherwise, clamped to 0..1", () => {
    const analyzed = node({ costShare: 0.2, timeShare: 0.7, actualLoops: 1, actualRows: 10 });
    expect(isAnalyzed(analyzed)).toBe(true);
    expect(isAnalyzed(plan)).toBe(false);
    expect(nodeShare(analyzed, true)).toBe(0.7);
    expect(nodeShare(analyzed, false)).toBe(0.2);
    expect(nodeShare(node({ costShare: 1.4 }), false)).toBe(1);
    expect(nodeShare(node({ costShare: Number.NaN }), false)).toBe(0);
  });

  it("multiplies per-loop actual rows by loops", () => {
    expect(actualTotalRows(node({ actualRows: 5, actualLoops: 40 }))).toBe(200);
    expect(actualTotalRows(node({}))).toBeUndefined();
  });

  it("gives every node a path id", () => {
    expect(planNodeIds(plan)).toEqual(["0", "0.0", "0.1"]);
    expect(countPlanNodes(plan)).toBe(3);
  });
});

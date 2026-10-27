import type { PlanFlag, PlanNode } from "@/shared/api";

/** A plan was run with EXPLAIN ANALYZE when its root carries actual loops. */
export function isAnalyzed(root: PlanNode): boolean {
  return root.actualLoops !== undefined;
}

/** The node's share of the whole plan: measured time when analyzed, else estimated cost. 0..1. */
export function nodeShare(node: PlanNode, analyzed: boolean): number {
  const share = analyzed && node.timeShare !== undefined ? node.timeShare : node.costShare;
  return Math.min(1, Math.max(0, Number.isFinite(share) ? share : 0));
}

/** "Seq Scan on shop.orders o", "Index Scan using orders_pkey on shop.orders", "Hash Join (Left)". */
export function nodeTitle(node: PlanNode): string {
  let title = node.nodeType;
  if (node.joinType && node.joinType !== "Inner") title += ` (${node.joinType})`;
  if (node.index) title += ` using ${node.index}`;
  if (node.relation) {
    title += ` on ${node.relation}`;
    const shortName = node.relation.split(".").pop();
    if (node.alias && node.alias !== shortName) title += ` ${node.alias}`;
  }
  return title;
}

const LEVEL_RANK: Record<PlanFlag["level"], number> = { danger: 0, warn: 1, info: 2 };

export interface PlanFlagSummary extends PlanFlag {
  node: string;
  share: number;
}

/** Every flag in the plan, worst and costliest first, de-duplicated by message. */
export function summarizeFlags(root: PlanNode, limit = 5): PlanFlagSummary[] {
  const analyzed = isAnalyzed(root);
  const out: PlanFlagSummary[] = [];
  const seen = new Set<string>();
  const visit = (node: PlanNode) => {
    for (const flag of node.flags ?? []) {
      if (seen.has(flag.message)) continue;
      seen.add(flag.message);
      out.push({ ...flag, node: nodeTitle(node), share: nodeShare(node, analyzed) });
    }
    for (const child of node.children ?? []) visit(child);
  };
  visit(root);
  out.sort((a, b) => LEVEL_RANK[a.level] - LEVEL_RANK[b.level] || b.share - a.share);
  return out.slice(0, limit);
}

/** Total rows a node produced over all loops (analyze only). */
export function actualTotalRows(node: PlanNode): number | undefined {
  if (node.actualRows === undefined) return undefined;
  return node.actualRows * (node.actualLoops ?? 1);
}

/** Path-like ids ("0", "0.1", "0.1.0") for collapsing nodes in a tree view. */
export function planNodeIds(root: PlanNode): string[] {
  const ids: string[] = [];
  const visit = (node: PlanNode, id: string) => {
    ids.push(id);
    (node.children ?? []).forEach((child, i) => {
      visit(child, `${id}.${i}`);
    });
  };
  visit(root, "0");
  return ids;
}

export function countPlanNodes(root: PlanNode): number {
  return planNodeIds(root).length;
}

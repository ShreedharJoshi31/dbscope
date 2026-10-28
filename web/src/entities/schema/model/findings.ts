import type { Finding, Model, Relation } from "@/shared/api";

const RANK: Record<Finding["level"], number> = { danger: 0, warn: 1, info: 2 };

/** The most serious level among findings, or undefined when there are none. */
export function worstLevel(findings: readonly Finding[] | undefined): Finding["level"] | undefined {
  let worst: Finding["level"] | undefined;
  for (const f of findings ?? []) if (!worst || RANK[f.level] < RANK[worst]) worst = f.level;
  return worst;
}

export interface FindingRow {
  /** "schema.name" of the relation, as the canvas keys it. */
  relation: string;
  finding: Finding;
}

/** Every relation's findings, most serious first, then by relation and rule. */
export function schemaFindings(model: Model | undefined): FindingRow[] {
  const rows: FindingRow[] = [];
  for (const r of model?.relations ?? []) {
    for (const finding of r.findings ?? []) rows.push({ relation: `${r.schema}.${r.name}`, finding });
  }
  return rows.sort(
    (a, b) =>
      RANK[a.finding.level] - RANK[b.finding.level] ||
      a.relation.localeCompare(b.relation) ||
      a.finding.ruleId.localeCompare(b.finding.ruleId),
  );
}

export function findingCount(relation: Relation): number {
  return relation.findings?.length ?? 0;
}

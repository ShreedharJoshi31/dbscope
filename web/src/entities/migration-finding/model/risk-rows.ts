import { ratingRank } from "@/entities/migration-finding/model/rating";
import type { ReviewModel, ReviewStatement } from "@/entities/migration-finding/model/review-model";
import type { Rating } from "@/shared/api";

export type RiskSortKey = "rating" | "rule" | "location" | "relation" | "message";
export type SortDirection = "asc" | "desc";

export interface RiskSort {
  key: RiskSortKey;
  dir: SortDirection;
}

/** Critical first, then file order. */
export const DEFAULT_RISK_SORT: RiskSort = { key: "rating", dir: "desc" };

export interface RiskRow {
  id: string;
  statementIndex: number;
  findingIndex: number;
  rating: Rating;
  ruleId: string;
  file: string;
  fileIndex: number;
  line: number;
  /** The locked relation the finding is about: the first one its message names, else the statement's first lock. */
  relation: string;
  message: string;
  safeAlternative: string | undefined;
}

function findingRelation(statement: ReviewStatement, message: string): string {
  return statement.relations.find((r) => message.includes(r)) ?? statement.relations[0] ?? "";
}

/** One row per finding, in report order. */
export function riskRows(model: Pick<ReviewModel, "statements">): RiskRow[] {
  const rows: RiskRow[] = [];
  for (const s of model.statements) {
    s.findings.forEach((f, findingIndex) => {
      rows.push({
        id: `${s.index}:${findingIndex}`,
        statementIndex: s.index,
        findingIndex,
        rating: f.rating,
        ruleId: f.ruleId,
        file: s.file,
        fileIndex: s.fileIndex,
        line: s.line,
        relation: findingRelation(s, f.message),
        message: f.message,
        safeAlternative: f.safeAlternative,
      });
    });
  }
  return rows;
}

const text = new Intl.Collator("en", { numeric: true, sensitivity: "base" });

function byLocation(a: RiskRow, b: RiskRow): number {
  return (
    a.fileIndex - b.fileIndex ||
    a.line - b.line ||
    a.statementIndex - b.statementIndex ||
    a.findingIndex - b.findingIndex
  );
}

const COMPARE: Record<RiskSortKey, (a: RiskRow, b: RiskRow) => number> = {
  rating: (a, b) => ratingRank(a.rating) - ratingRank(b.rating),
  rule: (a, b) => text.compare(a.ruleId, b.ruleId),
  location: byLocation,
  // Findings without a relation sort last in either direction would surprise; they sort as "".
  relation: (a, b) => text.compare(a.relation, b.relation),
  message: (a, b) => text.compare(a.message, b.message),
};

/** Sorts by one column; ties fall back to file order, so the list never reshuffles between renders. */
export function sortRiskRows(rows: readonly RiskRow[], sort: RiskSort): RiskRow[] {
  const sign = sort.dir === "asc" ? 1 : -1;
  const primary = COMPARE[sort.key];
  return [...rows].sort((a, b) => sign * primary(a, b) || byLocation(a, b));
}

/** Clicking a sorted header flips it; another header starts with its natural direction. */
export function nextRiskSort(current: RiskSort, key: RiskSortKey): RiskSort {
  if (current.key === key) return { key, dir: current.dir === "asc" ? "desc" : "asc" };
  return { key, dir: key === "rating" ? "desc" : "asc" };
}

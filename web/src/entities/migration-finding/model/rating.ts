import type { Rating } from "@/shared/api";

/** Worst first: the order counts, lists and the Markdown summary use. */
export const RATINGS: readonly Rating[] = ["critical", "high", "medium", "low"];

const RANK: Record<Rating, number> = { low: 0, medium: 1, high: 2, critical: 3 };

export function ratingRank(rating: Rating): number {
  return RANK[rating] ?? 0;
}

export type RatingTone = "info" | "warn" | "danger";
export type RatingShape = "circle" | "triangle" | "octagon" | "octagon-filled";

export interface RatingMeta {
  label: string;
  tone: RatingTone;
  /** docs/ARCHITECTURE.md: Low=info circle, Medium=warn triangle, High=danger octagon, Critical=filled octagon with bar. */
  shape: RatingShape;
  /** What the rating means, for tooltips and screen readers. */
  description: string;
}

export const RATING_META: Record<Rating, RatingMeta> = {
  low: { label: "Low", tone: "info", shape: "circle", description: "Low risk: safe to run as written" },
  medium: {
    label: "Medium",
    tone: "warn",
    shape: "triangle",
    description: "Medium risk: brief blocking or a fixable problem",
  },
  high: {
    label: "High",
    tone: "danger",
    shape: "octagon",
    description: "High risk: likely to block traffic or break clients",
  },
  critical: {
    label: "Critical",
    tone: "danger",
    shape: "octagon-filled",
    description: "Critical: will fail, lose data or cause a long outage",
  },
};

export function ratingMeta(rating: Rating): RatingMeta {
  return RATING_META[rating] ?? RATING_META.low;
}

export function worstRating(ratings: Iterable<Rating>): Rating {
  let worst: Rating = "low";
  for (const r of ratings) if (ratingRank(r) > ratingRank(worst)) worst = r;
  return worst;
}

/** "SQLite 3.54.1" for 3054001. */
export function sqliteVersionLabel(versionNum: number): string {
  if (!Number.isFinite(versionNum) || versionNum <= 0) return "SQLite (unknown version)";
  return `SQLite ${Math.floor(versionNum / 1_000_000)}.${Math.floor(versionNum / 1000) % 1000}.${versionNum % 1000}`;
}

/** "MySQL 8.4.13" for 80413, or "MariaDB 11.8.9". */
export function mysqlVersionLabel(versionNum: number, flavor: "mysql" | "mariadb"): string {
  const name = flavor === "mariadb" ? "MariaDB" : "MySQL";
  if (!Number.isFinite(versionNum) || versionNum <= 0) return `${name} (unknown version)`;
  return `${name} ${Math.floor(versionNum / 10000)}.${Math.floor(versionNum / 100) % 100}.${versionNum % 100}`;
}

/** "PostgreSQL 17" for 170000, "PostgreSQL 17.2" for 170002. */
export function pgVersionLabel(versionNum: number): string {
  if (!Number.isFinite(versionNum) || versionNum <= 0) return "PostgreSQL (unknown version)";
  const major = Math.floor(versionNum / 10000);
  const minor = versionNum % 10000;
  return minor > 0 ? `PostgreSQL ${major}.${minor}` : `PostgreSQL ${major}`;
}

export function pgMajor(versionNum: number): number {
  return Math.floor(versionNum / 10000);
}

import type { Engine } from "@/shared/api";

export const ENGINE_ORDER: readonly Engine[] = ["postgres", "sqlite"];

export const ENGINE_META: Record<Engine, { label: string; description: string }> = {
  postgres: {
    label: "PostgreSQL",
    description: "ERD, queries, questions in plain English, migration risk and workload.",
  },
  sqlite: {
    label: "SQLite",
    description: "A database file on this machine, opened read-only: ERD and queries.",
  },
};

/** The engine's name for display, e.g. "PostgreSQL"; results without an engine are PostgreSQL. */
export function engineLabel(engine: Engine | undefined): string {
  return ENGINE_META[engine ?? "postgres"].label;
}

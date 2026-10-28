import type { Engine, TestResult } from "@/shared/api";

export const ENGINE_ORDER: readonly Engine[] = ["postgres", "mysql", "sqlite"];

export const ENGINE_META: Record<Engine, { label: string; description: string; defaultPort?: number }> = {
  postgres: {
    label: "PostgreSQL",
    description: "ERD, queries, questions in plain English, migration risk and workload.",
    defaultPort: 5432,
  },
  mysql: {
    label: "MySQL / MariaDB",
    description: "ERD and read-only queries. Migration risk for MySQL is coming.",
    defaultPort: 3306,
  },
  sqlite: {
    label: "SQLite",
    description: "A database file on this machine, opened read-only: ERD, queries and migration checks.",
  },
};

/** The name to show for an engine or a test result's server: "PostgreSQL", "MySQL", "MariaDB". */
export function engineLabel(engine: TestResult["engine"]): string {
  switch (engine) {
    case "mysql":
      return "MySQL";
    case "mariadb":
      return "MariaDB";
    case undefined:
      return "PostgreSQL";
  }
  return ENGINE_META[engine].label;
}

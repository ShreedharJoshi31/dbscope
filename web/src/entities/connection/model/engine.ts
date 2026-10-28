import type { Engine, TestResult } from "@/shared/api";

export const ENGINE_ORDER: readonly Engine[] = ["postgres", "mysql", "sqlite", "mongodb", "redis", "dynamodb"];

export const ENGINE_META: Record<Engine, { label: string; description: string; defaultPort?: number }> = {
  postgres: {
    label: "PostgreSQL",
    description: "ERD, queries, questions in plain English, migration risk and workload.",
    defaultPort: 5432,
  },
  mysql: {
    label: "MySQL",
    description: "MySQL or MariaDB: ERD, read-only queries and online DDL migration risk.",
    defaultPort: 3306,
  },
  mongodb: {
    label: "MongoDB",
    description:
      "A schema inferred from sampled documents, with likely references, and read-only queries as JSON commands.",
    defaultPort: 27017,
  },
  redis: {
    label: "Redis",
    description:
      "Redis or Valkey: a map of key patterns with memory and expiry, big keys and server risks, read without touching values.",
    defaultPort: 6379,
  },
  dynamodb: {
    label: "DynamoDB",
    description:
      "Tables, keys and indexes with attributes from a small sample, and PartiQL SELECTs whose plan says when they scan. Uses your AWS credentials.",
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
    case "redis":
      return "Redis";
    case "valkey":
      return "Valkey";
    case undefined:
      return "PostgreSQL";
  }
  return ENGINE_META[engine].label;
}

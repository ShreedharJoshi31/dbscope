import type { Engine, TestResult } from "@/shared/api";

export const ENGINE_ORDER: readonly Engine[] = [
  "postgres",
  "mysql",
  "sqlite",
  "mongodb",
  "redis",
  "dynamodb",
  "cassandra",
  "neo4j",
  "elasticsearch",
  "clickhouse",
];

export interface EngineMeta {
  label: string;
  description: string;
  defaultPort?: number;
  /** What a query tab takes, for copy such as "Write SQL in a query tab". */
  queryLanguage?: string;
  /** Query tabs' file extension; sql when unset. */
  fileExtension?: string;
}

export const ENGINE_META: Record<Engine, EngineMeta> = {
  postgres: {
    queryLanguage: "SQL",
    label: "PostgreSQL",
    description: "ERD, queries, questions in plain English, migration risk and workload.",
    defaultPort: 5432,
  },
  mysql: {
    queryLanguage: "SQL",
    label: "MySQL",
    description: "MySQL or MariaDB: ERD, read-only queries and online DDL migration risk.",
    defaultPort: 3306,
  },
  mongodb: {
    fileExtension: "json",
    queryLanguage: "a JSON command",
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
    queryLanguage: "PartiQL",
    label: "DynamoDB",
    description:
      "Tables, keys and indexes with attributes from a small sample, and PartiQL SELECTs whose plan says when they scan. Uses your AWS credentials.",
  },
  cassandra: {
    fileExtension: "cql",
    queryLanguage: "CQL",
    label: "Cassandra",
    description:
      "Cassandra or ScyllaDB: tables with partition and clustering keys, indexes by kind, and read-only CQL whose plan says when a query scans the cluster.",
    defaultPort: 9042,
  },
  neo4j: {
    fileExtension: "cypher",
    queryLanguage: "Cypher",
    label: "Neo4j",
    description:
      "A map of node labels and the relationships between them, and read-only Cypher whose plan flags full scans, cartesian products and unbounded paths.",
    defaultPort: 7687,
  },
  elasticsearch: {
    fileExtension: "json",
    queryLanguage: "a search request as JSON, or SQL",
    label: "Elasticsearch",
    description:
      "Elasticsearch or OpenSearch: indices with their mapped fields, shards and mapping risks, fields an alias maps differently, and read-only searches and SQL.",
    defaultPort: 9200,
  },
  clickhouse: {
    queryLanguage: "SQL",
    label: "ClickHouse",
    description:
      "Tables with their engines, sorting and partition keys, parts and design risks, and read-only SQL whose plan shows how much the sorting key prunes.",
    defaultPort: 9000,
  },
  sqlite: {
    queryLanguage: "SQL",
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
    case "scylladb":
      return "ScyllaDB";
    case "opensearch":
      return "OpenSearch";
    case undefined:
      return "PostgreSQL";
  }
  return ENGINE_META[engine].label;
}

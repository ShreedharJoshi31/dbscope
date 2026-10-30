import type { Engine } from "@/shared/api";

/** What the paste form calls a migration and shows as an example, by the engine it is checked against. */
export interface PasteCopy {
  title: string;
  field: string;
  placeholder: string;
  nameHelp: string;
  defaultName: string;
}

const SQL_NAMES = "e.g. V42__orders.sql (Flyway) or 0042_orders.up.sql (golang-migrate)";

const SQL: PasteCopy = {
  title: "Paste migration SQL",
  field: "Migration SQL",
  placeholder: "ALTER TABLE shop.orders ADD COLUMN note text;",
  nameHelp: `The name picks the migration tool's rules, ${SQL_NAMES}.`,
  defaultName: "pasted.sql",
};

const BY_ENGINE: Partial<Record<Engine, PasteCopy>> = {
  mysql: { ...SQL, placeholder: "ALTER TABLE orders ADD COLUMN note TEXT, ALGORITHM=INSTANT;" },
  sqlite: { ...SQL, placeholder: "ALTER TABLE notes ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;" },
  clickhouse: { ...SQL, placeholder: "ALTER TABLE events ADD COLUMN country LowCardinality(String);" },
  cassandra: {
    title: "Paste a CQL migration",
    field: "Migration CQL",
    placeholder: "ALTER TABLE shop.orders ADD note text;",
    nameHelp: "The name orders the files, e.g. 0042_orders.cql.",
    defaultName: "pasted.cql",
  },
  mongodb: {
    title: "Paste a MongoDB migration",
    field: "Migration script (JavaScript)",
    placeholder: "db.users.createIndex({ email: 1 }, { unique: true });",
    nameHelp: "A mongosh script or a migrate-mongo file, e.g. 20261030-users-email.js.",
    defaultName: "pasted.js",
  },
  neo4j: {
    title: "Paste a Cypher migration",
    field: "Migration Cypher",
    placeholder: "CREATE CONSTRAINT person_email FOR (p:Person) REQUIRE p.email IS UNIQUE;",
    nameHelp: "The name orders the files, e.g. V3__people.cypher (neo4j-migrations).",
    defaultName: "pasted.cypher",
  },
  elasticsearch: {
    title: "Paste migration requests",
    field: "Requests (Kibana console form)",
    placeholder: 'PUT /orders/_mapping\n{ "properties": { "note": { "type": "text" } } }',
    nameHelp: "The name orders the files, e.g. V1__orders.http (elasticsearch-evolution).",
    defaultName: "pasted.http",
  },
};

export function pasteCopy(engine: Engine | undefined): PasteCopy {
  return (engine && BY_ENGINE[engine]) || SQL;
}

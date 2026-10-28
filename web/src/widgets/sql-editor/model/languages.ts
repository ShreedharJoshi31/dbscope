import type { Engine, Model } from "@/shared/api";

/** The editor language for an engine's query tabs. */
export type EditorLanguage =
  | "postgres"
  | "mysql"
  | "sqlite"
  | "clickhouse"
  | "tsql"
  | "partiql"
  | "cql"
  | "cypher"
  | "json"
  | "json-or-sql";

const LANGUAGE: Record<Engine, EditorLanguage> = {
  postgres: "postgres",
  mysql: "mysql",
  sqlite: "sqlite",
  clickhouse: "clickhouse",
  sqlserver: "tsql",
  dynamodb: "partiql",
  cassandra: "cql",
  neo4j: "cypher",
  mongodb: "json",
  elasticsearch: "json-or-sql",
  redis: "postgres", // no query tab: Redis connections show the keyspace report
};

/** The sample workspace and an unknown engine are PostgreSQL. */
export function languageFor(engine: Engine | undefined): EditorLanguage {
  return engine ? LANGUAGE[engine] : "postgres";
}

/** Relation and field names, for languages whose completion is not SQL's schema completion. */
export interface Names {
  relations: string[];
  fields: string[];
}

export function namesOf(model: Model | undefined): Names {
  const relations = new Set<string>();
  const fields = new Set<string>();
  for (const r of model?.relations ?? []) {
    if (r.stub) continue;
    relations.add(r.name);
    for (const c of r.columns ?? []) {
      // Neo4j's relationship rows ("-[:T]-> Label") are not properties.
      if (!c.name.startsWith("-[:")) fields.add(c.name);
    }
    for (const fk of r.foreignKeys ?? []) {
      const type = fk.columns[0]?.match(/^-\[:(.+)\]->/)?.[1];
      if (type) relations.add(type);
    }
  }
  return { relations: [...relations].sort(), fields: [...fields].sort() };
}

/** An example query for an empty tab, naming a real relation when the schema has one. */
export function exampleQuery(language: EditorLanguage, model: Model | undefined): string {
  const first = (model?.relations ?? []).find((r) => !r.stub && r.kind !== "view");
  const name = first?.name ?? "table";
  const column = first?.columns?.find((c) => !c.name.startsWith("-[:"))?.name ?? "field";
  switch (language) {
    case "cypher":
      return `MATCH (n:${name}) RETURN n.${column} LIMIT 25`;
    case "json":
      return `{"find": "${name}", "filter": {}, "limit": 20}`;
    case "json-or-sql":
      return `{"index": "${name}", "query": {"match": {"${column}": "text"}}}  or  SELECT * FROM "${name}" LIMIT 20`;
    case "cql":
      return `SELECT * FROM ${name} WHERE ${first?.primaryKey?.[0] ?? "id"} = ? LIMIT 20`;
    case "partiql":
      return `SELECT * FROM "${name}" WHERE ${first?.primaryKey?.[0] ?? "pk"} = 'value'`;
    case "tsql":
      return `SELECT TOP 20 * FROM ${first ? `${first.schema}.${name}` : name}`;
    default:
      return `SELECT * FROM ${name} LIMIT 20`;
  }
}

/** Starts with "{": a search request; otherwise SQL (Elasticsearch takes both). */
export function looksLikeJson(text: string): boolean {
  return text.trimStart().startsWith("{");
}

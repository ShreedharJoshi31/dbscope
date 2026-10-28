import { describe, expect, it } from "vitest";
import type { Model } from "@/shared/api";
import { exampleQuery, languageFor, namesOf } from "@/widgets/sql-editor/model/languages";

const graph = {
  serverVersion: 0,
  schemas: ["neo4j"],
  enums: [],
  relations: [
    {
      schema: "neo4j",
      name: "Person",
      kind: "node_label",
      rowEstimate: 3,
      pages: 0,
      columns: [
        { name: "name", type: "string", nullable: false },
        { name: "-[:KNOWS]-> Person", type: "Person", nullable: true },
      ],
      foreignKeys: [
        {
          name: "KNOWS to Person",
          columns: ["-[:KNOWS]-> Person"],
          ref: { schema: "neo4j", name: "Person" },
          refColumns: [""],
          validated: true,
        },
      ],
    },
  ],
} as unknown as Model;

describe("editor languages", () => {
  it("maps engines to languages", () => {
    expect(languageFor("neo4j")).toBe("cypher");
    expect(languageFor("mongodb")).toBe("json");
    expect(languageFor("elasticsearch")).toBe("json-or-sql");
    expect(languageFor(undefined)).toBe("postgres");
  });

  it("offers labels, relationship types and properties, not relationship rows", () => {
    expect(namesOf(graph)).toEqual({ relations: ["KNOWS", "Person"], fields: ["name"] });
  });

  it("writes an example with a real name", () => {
    expect(exampleQuery("cypher", graph)).toBe("MATCH (n:Person) RETURN n.name LIMIT 25");
    expect(exampleQuery("json", undefined)).toBe('{"find": "table", "filter": {}, "limit": 20}');
  });
});

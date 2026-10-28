import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";

const graph = {
  serverVersion: 20261001,
  schemas: ["neo4j"],
  enums: [],
  introspectedAt: "2026-10-28T10:00:00Z",
  durationMs: 5,
  relations: [
    {
      schema: "neo4j",
      name: "Person",
      kind: "node_label",
      rowEstimate: 300,
      pages: 0,
      columns: [
        { name: "name", type: "string", nullable: false },
        { name: "-[:FOLLOWS]-> Person", type: "Person", nullable: true },
      ],
      foreignKeys: [
        {
          name: "FOLLOWS to Person",
          columns: ["-[:FOLLOWS]-> Person"],
          ref: { schema: "neo4j", name: "Person" },
          refColumns: [""],
          validated: true,
        },
      ],
    },
  ],
};

test("a Neo4j tab speaks Cypher: its name, its example and its completion", async ({ page }) => {
  await stubAgent(page, {
    profiles: [
      stubProfile({
        engine: "neo4j",
        port: 7687,
        database: "neo4j",
        capabilities: { query: true, ask: false, migrations: false, workload: false },
      }),
    ],
    schema: graph,
  });
  await openApp(page);
  await page.getByRole("button", { name: /^New SQL tab/ }).click();
  await expect(page.getByRole("tab", { name: "query-1.cypher" })).toBeVisible();
  const editor = page.getByRole("textbox", { name: "Query editor, query-1.cypher" });
  await expect(page.locator(".cm-placeholder")).toContainText("MATCH (n:Person) RETURN n.name LIMIT 25");

  await editor.click();
  await page.keyboard.type("FOREA");
  await page.keyboard.press("ControlOrMeta+Space");
  await expect(page.locator(".cm-tooltip-autocomplete")).toHaveCount(0);

  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type("MATCH (p:Pe");
  await expect(page.locator(".cm-tooltip-autocomplete")).toContainText("Person");
  await page.keyboard.press("Enter");
  await page.keyboard.type(")-[:FO");
  await expect(page.locator(".cm-tooltip-autocomplete")).toContainText("FOLLOWS");
});

test("a MongoDB tab takes JSON and completes collection names inside strings", async ({ page }) => {
  const docs = {
    ...graph,
    schemas: ["shop"],
    relations: [
      {
        schema: "shop",
        name: "orders",
        kind: "collection",
        rowEstimate: 3,
        pages: 0,
        columns: [{ name: "status", type: "string", nullable: true }],
      },
    ],
  };
  await stubAgent(page, {
    profiles: [
      stubProfile({
        engine: "mongodb",
        port: 27017,
        database: "shop",
        capabilities: { query: true, ask: false, migrations: false, workload: false },
      }),
    ],
    schema: docs,
  });
  await openApp(page);
  await page.getByRole("button", { name: /^New SQL tab/ }).click();
  await expect(page.getByRole("tab", { name: "query-1.json" })).toBeVisible();
  await expect(page.locator(".cm-placeholder")).toContainText('{"find": "orders", "filter": {}, "limit": 20}');
  await page.getByRole("textbox", { name: "Query editor, query-1.json" }).click();
  await page.keyboard.type('{"find": "or');
  await expect(page.locator(".cm-tooltip-autocomplete")).toContainText("orders");
});

test("an Elasticsearch tab switches between SQL and a JSON search request as you type", async ({ page }) => {
  const indices = {
    ...graph,
    schemas: ["indices"],
    relations: [
      {
        schema: "indices",
        name: "logs",
        kind: "search_index",
        rowEstimate: 3,
        pages: 0,
        columns: [{ name: "message", type: "text", nullable: true }],
      },
    ],
  };
  await stubAgent(page, {
    profiles: [
      stubProfile({
        engine: "elasticsearch",
        port: 9200,
        database: "*",
        capabilities: { query: true, ask: false, migrations: false, workload: false },
      }),
    ],
    schema: indices,
  });
  await openApp(page);
  await page.getByRole("button", { name: /^New SQL tab/ }).click();
  await page.getByRole("textbox", { name: "Query editor, query-1.json" }).click();
  await page.keyboard.type("SELECT * FR");
  await expect(page.locator(".cm-tooltip-autocomplete")).toContainText("FROM");
  await page.keyboard.press("Escape");
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type('{"index": "lo');
  await expect(page.locator(".cm-tooltip-autocomplete")).toContainText("logs");
});

test("a PostgreSQL tab stays query-N.sql with the SQL editor", async ({ page }) => {
  await stubAgent(page, { profiles: [stubProfile()] });
  await openApp(page);
  await page.getByRole("button", { name: /^New SQL tab/ }).click();
  await expect(page.getByRole("textbox", { name: "SQL editor, query-1.sql" })).toBeVisible();
});

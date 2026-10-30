import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { newSqlTab } from "./support/query";

const person = (id: string, name: string) => ({ id, labels: ["Person"], props: { name, age: 30 } });
const company = { id: "4:db:9", labels: ["Company"], props: { name: "Acme" } };

const result = {
  columns: [{ name: "p", type: "", typeOid: 0 }],
  rows: [['{"labels":["Person"],"name":"Ann"}']],
  truncated: false,
  rowCount: 1,
  durationMs: 3,
  executedSql: "MATCH p = (:Person)-[:KNOWS]->(:Person) RETURN p",
  plan: {
    nodeType: "Query",
    stepsOnly: true,
    startupCost: 0,
    totalCost: 0,
    planRows: 0,
    costShare: 0,
    flags: [],
    children: [],
  },
  overlay: { relations: {}, joins: [] },
  warnings: [],
  graph: {
    nodes: [person("4:db:1", "Ann"), person("4:db:2", "Bob")],
    relationships: [{ id: "5:db:1", type: "KNOWS", start: "4:db:1", end: "4:db:2", props: {} }],
    truncated: false,
  },
};

test("a Cypher result is drawn as a graph, and a double-click expands a node", async ({ page }) => {
  await stubAgent(page, {
    profiles: [
      stubProfile({
        engine: "neo4j",
        port: 7687,
        database: "neo4j",
        capabilities: { query: true, ask: false, migrations: false, workload: false, health: true, graph: true },
      }),
    ],
    schema: {
      serverVersion: 20261001,
      schemas: ["neo4j"],
      enums: [],
      relations: [],
      introspectedAt: "2026-10-30T10:00:00Z",
      durationMs: 1,
    },
  });
  const expansions: unknown[] = [];
  await page.route("**/api/profiles/*/query", (route) => route.fulfill({ json: result }));
  await page.route("**/api/profiles/*/graph/expand", async (route) => {
    expansions.push(route.request().postDataJSON());
    await route.fulfill({
      json: {
        degree: 1200,
        shown: 1,
        graph: {
          nodes: [person("4:db:2", "Bob"), company],
          relationships: [{ id: "5:db:7", type: "WORKS_AT", start: "4:db:2", end: "4:db:9", props: { since: 2020 } }],
          truncated: false,
        },
      },
    });
  });
  await openApp(page);
  await page.getByRole("button", { name: /^New SQL tab/ }).click();
  await page.getByRole("textbox", { name: /Query editor/ }).click();
  await page.keyboard.type("MATCH p = (:Person)-[:KNOWS]->(:Person) RETURN p");
  await page.keyboard.press("ControlOrMeta+Enter");

  const view = page.getByTestId("graph-view");
  await expect(page.getByRole("tab", { name: "Graph" })).toHaveAttribute("aria-selected", "true");
  await expect(view).toContainText("2 nodes, 1 relationships");
  await expect(view.getByText("Ann")).toBeVisible();
  await expect(view.getByText("KNOWS")).toBeVisible();

  await view.getByText("Bob").click();
  const details = page.getByRole("complementary", { name: "Node details" });
  await expect(details).toContainText("age");
  await view.getByText("Bob").dblclick();
  await expect(view).toContainText("Showing 1 of 1,200 relationships of this node; 1 new nodes.");
  await expect(view).toContainText("3 nodes, 2 relationships");
  await expect(view.getByText("Acme")).toBeVisible();
  expect(expansions[0]).toEqual({ nodeId: "4:db:2", limit: 50 });

  await page.getByRole("tab", { name: "Results" }).click();
  await expect(page.getByTestId("result-summary")).toContainText("1 rows");
});

test("an Apache AGE result in PostgreSQL gets the Graph tab and expands within its graph", async ({ page }) => {
  await stubAgent(page, { profiles: [stubProfile({})] });
  const expansions: unknown[] = [];
  await page.route("**/api/profiles/*/query", (route) =>
    route.fulfill({ json: { ...result, graph: { ...result.graph, source: "social" } } }),
  );
  await page.route("**/api/profiles/*/graph/expand", async (route) => {
    expansions.push(route.request().postDataJSON());
    await route.fulfill({
      json: { degree: 1, shown: 1, graph: { nodes: [person("4:db:2", "Bob")], relationships: [], truncated: false } },
    });
  });
  await openApp(page);
  await expect(page.getByRole("tab", { name: "Graph" })).toHaveCount(0);
  await newSqlTab(page, "SELECT * FROM ag_catalog.cypher('social', $$ MATCH p = ()-[]->() RETURN p $$) AS (p agtype)");
  await page.keyboard.press("ControlOrMeta+Enter");
  await expect(page.getByRole("tab", { name: "Graph" })).toHaveAttribute("aria-selected", "true");
  await page.getByTestId("graph-view").getByText("Bob").dblclick();
  await expect(page.getByTestId("graph-view")).toContainText("All 1 relationships shown");
  expect(expansions[0]).toEqual({ nodeId: "4:db:2", limit: 50, graph: "social" });
});

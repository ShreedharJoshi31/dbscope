import { expect, type Page, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { waitForCanvas } from "./support/canvas";
import { dock, newSqlTab } from "./support/query";
import type { StubModel } from "./support/schema-stub";

// A fixed zone, so the local times typed and shown are known.
test.use({ timezoneId: "Asia/Kolkata" });

const ORACLE_ID = "01JB00000000000000000000OR";
const MYSQL_ID = "01JB00000000000000000000MY";
const POSTGRES_ID = "01JB00000000000000000000AA";

/** Mirrors schema.Model.TimeTravel, which StubModel leaves out. */
type TimeTravelModel = StubModel & { timeTravel?: { supported: boolean; note?: string } };

const SERVER_NOW = "2026-11-15T04:30:00Z"; // 10:00:00 in Asia/Kolkata
const OLDEST = "2026-11-15T04:15:00Z"; // 09:45:00, 900 s of assumed undo retention

const ORACLE = stubProfile({
  id: ORACLE_ID,
  name: "ledger-ora",
  engine: "oracle",
  port: 1521,
  database: "FREEPDB1",
  user: "TT",
  capabilities: { query: true, ask: false, migrations: false, workload: false, timeTravel: true },
});

const MYSQL = stubProfile({
  id: MYSQL_ID,
  name: "shop-mysql",
  engine: "mysql",
  port: 3306,
  capabilities: { query: true, ask: false, migrations: true, workload: false, timeTravel: true },
});

function model(relations: StubModel["relations"], timeTravel?: TimeTravelModel["timeTravel"]): TimeTravelModel {
  return {
    serverVersion: 1,
    schemas: ["TT"],
    relations,
    enums: [],
    introspectedAt: "2026-11-15T04:00:00Z",
    durationMs: 5,
    ...(timeTravel ? { timeTravel } : {}),
  };
}

const columns = [
  { name: "ID", type: "NUMBER", nullable: false },
  { name: "BAL", type: "NUMBER", nullable: true },
];

const ORACLE_MODEL = model(
  [
    {
      schema: "TT",
      name: "ACCT",
      kind: "table",
      rowEstimate: 2,
      pages: 1,
      columns,
      primaryKey: ["ID"],
      timeTravel: { kind: "flashback", available: true },
    },
    { schema: "TT", name: "PLAIN", kind: "table", rowEstimate: 2, pages: 1, columns, primaryKey: ["ID"] },
  ],
  { supported: true },
);

const MYSQL_NOTE = "MySQL has no system-versioned tables; MariaDB does (WITH SYSTEM VERSIONING)";
const MYSQL_MODEL = model(
  [{ schema: "shop", name: "orders", kind: "table", rowEstimate: 2, pages: 1, columns, primaryKey: ["ID"] }],
  { supported: false, note: MYSQL_NOTE },
);

/** GET /time-travel as the agent answers it on Oracle without SELECT_CATALOG_ROLE. */
async function stubBounds(page: Page) {
  const requests: string[][] = [];
  await page.route(/\/api\/profiles\/[^/]+\/time-travel\?/, async (route) => {
    const tables = (new URL(route.request().url()).searchParams.get("tables") ?? "").split(",");
    requests.push(tables);
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        now: SERVER_NOW,
        tables: tables.map((table) =>
          table === "TT.ACCT"
            ? { table, kind: "flashback", available: true, oldest: OLDEST, source: "assumed_default" }
            : { table, kind: null, available: false, oldest: null, source: "none", note: "Not a table" },
        ),
        note: "Undo retention is a target, not a promise",
      }),
    });
  });
  return requests;
}

const asOfButton = (page: Page) => page.getByRole("button", { name: /^As of/ });
const picker = (page: Page) => page.getByRole("dialog", { name: "Read as of a past time" });

async function connect(page: Page, name: string) {
  await page.getByRole("button", { name: /^Connection: / }).click();
  await page.getByRole("menuitemradio", { name: new RegExp(name) }).click();
  await expect(page.getByRole("button", { name: new RegExp(`^Connection: ${name}`) })).toBeVisible();
}

test.describe("as of time picker", () => {
  test("is hidden for a PostgreSQL connection", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await waitForCanvas(page);
    await newSqlTab(page, "SELECT * FROM shop.orders");
    await expect(page.getByRole("button", { name: "Run", exact: true })).toBeVisible();
    await expect(asOfButton(page)).toHaveCount(0);
  });

  test("on Oracle: bounds, range checks, the chip, asOf on Run and Explain, Clear", async ({ page }) => {
    test.slow(); // several runs, a reload and two connection switches
    const { phase23 } = await stubAgent(page, {
      profiles: [ORACLE, stubProfile({ id: POSTGRES_ID, name: "orders-dev" })],
      schemasByProfile: { [ORACLE_ID]: ORACLE_MODEL as StubModel },
    });
    const boundsRequests = await stubBounds(page);
    await openApp(page);
    await expect(page.getByRole("button", { name: /^Connection: ledger-ora/ })).toBeVisible();
    // Ends in a number so no completion list opens over the popover.
    const sql = "SELECT a.ID, a.BAL FROM tt.acct a JOIN TT.PLAIN p ON p.ID = a.ID WHERE a.BAL > 0";
    await newSqlTab(page, sql);

    // Keyboard: the trigger opens the popover; Esc closes it and focus goes back.
    await asOfButton(page).focus();
    await page.keyboard.press("Enter");
    await expect(picker(page)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(picker(page)).toHaveCount(0);
    await expect(asOfButton(page)).toBeFocused();

    await asOfButton(page).click();
    const dialog = picker(page);
    const tables = dialog.getByTestId("as-of-tables");
    await expect(tables.locator("tbody tr")).toHaveCount(1);
    const acct = tables.locator('tr[data-table="TT.ACCT"]');
    await expect(acct).toContainText("Flashback query");
    await expect(acct).toContainText("Available");
    await expect(acct).toContainText("about 15 minutes, assumed: undo retention could not be read");
    await expect(dialog).toContainText("Undo retention is a target, not a promise");
    // Only the versioned table the SQL names is asked about.
    expect(boundsRequests.at(-1)).toEqual(["TT.ACCT"]);

    const input = dialog.getByLabel(/^Date and time/);
    await expect(input).toHaveAttribute("min", "2026-11-15T09:45:00");
    await expect(input).toHaveAttribute("max", "2026-11-15T10:00:00");
    await expect(input).toHaveAttribute("step", "1");
    const apply = dialog.getByRole("button", { name: "Apply" });

    await input.fill("2026-11-15T09:40:10");
    await expect(dialog.getByRole("alert")).toContainText("Choose 2026-11-15 09:45:00 GMT+5:30 or later");
    await expect(input).toHaveAttribute("aria-invalid", "true");
    await expect(apply).toBeDisabled();
    await input.fill("2026-11-15T10:05:10");
    await expect(dialog.getByRole("alert")).toContainText("Choose 2026-11-15 10:00:00 GMT+5:30 or earlier");
    await expect(apply).toBeDisabled();

    await dialog.getByRole("button", { name: "Now" }).click();
    await expect(input).toHaveValue(/^2026-11-15T10:00(:00)?$/);
    await input.fill("2026-11-15T09:50:30");
    await expect(dialog.getByRole("alert")).toHaveCount(0);
    await apply.click();
    await expect(picker(page)).toHaveCount(0);

    const chip = page.getByTestId("as-of-chip");
    await expect(chip).toContainText("As of 2026-11-15 09:50:30 GMT+5:30");
    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect(dock(page).getByTestId("result-summary")).toContainText("as of 2026-11-15 09:50:30 GMT+5:30");
    expect(phase23.queries.at(-1)).toMatchObject({ sql, asOf: "2026-11-15T04:20:30Z" });
    await page.getByRole("button", { name: "Explain", exact: true }).click();
    await expect(page.getByTestId("run-status")).toContainText("Plan ready, as of 2026-11-15 09:50:30 GMT+5:30");
    expect(phase23.explains.at(-1)).toMatchObject({ sql, asOf: "2026-11-15T04:20:30Z" });

    // The tab keeps it across a reload.
    await openApp(page);
    await expect(page.getByTestId("as-of-chip")).toContainText("As of 2026-11-15 09:50:30 GMT+5:30");

    // Clear in the popover: the chip goes and the next run reads current data.
    await asOfButton(page).click();
    await expect(picker(page).getByLabel(/^Date and time/)).toHaveValue("2026-11-15T09:50:30");
    await picker(page).getByRole("button", { name: "Clear" }).click();
    await expect(page.getByTestId("as-of-chip")).toHaveCount(0);
    const before = phase23.queries.length;
    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect.poll(() => phase23.queries.length).toBe(before + 1);
    expect(phase23.queries.at(-1)).not.toHaveProperty("asOf");
    await expect(dock(page).getByTestId("result-summary")).not.toContainText("as of");

    // The chip's x clears it too.
    await asOfButton(page).click();
    await picker(page)
      .getByLabel(/^Date and time/)
      .fill("2026-11-15T09:55:10");
    await picker(page).getByRole("button", { name: "Apply" }).click();
    await page.getByRole("button", { name: "Clear the as of time and read current data" }).click();
    await expect(page.getByTestId("as-of-chip")).toHaveCount(0);
    await expect(asOfButton(page)).toBeFocused();

    // Switching connections clears it.
    await asOfButton(page).click();
    await picker(page)
      .getByLabel(/^Date and time/)
      .fill("2026-11-15T09:55:10");
    await picker(page).getByRole("button", { name: "Apply" }).click();
    await expect(page.getByTestId("as-of-chip")).toBeVisible();
    await connect(page, "orders-dev");
    await connect(page, "ledger-ora");
    await expect(page.getByRole("tab", { name: "query-1.sql" })).toBeVisible();
    await page.getByRole("tab", { name: "query-1.sql" }).click();
    await expect(asOfButton(page)).toHaveText("As of");
    await expect(page.getByTestId("as-of-chip")).toHaveCount(0);
  });

  test("on MySQL the popover only says there is no system versioning", async ({ page }) => {
    await stubAgent(page, { profiles: [MYSQL], schemasByProfile: { [MYSQL_ID]: MYSQL_MODEL as StubModel } });
    const boundsRequests = await stubBounds(page);
    await openApp(page);
    await expect(page.getByRole("button", { name: /^Connection: shop-mysql/ })).toBeVisible();
    await newSqlTab(page, "SELECT * FROM shop.orders");
    await asOfButton(page).click();
    const dialog = picker(page);
    await expect(dialog.getByTestId("as-of-unsupported")).toHaveText(MYSQL_NOTE);
    await expect(dialog.getByLabel(/Date and time/)).toHaveCount(0);
    await expect(dialog.getByRole("button", { name: "Apply" })).toHaveCount(0);
    expect(boundsRequests).toEqual([]);
    await page.keyboard.press("Escape");
    await expect(picker(page)).toHaveCount(0);
  });
});

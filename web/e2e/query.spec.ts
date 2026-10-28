import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { tableNode, waitForCanvas } from "./support/canvas";
import { dock, newSqlTab, replaceSql } from "./support/query";

test.describe("query editor and results", () => {
  test("Cmd/Ctrl+Enter runs; 2,000 rows render virtualized and stay responsive", async ({ page }) => {
    const { phase23 } = await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await waitForCanvas(page);
    await expect(page.getByRole("tab", { name: "query-1.sql" })).toHaveCount(0);
    await newSqlTab(page, "SELECT * FROM shop.orders LIMIT 2000");
    await expect(page.getByRole("tab", { name: "query-1.sql" })).toHaveAttribute("aria-selected", "true");
    await page.getByLabel("Max rows").selectOption("5000");
    await page.getByRole("tabpanel").locator(".cm-content").click();
    await page.keyboard.press("ControlOrMeta+Enter");

    const grid = dock(page).getByTestId("result-grid");
    await expect(grid).toBeVisible();
    await expect(dock(page).getByTestId("result-summary")).toContainText("2,000 rows");
    expect(phase23.queries.at(-1)).toEqual({ sql: "SELECT * FROM shop.orders LIMIT 2000", maxRows: 5000 });
    await expect(page.getByTestId("last-run")).toContainText("2,000 rows");

    // Virtualized: only the visible window of rows is in the DOM.
    const rendered = await grid.locator('[role="row"][data-row]').count();
    expect(rendered).toBeGreaterThan(5);
    expect(rendered).toBeLessThan(80);

    // NULL is italic "NULL"; empty strings stay empty; numbers right-aligned.
    await expect(grid.locator('[data-cell="0:4"] [data-kind="null"]')).toHaveText("NULL");
    await expect(grid.locator('[data-cell="1:4"] [data-empty]')).toHaveText("");
    await expect(grid.locator('[data-cell="0:0"]')).toHaveClass(/text-right/);

    // Scrolling to the end stays fast and renders the last row.
    const frames = await page.evaluate(async () => {
      const el = document.querySelector('[data-testid="result-grid"]') as HTMLElement;
      const times: number[] = [];
      let last = performance.now();
      for (let i = 1; i <= 30; i++) {
        el.scrollTop = (el.scrollHeight * i) / 30;
        await new Promise(requestAnimationFrame);
        const now = performance.now();
        times.push(now - last);
        last = now;
      }
      times.sort((a, b) => a - b);
      return { median: times[15] ?? 0 };
    });
    expect(frames.median).toBeLessThan(50);
    await expect(grid.locator('[role="row"][aria-rowindex="2001"]')).toBeVisible();

    // Keyboard: the grid is one tab stop; arrows move the active cell.
    await grid.locator('[data-cell="1999:1"]').click();
    await page.keyboard.press("ControlOrMeta+Home");
    await expect(grid.locator('[data-cell="0:0"]')).toBeFocused();
    await page.keyboard.press("ArrowRight");
    await expect(grid.locator('[data-cell="0:1"]')).toBeFocused();
  });

  test("switching connections clears the previous connection's results, messages and canvas overlay", async ({
    page,
  }) => {
    await stubAgent(page, {
      profiles: [stubProfile(), stubProfile({ id: "01JB00000000000000000000BB", name: "reports" })],
    });
    await openApp(page);
    await waitForCanvas(page);
    await newSqlTab(page, "SELECT * FROM shop.orders LIMIT 20");
    await page.getByRole("tabpanel").locator(".cm-content").click();
    await page.keyboard.press("ControlOrMeta+Enter");
    await expect(dock(page).getByTestId("result-grid")).toBeVisible();

    await page.getByRole("button", { name: /^Connection: orders-dev/ }).click();
    await page.getByRole("menuitemradio", { name: /reports/ }).click();
    await expect(page.getByRole("button", { name: /^Connection: reports/ })).toBeVisible();
    await expect(dock(page).getByTestId("result-grid")).toHaveCount(0);
    await expect(page.getByTestId("last-run")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Clear overlay" })).toHaveCount(0);
  });

  test("CSV export escapes formulas, quotes fields and keeps NULL distinct from empty", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await newSqlTab(page, "SELECT * FROM shop.orders LIMIT 8");
    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect(dock(page).getByTestId("result-summary")).toContainText("8 rows");

    const [download] = await Promise.all([
      page.waitForEvent("download"),
      dock(page).getByRole("button", { name: "Export CSV" }).click(),
    ]);
    expect(download.suggestedFilename()).toMatch(/^orders-dev-\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d\.csv$/);
    const csv = readFileSync(await download.path(), "utf8");
    const lines = csv.split("\r\n");
    expect(lines[0]).toBe("id,customer,status,total,note,placed_at");
    expect(lines[1]).toBe("1,Customer 01,pending,10.00,,2026-10-01 09:00:00+00"); // NULL → empty field
    expect(lines[2]).toBe('2,Customer 02,paid,10.37,"",2026-10-02 09:01:00+00'); // "" → quoted empty
    expect(lines[3]).toBe("3,Customer 03,shipped,10.74,'=SUM(A1:A9),2026-10-03 09:02:00+00");
    expect(lines[4]).toBe('4,Customer 04,refunded,11.11,"gift, ""fragile""",2026-10-04 09:03:00+00');
    expect(lines[5]).toContain(",'-5 off,");
    expect(csv).toContain('"line one\nline two"');
    expect(lines[7]).toContain(",'@home,");
    expect(csv.endsWith("\r\n")).toBe(true);
  });

  test("a guard rejection shows the reason in Messages and a squiggle in the editor", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await newSqlTab(page, "SELECT id\nFROM shop.orders\nWHER id = 1");
    await page.keyboard.press("ControlOrMeta+Enter");

    const message = dock(page).getByTestId("run-message");
    await expect(dock(page).getByRole("tab", { name: /Messages/ })).toHaveAttribute("aria-selected", "true");
    await expect(message).toContainText("The SQL has a syntax error");
    await expect(message).toContainText('syntax error at or near "WHER"');
    await expect(message).toContainText("Line 3, column 1 of your SQL.");
    await expect(message.getByTestId("sql-error-mark")).toHaveText("WHER");
    const squiggle = page.locator(".cm-lintRange-error");
    await expect(squiggle).toHaveText("WHER");

    await message.getByRole("button", { name: "Show line 3, column 1 in the editor" }).click();
    await expect(page.locator(".cm-content")).toBeFocused();

    // Editing the text drops the stale squiggle.
    await page.keyboard.press("End");
    await page.keyboard.type(" ");
    await expect(squiggle).toHaveCount(0);
  });

  test("409 cost confirmation → Run anyway → results with the warning", async ({ page }) => {
    const { phase23 } = await stubAgent(page, {
      profiles: [stubProfile({ name: "reports", env: "prod-replica", host: "replica.internal" })],
    });
    await openApp(page);
    await newSqlTab(page, "SELECT * FROM expensive_view LIMIT 5");
    await page.keyboard.press("ControlOrMeta+Enter");

    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toContainText("This query looks expensive");
    await expect(dialog).toContainText("2,450,000");
    await expect(dialog).toContainText("The limit for reports is 1,000,000");
    await dialog.getByRole("button", { name: "Run anyway" }).click();
    await expect(dock(page).getByTestId("result-summary")).toContainText("5 rows");
    expect(phase23.queries.map((q) => q.confirmCost ?? false)).toEqual([false, true]);
    await dock(page)
      .getByRole("tab", { name: /Messages/ })
      .click();
    await expect(dock(page).getByTestId("run-message")).toContainText("because you confirmed it");
  });

  test("Explain shows the plan tree, heatmap badges and an animated join; Clear overlay removes them", async ({
    page,
  }) => {
    const { phase23 } = await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await waitForCanvas(page);
    await newSqlTab(page, "SELECT * FROM shop.orders o JOIN shop.customers c ON c.id = o.customer_id");
    await page.keyboard.press("ControlOrMeta+Shift+Enter");

    await expect(dock(page).getByRole("tab", { name: "Plan" })).toHaveAttribute("aria-selected", "true");
    await expect(dock(page).getByTestId("plan-node")).toHaveCount(4);
    await expect(dock(page).getByTestId("plan-flag")).toContainText("Seq Scan reads about 2.1M rows");
    await expect(dock(page).getByText("Estimates only")).toBeVisible();
    expect(phase23.explains).toEqual([
      { sql: "SELECT * FROM shop.orders o JOIN shop.customers c ON c.id = o.customer_id" },
    ]);
    expect(phase23.queries).toHaveLength(0);
    await dock(page).getByRole("button", { name: "Collapse Hash Join" }).click();
    await expect(dock(page).getByTestId("plan-node")).toHaveCount(1);

    await page.getByRole("tab", { name: "ERD" }).click();
    await page.getByRole("button", { name: "Fit all tables in view" }).click();
    const ordersBadge = tableNode(page, "shop.orders").getByTestId("overlay-badge");
    await expect(ordersBadge).toHaveText(/Problem/);
    await expect(ordersBadge).toHaveAttribute("title", /Seq Scan reads about 2.1M rows/);
    await expect(tableNode(page, "shop.customers").getByTestId("overlay-badge")).toHaveText(/OK/);
    await expect(tableNode(page, "app.users")).toHaveAttribute("data-dim", "true");
    await expect(tableNode(page, "shop.orders")).not.toHaveAttribute("data-dim", "true");
    // Only the joined foreign key animates.
    await expect(page.getByTestId("edge-particles")).toHaveCount(1);
    await expect(page.locator(".erd-edge-flow")).toHaveCount(1);
    await expect(page.locator("animateMotion")).toHaveCount(3);

    await page.getByRole("button", { name: "Clear the query overlay" }).click();
    await expect(page.getByTestId("overlay-badge")).toHaveCount(0);
    await expect(page.getByTestId("edge-particles")).toHaveCount(0);
    await expect(tableNode(page, "app.users")).not.toHaveAttribute("data-dim", "true");
  });

  test("Esc on the canvas clears the overlay; reduced motion keeps a static emphasized join", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await waitForCanvas(page);
    await newSqlTab(page, "SELECT 1");
    await page.keyboard.press("ControlOrMeta+Shift+Enter");
    await expect(dock(page).getByTestId("plan-tree")).toBeVisible();
    await page.getByRole("tab", { name: "ERD" }).click();
    await page.getByRole("button", { name: "Fit all tables in view" }).click();
    await expect(page.locator(".erd-edge-flow")).toHaveCount(1);
    await expect(page.getByTestId("edge-particles")).toHaveCount(0);
    await page.getByTestId("erd-canvas").focus();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("overlay-badge")).toHaveCount(0);
  });

  test("Cmd/Ctrl+. cancels a running query; the elapsed timer shows meanwhile", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await newSqlTab(page, "SELECT pg_sleep(10)");
    await page.keyboard.press("ControlOrMeta+Enter");
    const status = page.getByTestId("run-status");
    await expect(status).toContainText("Running…");
    await expect(status).toContainText(/\d+ ms|\d\.\d\d s/);
    await expect(page.getByRole("button", { name: "Cancel", exact: true })).toBeVisible();
    await page.keyboard.press("ControlOrMeta+.");
    await expect(status).toHaveCount(0);
    await page.keyboard.press("ControlOrMeta+J"); // a cancel does not open the dock by itself
    await dock(page)
      .getByRole("tab", { name: /Messages/ })
      .click();
    await expect(dock(page).getByTestId("run-message")).toContainText("Cancelled");
  });

  test("running with Cmd/Ctrl+Enter closes an open completion list", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await waitForCanvas(page);
    await newSqlTab(page, "SELECT * FROM shop.orders ORDER BY id ");
    await page.getByRole("tabpanel").locator(".cm-content").press("End");
    await page.keyboard.type("DES");
    await expect(page.locator(".cm-tooltip-autocomplete")).toBeVisible();
    await page.keyboard.press("ControlOrMeta+Enter");
    await expect(dock(page).getByTestId("result-grid")).toBeVisible();
    await expect(page.locator(".cm-tooltip-autocomplete")).toHaveCount(0);
  });

  test("clicking a mapped column header selects that table on the canvas", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await waitForCanvas(page);
    await newSqlTab(page, "SELECT * FROM shop.orders LIMIT 3");
    await page.keyboard.press("ControlOrMeta+Enter");
    await dock(page).getByRole("button", { name: "customers.name" }).click();
    await expect(page.getByRole("tab", { name: "ERD" })).toHaveAttribute("aria-selected", "true");
    await expect(tableNode(page, "shop.customers")).toHaveAttribute("data-mark", "selected");
  });

  test("history lists runs; clicking one opens its SQL in a new tab; tabs persist across reloads", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    const editor = await newSqlTab(page, "SELECT * FROM shop.orders LIMIT 3");
    await page.keyboard.press("ControlOrMeta+Enter");
    await expect(dock(page).getByTestId("result-summary")).toBeVisible();
    await replaceSql(page, editor, "SELECT 1 WHER");
    await page.keyboard.press("ControlOrMeta+Enter");
    await expect(dock(page).getByTestId("run-message")).toBeVisible();

    await page.getByRole("navigation", { name: "Workbench" }).getByRole("button", { name: "Query history" }).click();
    const history = page.getByRole("complementary", { name: "Query history" });
    const entries = history.getByTestId("history-entry");
    await expect(entries).toHaveCount(2);
    await expect(entries.nth(0)).toContainText("Rejected");
    await expect(entries.nth(1)).toContainText("OK");
    await expect(entries.nth(1)).toContainText("3 rows");
    await entries.nth(1).click();
    await expect(page.getByRole("tab", { name: "query-2.sql" })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("tabpanel").locator(".cm-content")).toHaveText("SELECT * FROM shop.orders LIMIT 3");

    await page.reload();
    await expect(page.getByRole("tab", { name: "query-1.sql" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "query-2.sql" })).toHaveAttribute("aria-selected", "true");
    await page.getByRole("tab", { name: "query-1.sql" }).click();
    await expect(page.getByRole("tabpanel").locator(".cm-content")).toHaveText("SELECT 1 WHER");
  });

  test("palette runs SQL commands: new tab, run, explain, history", async ({ page }) => {
    const { phase23 } = await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    const palette = async (name: RegExp) => {
      await page.keyboard.press("ControlOrMeta+K");
      await page.getByRole("option", { name }).click();
    };
    await palette(/New SQL tab/);
    await expect(page.getByRole("tab", { name: "query-1.sql" })).toBeVisible();
    await page.getByRole("tabpanel").locator(".cm-content").click();
    await page.keyboard.insertText("SELECT * FROM shop.orders LIMIT 2");
    await page.getByRole("tab", { name: "ERD" }).focus();
    await palette(/Run the query in the SQL tab/);
    await expect.poll(() => phase23.queries.length).toBe(1);
    await palette(/Explain the query/);
    await expect.poll(() => phase23.explains.length).toBe(1);
    await palette(/Show query history/);
    await expect(page.getByRole("complementary", { name: "Query history" })).toBeVisible();
  });

  test("the connection form's advanced section sets maxCost", async ({ page }) => {
    const { profiles } = await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await page
      .getByRole("button", { name: /orders-dev/ })
      .first()
      .click();
    await page.getByRole("menuitem", { name: /Edit/ }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: /Advanced/ }).click();
    const cost = dialog.getByLabel(/Cost limit/);
    await expect(cost).toHaveValue("");
    await expect(dialog.getByText(/Blank = environment default/)).toBeVisible();
    await cost.fill("0");
    await dialog.getByRole("button", { name: /^Save/ }).click();
    await expect.poll(() => profiles[0]?.maxCost).toBe(0);
  });
});

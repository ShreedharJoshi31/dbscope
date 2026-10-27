import { expect, type Page, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { centreOn, loadFonts, waitForCanvas } from "./support/canvas";
import { ANTHROPIC, assistant, dock, newSqlTab } from "./support/query";

/** Relative times read Date.now(); pin it so baselines stay stable. */
const NOW = new Date("2026-10-26T10:00:00Z");
const SQL =
  "SELECT o.id, c.name AS customer, o.status, o.total, o.note, o.placed_at\nFROM shop.orders o\nJOIN shop.customers c ON c.id = o.customer_id\nLIMIT 40";

async function runQuery(page: Page) {
  await newSqlTab(page, SQL);
  await page.keyboard.press("ControlOrMeta+Enter");
  await expect(dock(page).getByTestId("result-summary")).toContainText("40 rows");
  // Settle: no caret, no tooltips, no focus ring on the editor.
  await page.locator('[data-region="status"]').click({ position: { x: 4, y: 4 } });
  await page.mouse.move(0, 0);
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme} theme (phase 2 and 3)`, () => {
    test.use({ colorScheme, reducedMotion: "reduce" });
    test.beforeEach(async ({ page }) => {
      // Each test lays out the canvas and runs a query; emulated amd64 baseline runs need the headroom.
      test.slow();
      await page.clock.setFixedTime(NOW);
    });

    test(`editor and results screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC });
      await openApp(page);
      await waitForCanvas(page);
      await runQuery(page);
      await loadFonts(page);
      await expect(page).toHaveScreenshot(`editor-results-${colorScheme}.png`);
    });

    test(`plan tab screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC });
      await openApp(page);
      await waitForCanvas(page);
      await runQuery(page);
      await dock(page).getByRole("tab", { name: "Plan" }).click();
      await page.mouse.move(0, 0);
      await loadFonts(page);
      await expect(dock(page)).toHaveScreenshot(`plan-tab-${colorScheme}.png`);
    });

    test(`canvas heatmap screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC });
      await openApp(page);
      await waitForCanvas(page);
      await runQuery(page);
      await page.getByRole("tab", { name: "ERD" }).click();
      await centreOn(page, "shop.orders", 0.8);
      await page.mouse.move(0, 0);
      await loadFonts(page);
      await expect(page.locator(".erd-canvas")).toHaveScreenshot(`canvas-heatmap-${colorScheme}.png`);
    });

    test(`assistant answer card screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC });
      await openApp(page);
      await waitForCanvas(page);
      const composer = assistant(page).getByTestId("assistant-composer");
      await composer.fill("Who ordered most in the last 30 days?");
      await composer.press("Enter");
      const card = assistant(page).getByTestId("answer-card");
      await expect(card).toHaveAttribute("data-status", "done", { timeout: 10_000 });
      await expect(card.getByTestId("sql-code").locator(".sx-keyword").first()).toBeVisible();
      await page.locator('[data-region="status"]').click({ position: { x: 4, y: 4 } });
      await page.mouse.move(0, 0);
      await loadFonts(page);
      await expect(card).toHaveScreenshot(`assistant-answer-${colorScheme}.png`);
    });

    test(`LLM settings screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page, { profiles: [stubProfile()], llm: ANTHROPIC });
      await openApp(page);
      await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
      await assistant(page).getByRole("button", { name: "Assistant model settings" }).click();
      const dialog = page.getByRole("dialog", { name: "Assistant model" });
      await expect(dialog.getByTestId("api-key-saved")).toBeVisible();
      await expect(dialog.getByRole("region", { name: /What is sent/ })).toContainText("For orders-dev");
      await page.mouse.move(0, 0);
      await loadFonts(page);
      await expect(dialog).toHaveScreenshot(`llm-settings-${colorScheme}.png`);
    });
  });
}

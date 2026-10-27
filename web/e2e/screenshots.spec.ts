import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { centreOn, loadFonts, waitForCanvas } from "./support/canvas";

/** Relative times ("schema snapshot 10 h ago") read Date.now(); pin it so baselines stay stable. */
const NOW = new Date("2026-10-26T10:00:00Z");

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme} theme`, () => {
    test.use({ colorScheme, reducedMotion: "reduce" });
    test.beforeEach(async ({ page }) => {
      await page.clock.setFixedTime(NOW);
    });

    test(`onboarding screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page);
      await openApp(page);
      await expect(page.getByRole("heading", { name: "Connect to PostgreSQL" })).toBeVisible();
      await loadFonts(page);
      await expect(page).toHaveScreenshot(`onboarding-${colorScheme}.png`, { fullPage: true });
    });

    test(`workbench screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page, { profiles: [stubProfile()] });
      await openApp(page);
      await expect(page.getByRole("contentinfo")).toContainText("Connected");
      await waitForCanvas(page);
      await loadFonts(page);
      await expect(page).toHaveScreenshot(`workbench-${colorScheme}.png`);
    });

    test(`prod-replica workbench screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page, {
        profiles: [
          stubProfile({ name: "reports", env: "prod-replica", host: "replica.internal", database: "reports" }),
        ],
      });
      await openApp(page);
      await expect(page.getByRole("contentinfo")).toContainText("Connected");
      await waitForCanvas(page);
      await loadFonts(page);
      await expect(page).toHaveScreenshot(`workbench-prod-replica-${colorScheme}.png`);
    });

    test(`ERD canvas screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page);
      await openApp(page);
      await page.getByRole("button", { name: "Open sample schema" }).click();
      await waitForCanvas(page);
      // Full detail around orders: column rows and badges, crow's feet, the parallel address keys,
      // the self-referencing categories key nearby and an inferred-key label.
      await centreOn(page, "shop.orders", 0.9);
      await page.mouse.move(0, 0);
      await loadFonts(page);
      await expect(page.locator(".erd-canvas")).toHaveScreenshot(`erd-canvas-${colorScheme}.png`);
    });

    test(`ERD canvas overview screenshot (${colorScheme})`, async ({ page }) => {
      await stubAgent(page);
      await openApp(page);
      await page.getByRole("button", { name: "Open sample schema" }).click();
      await waitForCanvas(page);
      await page.mouse.move(0, 0);
      await loadFonts(page);
      await expect(page.locator(".erd-canvas")).toHaveScreenshot(`erd-overview-${colorScheme}.png`);
    });
  });
}

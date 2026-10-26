import { expect, type Page, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";

/** document.fonts.ready resolves before lazily requested faces start loading; request them explicitly. */
async function loadFonts(page: Page) {
  await page.evaluate(async () => {
    await Promise.all([
      document.fonts.load("13px 'Geist Variable'"),
      document.fonts.load("13px 'Geist Mono Variable'"),
    ]);
    await document.fonts.ready;
  });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme} theme`, () => {
    test.use({ colorScheme });

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
      await loadFonts(page);
      await expect(page).toHaveScreenshot(`workbench-prod-replica-${colorScheme}.png`);
    });
  });
}

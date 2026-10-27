import { expect, type Page, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { centreOn, loadFonts, tableNode, waitForCanvas } from "./support/canvas";
import { dropFiles, marker, review } from "./support/migration";
import { MIGRATION_FILES } from "./support/migration-stub";

const NOW = new Date("2026-10-26T10:00:00Z");

async function analyze(page: Page) {
  await stubAgent(page, { profiles: [stubProfile()] });
  await openApp(page);
  await waitForCanvas(page);
  await dropFiles(page, MIGRATION_FILES);
  await expect(review(page).getByTestId("overall-rating")).toHaveText("Critical");
  // The highlighted SQL and both files' editors have rendered.
  await expect(page.getByTestId("migration-file-code")).toHaveCount(2);
  await expect(
    page.getByTestId("statement-detail").getByTestId("sql-code").locator(".sx-keyword").first(),
  ).toBeVisible();
}

/** No caret, tooltip, hover or focus ring in the picture. */
async function settle(page: Page) {
  await page.locator('[data-region="status"]').click({ position: { x: 4, y: 4 } });
  await page.mouse.move(0, 0);
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme} theme (phase 4)`, () => {
    test.use({ colorScheme, reducedMotion: "reduce" });
    test.beforeEach(async ({ page }) => {
      test.slow();
      await page.clock.setFixedTime(NOW);
    });

    test(`migration tab screenshot (${colorScheme})`, async ({ page }) => {
      await analyze(page);
      await loadFonts(page);
      await marker(page, 5).click();
      await settle(page);
      // Revealing a statement centres it after CodeMirror measures; pin the scroll so baselines do not depend on timing.
      await page.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() =>
              requestAnimationFrame(() => {
                const pane = document.querySelector<HTMLElement>('section[aria-label^="Migration files"]');
                if (pane) pane.scrollTop = pane.scrollHeight;
                resolve();
              }),
            ),
          ),
      );
      await expect(page).toHaveScreenshot(`migration-tab-${colorScheme}.png`);
    });

    test(`canvas diff overlay screenshot (${colorScheme})`, async ({ page }) => {
      await analyze(page);
      await marker(page, 2).click();
      await page.getByTestId("statement-detail").getByRole("button", { name: "Show on canvas" }).click();
      await expect(tableNode(page, "shop.customers")).toHaveAttribute("data-mark", "selected");
      // shop.customers right of centre, so the legend (top left) does not cover it.
      await centreOn(page, "shop.customers", 0.7);
      await page.evaluate(() => {
        const c = window.__dbscopeCanvas;
        const v = c?.getViewport();
        if (c && v) c.setViewport({ ...v, x: v.x + 30 });
      });
      await expect(tableNode(page, "shop.addresses")).toHaveAttribute("data-mark", "impact");
      await settle(page);
      await loadFonts(page);
      await expect(page.locator(".erd-canvas")).toHaveScreenshot(`canvas-diff-${colorScheme}.png`);
    });
  });
}

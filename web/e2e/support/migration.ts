import { expect, type Locator, type Page } from "@playwright/test";

/**
 * Drops files on an element the way a browser does: dragenter/dragover, then
 * drop, with a DataTransfer holding real File objects (in the given order).
 */
export async function dropFiles(
  page: Page,
  files: { name: string; sql: string }[],
  selector = '[data-region="centre"]',
) {
  const dataTransfer = await page.evaluateHandle((list) => {
    const dt = new DataTransfer();
    for (const f of list) dt.items.add(new File([f.sql], f.name, { type: "application/sql" }));
    return dt;
  }, files);
  await page.dispatchEvent(selector, "dragenter", { dataTransfer });
  await page.dispatchEvent(selector, "dragover", { dataTransfer });
  await expect(page.getByTestId("migration-drop-overlay")).toBeVisible();
  await page.dispatchEvent(selector, "drop", { dataTransfer });
}

export function review(page: Page): Locator {
  return page.getByTestId("migration-review");
}

export function marker(page: Page, index: number): Locator {
  return page.locator(`.cm-risk-marker[data-statement="${index}"]`);
}

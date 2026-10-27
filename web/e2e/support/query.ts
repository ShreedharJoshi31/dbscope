import { expect, type Locator, type Page } from "@playwright/test";

/** Opens a new SQL tab with the shortcut and types into its editor. */
export async function newSqlTab(page: Page, sql: string): Promise<Locator> {
  // Workbench hotkeys register once the profile list has loaded.
  await expect(page.getByRole("button", { name: /^New SQL tab/ })).toBeVisible();
  const before = await page.getByRole("tab", { name: /\.sql$/ }).count();
  await page.keyboard.press("ControlOrMeta+Alt+N");
  await expect(page.getByRole("tab", { name: /\.sql$/ })).toHaveCount(before + 1);
  const editor = page.getByRole("tabpanel").locator(".cm-content");
  await expect(editor).toBeVisible();
  await editor.click();
  await page.keyboard.insertText(sql);
  return editor;
}

export async function replaceSql(page: Page, editor: Locator, sql: string) {
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText(sql);
}

export function dock(page: Page): Locator {
  return page.getByRole("region", { name: "Results dock" });
}

export function assistant(page: Page): Locator {
  return page.getByRole("complementary", { name: "Assistant" });
}

export const ANTHROPIC = {
  provider: "anthropic" as const,
  model: "claude-opus-5-5",
  summaryModel: "claude-haiku-5-5",
  effort: "medium" as const,
  hasApiKey: true,
};

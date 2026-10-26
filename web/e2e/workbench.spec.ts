import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";

test.describe("workbench", () => {
  test("keyboard shortcuts toggle the explorer, dock and assistant", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);

    const explorer = page.getByRole("complementary", { name: "Schema explorer" });
    const dock = page.getByRole("region", { name: "Results dock" });
    const assistant = page.getByRole("complementary", { name: "Assistant" });
    const explorerToggle = page.getByRole("navigation", { name: "Workbench" }).getByRole("button", {
      name: "Toggle schema explorer",
    });

    await expect(explorer).toBeVisible();
    await expect(assistant).toBeVisible();
    await expect(dock).toBeHidden(); // collapsed until first use
    await expect(explorerToggle).toHaveAttribute("aria-pressed", "true");

    await page.keyboard.press("ControlOrMeta+B");
    await expect(explorer).toBeHidden();
    await expect(explorerToggle).toHaveAttribute("aria-pressed", "false");

    await page.keyboard.press("ControlOrMeta+J");
    await expect(dock).toBeVisible();
    await expect(dock.getByRole("tab", { name: "Results" })).toBeVisible();

    await page.keyboard.press("ControlOrMeta+I");
    await expect(assistant).toBeHidden();

    await page.keyboard.press("ControlOrMeta+B");
    await expect(explorer).toBeVisible();

    // Collapsed state survives a reload.
    await page.reload();
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    await expect(page.getByRole("complementary", { name: "Assistant" })).toBeHidden();
    await expect(page.getByRole("region", { name: "Results dock" })).toBeVisible();
  });

  test("splitters resize from the keyboard", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    const explorer = page.getByRole("complementary", { name: "Schema explorer" });
    const before = (await explorer.boundingBox())?.width ?? 0;

    await page.getByRole("separator", { name: "Resize schema explorer" }).focus();
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");

    await expect.poll(async () => (await explorer.boundingBox())?.width ?? 0).toBeGreaterThan(before);
  });

  test("Ctrl/Cmd+K opens the command palette and ? opens the shortcut sheet", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    // Workbench hotkeys register once the profile list has loaded.
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();

    await page.keyboard.press("ControlOrMeta+K");
    const palette = page.getByRole("dialog", { name: "Command palette" });
    await expect(palette).toBeVisible();
    await page.keyboard.type("dock");
    await page.keyboard.press("Enter");
    await expect(palette).toBeHidden();
    await expect(page.getByRole("region", { name: "Results dock" })).toBeVisible();

    await page.keyboard.press("?");
    await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();
  });

  test("F6 moves focus between regions", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await expect(page.locator('[data-region="centre"]')).toBeFocused();
    await page.keyboard.press("F6");
    await expect(page.locator('[data-region="assistant"]')).toBeFocused();
    await page.keyboard.press("Shift+F6");
    await expect(page.locator('[data-region="centre"]')).toBeFocused();
  });

  test("prod-replica shows a permanent banner and a [PROD] title", async ({ page }) => {
    await stubAgent(page, {
      profiles: [stubProfile({ name: "reports", env: "prod-replica", host: "replica.internal", database: "reports" })],
    });
    await openApp(page);

    const banner = page.getByRole("note", { name: "Production replica warning" });
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("Prod-replica");
    await expect(banner).toContainText("Read-only");
    await expect(banner).toContainText("reports @ replica.internal");
    await expect(banner.getByRole("button")).toHaveCount(0);
    await expect(page).toHaveTitle("[PROD] reports · dbscope");
    await expect(page.getByRole("contentinfo")).toContainText("Read-only");
  });

  test("local connections have no banner", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    await expect(page.getByTestId("prod-banner")).toHaveCount(0);
    await expect(page).toHaveTitle("orders-dev · dbscope");
  });

  test("below 1280px the assistant opens as an overlay drawer", async ({ page }) => {
    await page.setViewportSize({ width: 1100, height: 800 });
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    await expect(page.getByRole("complementary", { name: "Assistant" })).toHaveCount(0);

    await page.keyboard.press("ControlOrMeta+I");
    const drawer = page.getByRole("dialog", { name: "Assistant" });
    await expect(drawer).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(drawer).toBeHidden();
  });

  test("crossing the 1280px breakpoint keeps every panel's open state", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await openApp(page);

    const explorer = page.getByRole("complementary", { name: "Schema explorer" });
    const dock = page.getByRole("region", { name: "Results dock" });
    const inlineAssistant = page.getByRole("complementary", { name: "Assistant" });
    const drawer = page.getByRole("dialog", { name: "Assistant" });
    const assistantToggle = page.getByRole("navigation", { name: "Workbench" }).getByRole("button", {
      name: "Toggle assistant",
    });

    await expect(explorer).toBeVisible();
    await expect(inlineAssistant).toBeVisible();

    // Wide -> narrow: the explorer stays open and the first Cmd/Ctrl+I opens the drawer.
    await page.setViewportSize({ width: 1100, height: 760 });
    await expect(inlineAssistant).toHaveCount(0);
    await expect(explorer).toBeVisible();
    await expect(assistantToggle).toHaveAttribute("aria-pressed", "false");
    await page.keyboard.press("ControlOrMeta+I");
    await expect(drawer).toBeVisible();
    await page.keyboard.press("ControlOrMeta+I");
    await expect(drawer).toBeHidden();
    await assistantToggle.click();
    await expect(drawer).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(drawer).toBeHidden();
    await page.keyboard.press("ControlOrMeta+J");
    await expect(dock).toBeVisible();

    // Narrow -> wide: the explorer, the dock and the inline assistant are as they were.
    await page.setViewportSize({ width: 1440, height: 900 });
    await expect(explorer).toBeVisible();
    await expect(dock).toBeVisible();
    await expect(inlineAssistant).toBeVisible();
    await expect(assistantToggle).toHaveAttribute("aria-pressed", "true");

    // The first Cmd/Ctrl+I hides the inline panel, and that choice survives a round trip.
    await page.keyboard.press("ControlOrMeta+I");
    await expect(inlineAssistant).toBeHidden();
    await page.setViewportSize({ width: 1100, height: 760 });
    await expect(explorer).toBeVisible();
    await expect(dock).toBeVisible();
    // Collapsing the explorer while narrow keeps it collapsed when going wide.
    await page.keyboard.press("ControlOrMeta+B");
    await expect(explorer).toBeHidden();
    await page.setViewportSize({ width: 1440, height: 900 });
    await expect(explorer).toBeHidden();
    await expect(dock).toBeVisible();
    await expect(inlineAssistant).toBeHidden();
    await page.keyboard.press("ControlOrMeta+I");
    await expect(inlineAssistant).toBeVisible();
    await page.keyboard.press("ControlOrMeta+B");
    await expect(explorer).toBeVisible();

    // The persisted layout agrees with what is on screen.
    await page.reload();
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    await expect(page.getByRole("complementary", { name: "Assistant" })).toBeVisible();
    await expect(page.getByRole("region", { name: "Results dock" })).toBeVisible();
  });

  test("deleting a prod-replica connection requires typing its name", async ({ page }) => {
    await stubAgent(page, {
      profiles: [
        stubProfile(),
        stubProfile({ id: "01JB00000000000000000000BB", name: "reports", env: "prod-replica" }),
      ],
    });
    await openApp(page);
    await page.getByRole("button", { name: /^Connection: orders-dev/ }).click();
    await page.getByRole("menuitemradio", { name: /reports/ }).click();
    await expect(page.getByRole("note", { name: "Production replica warning" })).toBeVisible();
    await expect(page.getByRole("menu")).toBeHidden();

    await page.getByRole("button", { name: /^Connection: reports/ }).click();
    await page.getByRole("menuitem", { name: "Delete reports…" }).click();
    const confirm = page.getByRole("alertdialog");
    const deleteButton = confirm.getByRole("button", { name: "Delete connection" });
    await expect(deleteButton).toBeDisabled();
    await confirm.getByLabel(/Type reports to confirm/).fill("reports");
    await deleteButton.click();

    await expect(confirm).toBeHidden();
    await expect(page.getByRole("button", { name: /^Connection: orders-dev/ })).toBeVisible();
    await expect(page.getByTestId("prod-banner")).toHaveCount(0);
  });
});

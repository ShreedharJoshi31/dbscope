import { expect, test } from "@playwright/test";
import { openApp, stubAgent } from "./support/agent-stub";

test.describe("first run", () => {
  test("paste URL, test, save and land in the workbench", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await stubAgent(page);
    await openApp(page);

    await expect(page.getByRole("heading", { name: "Connect to PostgreSQL" })).toBeVisible();
    // The token moved to sessionStorage and left the address bar.
    await expect(page).toHaveURL(/\/$/);

    const url = page.getByLabel("Paste a connection URL");
    await expect(url).toBeFocused();
    await page.evaluate(() => navigator.clipboard.writeText("postgres://app:pw@127.0.0.1:5432/orders?sslmode=disable"));
    await page.keyboard.press("ControlOrMeta+V");

    await expect(page.getByLabel("Host")).toHaveValue("127.0.0.1");
    await expect(page.getByLabel("Database")).toHaveValue("orders");
    await expect(page.getByLabel("User")).toHaveValue("app");
    await expect(page.getByLabel("Name")).toHaveValue("orders@127.0.0.1");
    await expect(url).toHaveValue("");

    // Environment is required and only suggested for loopback hosts.
    await page.getByRole("button", { name: "Use local" }).click();
    await expect(page.getByRole("radio", { name: "local" })).toBeChecked();

    await page.getByRole("button", { name: "Test connection" }).click();
    const result = page.getByRole("region", { name: "Connection test result" });
    await expect(result).toContainText("Connected in 3 ms");
    await expect(result).toContainText("PostgreSQL 17.2");

    await page.getByRole("button", { name: "Save and open" }).click();

    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    await expect(page.getByRole("button", { name: /Connection: orders@127\.0\.0\.1, local/ })).toBeVisible();
    await expect(page.getByRole("contentinfo")).toContainText("Connected");
    await expect(page).toHaveTitle("orders@127.0.0.1 · dbscope");
  });

  test("shows inline validation when saving an empty form", async ({ page }) => {
    await stubAgent(page);
    await openApp(page);

    await page.getByLabel("Name").press("Enter");
    await expect(page.getByText("Enter a name for this connection.")).toBeVisible();
    await expect(page.getByText("Choose the environment this database belongs to.")).toBeVisible();
    await expect(page.getByLabel("Name")).toBeFocused();
  });

  test("without a session token it explains how to open dbscope", async ({ page }) => {
    await stubAgent(page);
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Open dbscope from the URL printed by dbscope ui" })).toBeVisible();
  });
});

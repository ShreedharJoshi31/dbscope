import { expect, test } from "@playwright/test";
import { E2E_TOKEN, openApp, stubAgent, stubProfile } from "./support/agent-stub";

test.describe("first run", () => {
  test("paste URL, test, save and land in the workbench", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await stubAgent(page);
    await openApp(page);

    await expect(page.getByRole("heading", { name: "Connect a database" })).toBeVisible();
    // The token moved to sessionStorage and left the address bar.
    await expect(page).toHaveURL(/\/$/);

    const url = page.getByLabel("Paste a connection URL");
    await expect(url).toBeFocused();
    await page.evaluate(() => navigator.clipboard.writeText("postgres://app:pw@127.0.0.1:5432/orders?sslmode=disable"));
    await page.keyboard.press("ControlOrMeta+V");

    await expect(page.getByLabel("Host")).toHaveValue("127.0.0.1");
    await expect(page.getByLabel("Database", { exact: true })).toHaveValue("orders");
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

  test("pasting a new session link into a tab with a rejected token recovers", async ({ page }) => {
    await stubAgent(page, { profiles: [stubProfile()] });
    await page.goto("/#t=stale-token-from-an-old-agent");
    await expect(page.getByText("The dbscope agent did not accept this session.", { exact: false })).toBeVisible();

    // Same document, only the fragment changes: the browser fires hashchange instead of loading the page.
    await page.goto(`/#t=${E2E_TOKEN}`);
    await expect(page.getByRole("complementary", { name: "Schema explorer" })).toBeVisible();
    await expect(page).toHaveURL(/\/$/);
  });

  test("pasting a session link into a tab that had no token recovers", async ({ page }) => {
    await stubAgent(page);
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Open dbscope from the URL printed by dbscope ui" })).toBeVisible();

    await page.goto(`/#t=${E2E_TOKEN}`);
    await expect(page.getByRole("heading", { name: "Connect a database" })).toBeVisible();
    await expect(page).toHaveURL(/\/$/);
  });
});

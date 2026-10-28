import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { tableNode, waitForCanvas } from "./support/canvas";
import { dock } from "./support/query";
import { sampleModel } from "./support/schema-stub";

test("design risks show on their cards, in the table details and in the Schema risks tab", async ({ page }) => {
  const schema = sampleModel();
  for (const r of schema.relations ?? []) {
    if (`${r.schema}.${r.name}` === "shop.inventory") {
      r.findings = [{ ruleId: "DBS-TEST-NO-INDEX", level: "danger", message: "every query reads the whole table" }];
    }
    if (`${r.schema}.${r.name}` === "shop.customers") {
      r.findings = [
        { ruleId: "DBS-TEST-NULLABLE", level: "warn", message: "email is nullable in the key", column: "email" },
        { ruleId: "DBS-TEST-NOTE", level: "info", message: "no expiry set" },
      ];
    }
  }
  await stubAgent(page, { profiles: [stubProfile()], schema });
  await openApp(page);
  await waitForCanvas(page);

  const badge = tableNode(page, "shop.customers").getByTestId("risk-badge");
  await expect(badge).toHaveText(/2 risks/);
  await expect(badge).toHaveAttribute("data-level", "warn");
  await expect(tableNode(page, "shop.inventory").getByTestId("risk-badge")).toHaveAttribute("data-level", "danger");
  await expect(tableNode(page, "shop.orders").getByTestId("risk-badge")).toHaveCount(0);

  await page.getByRole("button", { name: "Toggle results dock" }).click();
  // The count leaves out info-level findings.
  const tab = dock(page).getByRole("tab", { name: /Schema risks/ });
  await expect(tab).toContainText("2");
  await tab.click();
  const rows = dock(page).getByTestId("schema-risk-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0)).toHaveAttribute("data-level", "danger");
  await expect(rows.nth(2)).toHaveAttribute("data-level", "info");

  await rows.nth(1).getByRole("button", { name: "shop.customers.email" }).click();
  const details = page.getByTestId("table-details");
  await expect(details).toContainText("shop.customers");
  await expect(details.getByRole("list", { name: "Design risks" }).getByRole("listitem")).toHaveCount(2);
});

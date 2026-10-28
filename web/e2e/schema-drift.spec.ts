import { expect, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { tableNode, waitForCanvas } from "./support/canvas";
import { dock } from "./support/query";
import { sampleModel } from "./support/schema-stub";

const STAGING = "01JB00000000000000000000BB";

/** The sample schema with one column retyped, one table removed and one table added. */
function changed() {
  const m = sampleModel();
  const rels = (m.relations ?? []).filter((r) => `${r.schema}.${r.name}` !== "shop.inventory");
  const customers = rels.find((r) => `${r.schema}.${r.name}` === "shop.customers") as {
    columns: { name: string; type: string }[];
  };
  const email = customers.columns.find((c) => c.name === "email");
  if (email) email.type = "text";
  customers.columns.push({ name: "loyalty_tier", type: "text" });
  rels.push({
    schema: "shop",
    name: "wishlists",
    kind: "table",
    rowEstimate: 0,
    pages: 0,
    columns: [{ name: "id", type: "bigint", nullable: false }],
  });
  m.relations = rels;
  return m;
}

test("comparing with another connection marks the canvas and lists every difference", async ({ page }) => {
  await stubAgent(page, {
    profiles: [stubProfile(), stubProfile({ id: STAGING, name: "orders-staging", env: "staging" })],
    schema: changed(),
    schemasByProfile: { [STAGING]: sampleModel() },
  });
  await openApp(page);
  await waitForCanvas(page);
  await page.keyboard.press("ControlOrMeta+K");
  await page.getByRole("option", { name: /Compare schema/ }).click();
  const dialog = page.getByRole("dialog", { name: "Compare schema" });
  await dialog.getByRole("radio", { name: "Another connection" }).click();
  await expect(dialog.getByRole("combobox")).toHaveValue(STAGING);
  await dialog.getByRole("button", { name: "Compare" }).click();
  await expect(dialog).toBeHidden();

  const rows = dock(page).getByTestId("drift-row");
  await expect(dock(page).getByText(/4 differences from/)).toBeVisible();
  await expect(rows.filter({ hasText: "shop.inventory" })).toHaveAttribute("data-change", "dropped");
  await expect(rows.filter({ hasText: "shop.customers.email" })).toContainText("→ text");
  await expect(rows.filter({ hasText: "shop.customers.loyalty_tier" })).toHaveAttribute("data-change", "added");
  const legend = page.getByRole("region", { name: "Comparison overlay" });
  await expect(legend).toContainText("Compared vs orders-staging");
  await expect(legend.getByRole("button", { name: "Clear comparison" })).toBeVisible();

  // A row shows its table on the canvas, where the badge says how it differs.
  await rows.filter({ hasText: "shop.customers.email" }).getByRole("button").click();
  await expect(tableNode(page, "shop.customers").getByTestId("diff-badge")).toContainText("altered");
  // The added column is already a row of the card, so no placeholder strip repeats it.
  await expect(tableNode(page, "shop.customers").getByTestId("added-columns")).toHaveCount(0);
  await rows.filter({ hasText: "shop.wishlists" }).getByRole("button").click();
  await expect(tableNode(page, "shop.wishlists").getByTestId("diff-badge")).toContainText("added");

  await dock(page).getByRole("button", { name: "Clear" }).click();
  await expect(tableNode(page, "shop.customers").getByTestId("diff-badge")).toHaveCount(0);
});

test("an earlier snapshot of the same connection can be compared", async ({ page }) => {
  await stubAgent(page, {
    profiles: [stubProfile()],
    schema: changed(),
    snapshots: {
      "01JB00000000000000000000AA": [
        { id: 2, takenAt: new Date().toISOString(), model: changed() },
        { id: 1, takenAt: new Date(Date.now() - 3 * 86400_000).toISOString(), model: sampleModel() },
      ],
    },
  });
  await openApp(page);
  await waitForCanvas(page);
  await page.keyboard.press("ControlOrMeta+K");
  await page.getByRole("option", { name: /Compare schema/ }).click();
  const dialog = page.getByRole("dialog", { name: "Compare schema" });
  await expect(dialog.getByRole("combobox")).toContainText("3 days ago");
  await dialog.getByRole("button", { name: "Compare" }).click();
  await expect(dock(page).getByText(/4 differences from the schema from 3 days ago/)).toBeVisible();
});

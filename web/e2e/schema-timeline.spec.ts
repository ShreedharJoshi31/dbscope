import { expect, type Page, test } from "@playwright/test";
import { openApp, stubAgent, stubProfile } from "./support/agent-stub";
import { centreOn, tableNode, waitForCanvas } from "./support/canvas";
import { sampleModel } from "./support/schema-stub";

const PROFILE = "01JB00000000000000000000AA";
const DAY = 86_400_000;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

/** The oldest snapshot read only app and shop. */
function first() {
  const m = sampleModel();
  m.schemas = ["app", "shop"];
  m.relations = (m.relations ?? []).filter((r) => r.schema !== "billing");
  return m;
}

/** billing is read now, and shop.customers.email became text. */
function second() {
  const m = sampleModel();
  const customers = m.relations?.find((r) => `${r.schema}.${r.name}` === "shop.customers");
  const email = customers?.columns?.find((c) => c.name === "email");
  if (email) email.type = "text";
  return m;
}

/** shop.inventory removed and shop.wishlists added: the live schema. */
function third() {
  const m = second();
  m.relations = (m.relations ?? []).filter((r) => `${r.schema}.${r.name}` !== "shop.inventory");
  m.relations.push({
    schema: "shop",
    name: "wishlists",
    kind: "table",
    rowEstimate: 0,
    pages: 0,
    columns: [{ name: "id", type: "bigint", nullable: false }],
  });
  return m;
}

const timeline = (page: Page) => page.getByRole("region", { name: "Schema timeline" });
const options = (page: Page) => timeline(page).getByRole("option");

test("the timeline steps through snapshots, marks each step's changes and returns to live", async ({ page }) => {
  await stubAgent(page, {
    profiles: [stubProfile()],
    schema: third(),
    snapshots: {
      [PROFILE]: [
        { id: 3, takenAt: ago(2 * DAY), lastSeenAt: ago(60_000), model: third() },
        { id: 2, takenAt: ago(6 * DAY), lastSeenAt: ago(5 * DAY), model: second() },
        { id: 1, takenAt: ago(10 * DAY), lastSeenAt: ago(9 * DAY), model: first() },
      ],
    },
  });
  await openApp(page);
  await waitForCanvas(page);

  // Oldest first, then Live; a gap between each pair of snapshots.
  await expect(options(page)).toHaveCount(4);
  await expect(options(page).last()).toHaveAccessibleName("Live: the current schema");
  await expect(options(page).last()).toHaveAttribute("aria-selected", "true");
  const gaps = timeline(page).getByTestId("timeline-gap");
  await expect(gaps).toHaveCount(2);
  await expect(gaps.first()).toHaveText("no observation");
  await expect(gaps.first()).toHaveAttribute("title", /^No observation from .+ to .+\./);
  await expect(timeline(page).getByRole("button", { name: "Next snapshot" })).toBeDisabled();

  // One step back: the newest snapshot against the one before it.
  await timeline(page).getByRole("button", { name: "Previous snapshot" }).click();
  await expect(options(page).nth(2)).toHaveAttribute("aria-selected", "true");
  const legend = page.getByRole("region", { name: "Timeline overlay" });
  await expect(legend).toBeVisible();
  await expect(legend.getByTestId("timeline-removed")).toContainText("shop.inventory");
  await centreOn(page, "shop.wishlists");
  await expect(tableNode(page, "shop.wishlists").getByTestId("diff-badge")).toContainText("added");
  await expect(tableNode(page, "shop.wishlists").getByTestId("diff-badge")).toHaveAttribute(
    "title",
    /^Added since the previous snapshot/,
  );

  // Keyboard on the focused stop: one more step back.
  await options(page).nth(2).focus();
  await page.keyboard.press("ArrowLeft");
  await expect(options(page).nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(legend.getByTestId("timeline-not-compared")).toContainText("billing");
  await expect(legend.getByTestId("timeline-removed")).toHaveCount(0);
  await expect(tableNode(page, "shop.wishlists")).toHaveCount(0);
  await centreOn(page, "shop.customers");
  await expect(tableNode(page, "shop.customers").getByTestId("diff-badge")).toContainText("altered");

  await page.keyboard.press("Home");
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true");
  await expect(legend).toContainText("The first observation.");
  await expect(timeline(page).getByRole("button", { name: "Previous snapshot" })).toBeDisabled();

  // Live clears the timeline's marks and draws the current schema again.
  await options(page).last().click();
  await expect(options(page).last()).toHaveAttribute("aria-selected", "true");
  await expect(page.getByTestId("migration-legend")).toHaveCount(0);
  await centreOn(page, "shop.wishlists");
  await expect(tableNode(page, "shop.wishlists")).toBeVisible();
  await expect(tableNode(page, "shop.wishlists").getByTestId("diff-badge")).toHaveCount(0);
});

test("a drift comparison replaces a timeline step, and the timeline goes back to live", async ({ page }) => {
  await stubAgent(page, {
    profiles: [stubProfile()],
    schema: third(),
    snapshots: {
      [PROFILE]: [
        { id: 3, takenAt: ago(2 * DAY), lastSeenAt: ago(60_000), model: third() },
        { id: 2, takenAt: ago(6 * DAY), lastSeenAt: ago(5 * DAY), model: second() },
      ],
    },
  });
  await openApp(page);
  await waitForCanvas(page);
  await timeline(page).getByRole("button", { name: "Previous snapshot" }).click();
  await expect(page.getByRole("region", { name: "Timeline overlay" })).toBeVisible();

  await page.keyboard.press("ControlOrMeta+K");
  await page.getByRole("option", { name: /Compare schema/ }).click();
  const dialog = page.getByRole("dialog", { name: "Compare schema" });
  await dialog.getByRole("button", { name: "Compare" }).click();
  await expect(dialog).toBeHidden();

  const comparison = page.getByRole("region", { name: "Comparison overlay" });
  await expect(comparison).toBeVisible();
  await expect(options(page).last()).toHaveAttribute("aria-selected", "true");
  await comparison.getByRole("button", { name: "Clear comparison" }).click();
  await expect(page.getByTestId("migration-legend")).toHaveCount(0);
});

test("the timeline is hidden with fewer than two snapshots", async ({ page }) => {
  await stubAgent(page, {
    profiles: [stubProfile()],
    snapshots: { [PROFILE]: [{ id: 1, takenAt: ago(DAY), model: sampleModel() }] },
  });
  const listed = page.waitForResponse((r) => new URL(r.url()).pathname.endsWith("/snapshots"));
  await openApp(page);
  await waitForCanvas(page);
  await listed;
  await expect(page.getByTestId("erd-canvas")).toBeVisible();
  await expect(timeline(page)).toHaveCount(0);
});
